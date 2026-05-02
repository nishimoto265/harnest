package policyrepo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/nishimoto265/auto-improve/internal/processenv"
)

var removePreparedPublishWorktree = removeWorktree

func PublishSnapshot(ctx context.Context, repoRoot, branch, expectedHead, runsBase, runID string) (string, error) {
	plan, err := PrepareSnapshotPublish(ctx, repoRoot, branch, expectedHead, runsBase, runID)
	if err != nil {
		return "", err
	}
	defer func() {
		_ = plan.Cleanup()
	}()
	if err := plan.Push(ctx); err != nil {
		return "", err
	}
	return plan.Head, nil
}

func PrepareSnapshotPublish(ctx context.Context, repoRoot, branch, expectedHead, runsBase, runID string) (*PreparedPublish, error) {
	if expectedHead == "" {
		return nil, fmt.Errorf("policyrepo: expected head is required for publish")
	}
	snap, err := loadLocalSnapshot(runsBase)
	if err != nil {
		return nil, err
	}
	if err := fetchBranch(ctx, repoRoot, branch); err != nil {
		return nil, err
	}
	tmpDir, err := os.MkdirTemp(runsBase, "policy-publish-"+sanitizeRunID(runID)+"-")
	if err != nil {
		return nil, err
	}
	plan := &PreparedPublish{
		RepoRoot:     repoRoot,
		Branch:       branch,
		ExpectedHead: expectedHead,
		Head:         expectedHead,
		worktreeDir:  tmpDir,
	}
	if _, err := gitText(ctx, repoRoot, "worktree", "add", "--detach", tmpDir, expectedHead); err != nil {
		_ = plan.Cleanup()
		return nil, err
	}

	if _, err := gitText(ctx, tmpDir, "rm", "-r", "--ignore-unmatch", "--", "."); err != nil {
		_ = plan.Cleanup()
		return nil, err
	}
	if err := syncSnapshotToWorktree(tmpDir, snap); err != nil {
		_ = plan.Cleanup()
		return nil, err
	}
	if _, err := gitText(ctx, tmpDir, "add", "-A", "--", RepoDirName); err != nil {
		_ = plan.Cleanup()
		return nil, err
	}
	hasDiff, err := hasStagedDiff(ctx, tmpDir)
	if err != nil {
		_ = plan.Cleanup()
		return nil, err
	}
	if !hasDiff {
		if err := plan.Cleanup(); err != nil {
			return nil, err
		}
		return plan, nil
	}

	env := processenv.GitLocalEnv()
	env = append(env,
		"GIT_AUTHOR_NAME=auto-improve",
		"GIT_AUTHOR_EMAIL=auto-improve@local",
		"GIT_COMMITTER_NAME=auto-improve",
		"GIT_COMMITTER_EMAIL=auto-improve@local",
		"GIT_AUTHOR_DATE=2000-01-01T00:00:00Z",
		"GIT_COMMITTER_DATE=2000-01-01T00:00:00Z",
	)
	if _, err := runGit(ctx, env, "-C", tmpDir, "commit", "-m", fmt.Sprintf("auto-improve: publish policy snapshot for %s", runID)); err != nil {
		_ = plan.Cleanup()
		return nil, fmt.Errorf("policyrepo: commit policy snapshot: %w", err)
	}
	headBytes, err := gitText(ctx, tmpDir, "rev-parse", "HEAD")
	if err != nil {
		_ = plan.Cleanup()
		return nil, err
	}
	plan.Head = strings.TrimSpace(string(headBytes))
	plan.needsPush = true
	return plan, nil
}

func (p *PreparedPublish) Push(ctx context.Context) error {
	if p == nil {
		return errors.New("policyrepo: prepared publish is required")
	}
	if !p.needsPush {
		return nil
	}
	remoteURL, err := originPushURL(ctx, p.RepoRoot)
	if err != nil {
		return err
	}
	if _, err := runGit(ctx, processenv.GitNetworkEnvForRemoteURL(remoteURL), "-C", p.RepoRoot, "push", "origin", fmt.Sprintf("%s:%s", p.Head, p.Branch), fmt.Sprintf("--force-with-lease=%s:%s", p.Branch, p.ExpectedHead)); err != nil {
		return fmt.Errorf("policyrepo: push policy snapshot: %w", err)
	}
	return nil
}

func (p *PreparedPublish) Cleanup() error {
	if p == nil || p.cleaned || p.worktreeDir == "" {
		return nil
	}
	if err := removePreparedPublishWorktree(p.RepoRoot, p.worktreeDir); err != nil {
		removeErr := os.RemoveAll(p.worktreeDir)
		if removeErr != nil {
			return fmt.Errorf("policyrepo: remove policy worktree after publish: %w; remove temp dir: %v", err, removeErr)
		}
		return fmt.Errorf("policyrepo: remove policy worktree after publish: %w", err)
	}
	p.cleaned = true
	return nil
}
