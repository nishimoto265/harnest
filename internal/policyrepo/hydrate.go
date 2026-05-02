package policyrepo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	internalio "github.com/nishimoto265/auto-improve/internal/io"
)

func HydrateFromBranch(ctx context.Context, repoRoot, branch, runsBase string) error {
	_, err := hydrateSnapshotFromBranch(ctx, repoRoot, branch, runsBase, "")
	return err
}

func HydrateAndSnapshotFromBranch(ctx context.Context, repoRoot, branch, runsBase, runDir string) error {
	_, err := hydrateSnapshotFromBranch(ctx, repoRoot, branch, runsBase, runDir)
	return err
}

func HydrateAndSnapshotFromBranchOrSeed(ctx context.Context, repoRoot, branch, runsBase, runDir string) error {
	_, err := hydrateSnapshotFromBranch(ctx, repoRoot, branch, runsBase, runDir)
	if err == nil {
		return nil
	}
	if !policyBranchHydrationAllowsSeed(err) {
		return err
	}
	seed, seedErr := loadRepoLocalSnapshot(repoRoot)
	if seedErr != nil {
		if errors.Is(seedErr, os.ErrNotExist) {
			return err
		}
		return fmt.Errorf("policyrepo: seed repo-local policy after hydrate failure: %w", seedErr)
	}
	lock, lockErr := internalio.AcquireFileLockContext(ctx, filepath.Join(runsBase, "promotion.lock"))
	if lockErr != nil {
		return lockErr
	}
	defer func() { _ = lock.Unlock() }()
	if strings.TrimSpace(runDir) != "" {
		if applyErr := applySnapshotToRunDir(runDir, seed); applyErr != nil {
			return applyErr
		}
		registryHead, headErr := registryHead(filepath.Join(runDir, "policy", registryLocalName))
		if headErr != nil {
			return headErr
		}
		policyHead, _ := branchHead(ctx, repoRoot, branch)
		if metaErr := writeSnapshotMetadata(runDir, SnapshotMetadata{
			SchemaVersion: "1",
			PolicyBranch:  branch,
			PolicyHead:    policyHead,
			RegistryHead:  registryHead,
		}); metaErr != nil {
			return metaErr
		}
	}
	return applySnapshot(runsBase, seed)
}

func policyBranchHydrationAllowsSeed(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "couldn't find remote ref") ||
		strings.Contains(msg, "could not find remote ref") ||
		strings.Contains(msg, "has no managed policy files") ||
		strings.Contains(msg, "is missing "+RegistryRepoRelPath)
}

func SnapshotLocalForRun(ctx context.Context, runsBase, runDir string) error {
	lock, err := internalio.AcquireFileLockContext(ctx, filepath.Join(runsBase, "promotion.lock"))
	if err != nil {
		return err
	}
	defer func() { _ = lock.Unlock() }()

	snap, err := loadOptionalLocalSnapshot(runsBase)
	if err != nil {
		return err
	}
	if err := applySnapshotToRunDir(runDir, snap); err != nil {
		return err
	}
	registryPath := filepath.Join(runDir, "policy", registryLocalName)
	if len(snap.registry) == 0 {
		if err := internalio.WriteAtomic(registryPath, nil); err != nil {
			return err
		}
	}
	registryHead, err := registryHead(registryPath)
	if err != nil {
		return err
	}
	return writeSnapshotMetadata(runDir, SnapshotMetadata{
		SchemaVersion: "1",
		RegistryHead:  registryHead,
	})
}

func hydrateSnapshotFromBranch(ctx context.Context, repoRoot, branch, runsBase, runDir string) (snapshot, error) {
	lock, err := internalio.AcquireFileLockContext(ctx, filepath.Join(runsBase, "promotion.lock"))
	if err != nil {
		return snapshot{}, err
	}
	defer func() { _ = lock.Unlock() }()

	if err := fetchBranch(ctx, repoRoot, branch); err != nil {
		return snapshot{}, err
	}
	policyHead, err := branchHead(ctx, repoRoot, branch)
	if err != nil {
		return snapshot{}, err
	}
	snap, err := loadBranchSnapshot(ctx, repoRoot, branch)
	if err != nil {
		return snapshot{}, err
	}
	if strings.TrimSpace(runDir) != "" {
		if err := applySnapshotToRunDir(runDir, snap); err != nil {
			return snapshot{}, err
		}
		registryHead, err := registryHead(filepath.Join(runDir, "policy", registryLocalName))
		if err != nil {
			return snapshot{}, err
		}
		if err := writeSnapshotMetadata(runDir, SnapshotMetadata{
			SchemaVersion: "1",
			PolicyBranch:  branch,
			PolicyHead:    policyHead,
			RegistryHead:  registryHead,
		}); err != nil {
			return snapshot{}, err
		}
	}
	if err := applySnapshot(runsBase, snap); err != nil {
		return snapshot{}, err
	}
	return snap, nil
}
