package io

import (
	crand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/validation"
)

type RunContext struct {
	RunID        contracts.RunID
	RunsBase     string
	WorktreeBase string
	worktrees    map[int]map[contracts.AgentID]contracts.WorktreeAllocation
}

// NewRunID returns a fresh run identifier with the canonical
// YYYY-MM-DD-PR<num>-<hex7> format.
func NewRunID(pr int) contracts.RunID {
	var entropy [4]byte
	if _, err := crand.Read(entropy[:]); err != nil {
		fallback := atomicNowFunc().UnixNano() ^ int64(os.Getpid())
		copy(entropy[:], []byte(fmt.Sprintf("%08x", fallback)))
	}
	suffix := hex.EncodeToString(entropy[:])[:7]
	return contracts.RunID(fmt.Sprintf("%s-PR%d-%s", atomicNowFunc().Format("2006-01-02"), pr, suffix))
}

func NewRunContext(runID contracts.RunID, runsBase, worktreeBase string) (RunContext, error) {
	if err := validation.Instance().Var(runID, "required,run_id_fmt"); err != nil {
		return RunContext{}, err
	}
	if err := contracts.EnsureCleanAbsolutePath(runsBase); err != nil {
		return RunContext{}, err
	}
	if err := contracts.EnsureCleanAbsolutePath(worktreeBase); err != nil {
		return RunContext{}, err
	}
	return RunContext{
		RunID:        runID,
		RunsBase:     runsBase,
		WorktreeBase: worktreeBase,
	}, nil
}

func RunContextFromTaskPackage(pkg contracts.TaskPackage, runsBase, worktreeBase string) (RunContext, error) {
	if err := pkg.Validate(); err != nil {
		return RunContext{}, err
	}
	if err := contracts.EnsureCleanAbsolutePath(runsBase); err != nil {
		return RunContext{}, err
	}
	if err := contracts.EnsureCleanAbsolutePath(worktreeBase); err != nil {
		return RunContext{}, err
	}
	derivedWorktreeBase, err := derivePersistedWorktreeBase(pkg)
	if err != nil {
		return RunContext{}, err
	}
	if !sameCanonicalPath(derivedWorktreeBase, worktreeBase) {
		return RunContext{}, fmt.Errorf("%w: configured=%q persisted=%q", ErrWorktreeBaseMismatch, worktreeBase, derivedWorktreeBase)
	}
	ctx := RunContext{
		RunID:        pkg.RunID,
		RunsBase:     runsBase,
		WorktreeBase: worktreeBase,
	}
	ctx.worktrees = make(map[int]map[contracts.AgentID]contracts.WorktreeAllocation, 2)
	for _, worktree := range pkg.Worktrees {
		if err := validatePersistedWorktreeAllocation(pkg.RunID, worktree, worktreeBase); err != nil {
			return RunContext{}, err
		}
		if _, ok := ctx.worktrees[worktree.Pass]; !ok {
			ctx.worktrees[worktree.Pass] = make(map[contracts.AgentID]contracts.WorktreeAllocation)
		}
		ctx.worktrees[worktree.Pass][worktree.Agent] = worktree
	}
	return ctx, nil
}

func sameCanonicalPath(left, right string) bool {
	leftKey, err := contracts.CanonicalizePathForUniqueness(left)
	if err != nil {
		return false
	}
	rightKey, err := contracts.CanonicalizePathForUniqueness(right)
	if err != nil {
		return false
	}
	return leftKey == rightKey
}

func (ctx RunContext) RunDir() string {
	return filepath.Join(ctx.RunsBase, string(ctx.RunID))
}

func (ctx RunContext) ResolveRunRelative(path string) (string, error) {
	if err := contracts.EnsureCleanRelativePath(path); err != nil {
		return "", err
	}
	return filepath.Join(ctx.RunDir(), path), nil
}

func (ctx RunContext) TaskPackagePath() string {
	return filepath.Join(ctx.RunDir(), "task-package.json")
}

func (ctx RunContext) BaseSHAPath() string {
	return filepath.Join(ctx.RunDir(), "base.sha")
}

func (ctx RunContext) ProcessedPath() string {
	return filepath.Join(ctx.RunsBase, "processed.jsonl")
}

func (ctx RunContext) RulesRegistryPath() string {
	return filepath.Join(ctx.RunsBase, "rules-registry.jsonl")
}

func (ctx RunContext) RulesIdempotencyIndexPath() string {
	return filepath.Join(ctx.RunsBase, "rules-idempotency-index.jsonl")
}

func (ctx RunContext) PromotionLockPath() string {
	return filepath.Join(ctx.RunsBase, "promotion.lock")
}

func (ctx RunContext) Pass1WorktreePath(agent contracts.AgentID) (string, error) {
	return ctx.worktreePath(1, agent)
}

func (ctx RunContext) Pass2WorktreePath(agent contracts.AgentID) (string, error) {
	return ctx.worktreePath(2, agent)
}

func (ctx RunContext) ManifestPath(pass int, agent contracts.AgentID) (string, error) {
	dir, err := ctx.agentDir(pass, agent)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "manifest.json"), nil
}

func (ctx RunContext) worktreePath(pass int, agent contracts.AgentID) (string, error) {
	if err := validatePass(pass); err != nil {
		return "", err
	}
	if err := validation.Instance().Var(agent, "required,agent_id_fmt"); err != nil {
		return "", err
	}
	if ctx.worktrees == nil {
		return "", ErrWorktreePathUnavailable
	}
	if allocation, ok := ctx.worktrees[pass][agent]; ok {
		return allocation.Path, nil
	}
	return "", ErrWorktreePathUnavailable
}

func (ctx RunContext) ValidateWorktreeAllocation(allocation contracts.WorktreeAllocation) error {
	if err := allocation.Validate(); err != nil {
		return err
	}
	return ctx.ValidateWorktreePath(allocation.Path)
}

func (ctx RunContext) ValidateWorktreePath(path string) error {
	if err := contracts.EnsureCleanAbsolutePath(ctx.WorktreeBase); err != nil {
		return err
	}
	if err := contracts.EnsureCleanAbsolutePath(path); err != nil {
		return err
	}
	baseKey, err := contracts.CanonicalizePathForUniqueness(ctx.WorktreeBase)
	if err != nil {
		return err
	}
	pathKey, err := contracts.CanonicalizePathForUniqueness(path)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(baseKey, pathKey)
	if err != nil {
		return err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: worktree_base=%q path=%q rel=%q", ErrWorktreePathEscapesBase, ctx.WorktreeBase, path, rel)
	}
	return nil
}

func (ctx RunContext) passDir(pass int) (string, error) {
	switch pass {
	case 1:
		return filepath.Join(ctx.RunDir(), "20-pass1"), nil
	case 2:
		return filepath.Join(ctx.RunDir(), "50-pass2"), nil
	default:
		return "", ErrInvalidPass
	}
}

func (ctx RunContext) agentDir(pass int, agent contracts.AgentID) (string, error) {
	if err := validatePass(pass); err != nil {
		return "", err
	}
	if err := validation.Instance().Var(agent, "required,agent_id_fmt"); err != nil {
		return "", err
	}
	passDir, _ := ctx.passDir(pass)
	return filepath.Join(passDir, string(agent)), nil
}

func LoadFinalizedManifest(ctx RunContext, pass int, agent contracts.AgentID) (*contracts.Manifest, error) {
	path, err := ctx.ManifestPath(pass, agent)
	if err != nil {
		return nil, err
	}
	manifest, err := ReadJSON[contracts.Manifest](path)
	if err != nil {
		return nil, err
	}
	if err := validateManifestIdentity(manifest, ctx.RunID, pass, agent); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func LoadScorableManifest(ctx RunContext, pass int, agent contracts.AgentID) (*contracts.ManifestSuccess, error) {
	manifest, err := LoadFinalizedManifest(ctx, pass, agent)
	if err != nil {
		return nil, err
	}
	switch v := manifest.Value.(type) {
	case contracts.ManifestSuccess:
		return &v, nil
	case *contracts.ManifestSuccess:
		if v == nil {
			return nil, ErrNotScorable
		}
		cloned := *v
		return &cloned, nil
	default:
		return nil, ErrNotScorable
	}
}

func validatePass(pass int) error {
	if pass != 1 && pass != 2 {
		return fmt.Errorf("%w: pass=%d", ErrInvalidPass, pass)
	}
	return nil
}

func derivePersistedWorktreeBase(pkg contracts.TaskPackage) (string, error) {
	if len(pkg.Worktrees) == 0 {
		return "", ErrWorktreePathUnavailable
	}
	base := filepath.Dir(pkg.Worktrees[0].Path)
	if err := contracts.EnsureCleanAbsolutePath(base); err != nil {
		return "", err
	}
	for _, worktree := range pkg.Worktrees[1:] {
		candidateBase := filepath.Dir(worktree.Path)
		if err := contracts.EnsureCleanAbsolutePath(candidateBase); err != nil {
			return "", err
		}
		for {
			rel, err := filepath.Rel(base, candidateBase)
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				break
			}
			parent := filepath.Dir(base)
			if parent == base {
				return "", fmt.Errorf("%w: could not derive common base for %q and %q", ErrWorktreePathEscapesBase, pkg.Worktrees[0].Path, worktree.Path)
			}
			base = parent
		}
	}
	return base, nil
}

func validatePersistedWorktreeAllocation(runID contracts.RunID, allocation contracts.WorktreeAllocation, worktreeBase string) error {
	if err := allocation.Validate(); err != nil {
		return err
	}
	baseKey, err := contracts.CanonicalizePathForUniqueness(worktreeBase)
	if err != nil {
		return err
	}
	pathKey, err := contracts.CanonicalizePathForUniqueness(allocation.Path)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(baseKey, pathKey)
	if err != nil {
		return err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: worktree_base=%q path=%q", ErrWorktreePathEscapesBase, worktreeBase, allocation.Path)
	}
	if !persistedWorktreePathMatches(runID, allocation) {
		wantBase := fmt.Sprintf("%s-pass%d-%s", runID, allocation.Pass, allocation.Agent)
		return fmt.Errorf("io: persisted worktree path mismatch: got=%q want=%q", allocation.Path, filepath.Join(worktreeBase, wantBase))
	}
	return nil
}

func persistedWorktreePathMatches(runID contracts.RunID, allocation contracts.WorktreeAllocation) bool {
	base := filepath.Base(allocation.Path)
	if base == fmt.Sprintf("%s-pass%d-%s", runID, allocation.Pass, allocation.Agent) {
		return true
	}
	if base == fmt.Sprintf("pass%d-%s", allocation.Pass, allocation.Agent) {
		return true
	}
	parent := filepath.Base(filepath.Dir(allocation.Path))
	if base == string(allocation.Agent) && parent == fmt.Sprintf("pass%d", allocation.Pass) {
		return true
	}
	return base == fmt.Sprintf("%d", allocation.Pass) && parent == string(allocation.Agent)
}

func validateManifestIdentity(manifest contracts.Manifest, runID contracts.RunID, pass int, agent contracts.AgentID) error {
	switch value := manifest.Value.(type) {
	case contracts.ManifestSuccess:
		return validateManifestVariantIdentity(value.RunID, value.Pass, value.Agent, runID, pass, agent)
	case *contracts.ManifestSuccess:
		if value == nil {
			return ErrNotScorable
		}
		return validateManifestVariantIdentity(value.RunID, value.Pass, value.Agent, runID, pass, agent)
	case contracts.ManifestError:
		return validateManifestVariantIdentity(value.RunID, value.Pass, value.Agent, runID, pass, agent)
	case *contracts.ManifestError:
		if value == nil {
			return ErrNotScorable
		}
		return validateManifestVariantIdentity(value.RunID, value.Pass, value.Agent, runID, pass, agent)
	case contracts.ManifestTimeout:
		return validateManifestVariantIdentity(value.RunID, value.Pass, value.Agent, runID, pass, agent)
	case *contracts.ManifestTimeout:
		if value == nil {
			return ErrNotScorable
		}
		return validateManifestVariantIdentity(value.RunID, value.Pass, value.Agent, runID, pass, agent)
	default:
		return errors.New("io: unsupported manifest variant")
	}
}

func validateManifestVariantIdentity(actualRunID contracts.RunID, actualPass int, actualAgent contracts.AgentID, expectedRunID contracts.RunID, expectedPass int, expectedAgent contracts.AgentID) error {
	if actualRunID != expectedRunID {
		return fmt.Errorf("io: manifest run_id mismatch: got=%s want=%s", actualRunID, expectedRunID)
	}
	if actualPass != expectedPass {
		return fmt.Errorf("io: manifest pass mismatch: got=%d want=%d", actualPass, expectedPass)
	}
	if actualAgent != expectedAgent {
		return fmt.Errorf("io: manifest agent mismatch: got=%s want=%s", actualAgent, expectedAgent)
	}
	return nil
}
