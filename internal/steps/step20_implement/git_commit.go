package step20_implement

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/policyartifact"
	"github.com/nishimoto265/auto-improve/internal/steps/agentrunner"
)

func successDiffBytes(ctx context.Context, worktreePath, baseSHA string) ([]byte, error) {
	return collectSuccessDiffBytes(ctx, worktreePath, baseSHA, "step20")
}

func collectCtxFromContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func commitPolicyOverlayBase(ctx context.Context, allocation contracts.WorktreeAllocation, runID contracts.RunID) (contracts.WorktreeAllocation, error) {
	var err error
	allocation, err = adoptExistingPolicyOverlayHead(ctx, allocation)
	if err != nil {
		return allocation, err
	}
	if err := unstagePolicyArtifacts(ctx, allocation); err != nil {
		return allocation, err
	}
	if _, err := gitOutputContext(ctx, identity, allocation.Path, "add", "-A", "-f", "--", policyartifact.OverlayDir); err != nil {
		return allocation, err
	}
	staged, err := gitOutputContext(ctx, strings.TrimSpace, allocation.Path, "diff", "--cached", "--name-only", "--", policyartifact.OverlayDir)
	if err != nil {
		return allocation, err
	}
	if staged == "" {
		return adoptExistingPolicyOverlayHead(ctx, allocation)
	}
	parent := allocation.BaseSHA
	tree, err := gitOutputContext(ctx, strings.TrimSpace, allocation.Path, "write-tree")
	if err != nil {
		return allocation, err
	}
	commitSHA, err := gitOutputContextWithEnv(
		ctx,
		strings.TrimSpace,
		allocation.Path,
		syntheticCommitEnv(),
		"commit-tree",
		tree,
		"-p",
		parent,
		"-m",
		fmt.Sprintf("auto-improve: prepare step20 policy overlay for %s %s", runID, allocation.Agent),
	)
	if err != nil {
		return allocation, err
	}
	if _, err := gitOutputContext(ctx, identity, allocation.Path, "update-ref", "refs/heads/"+allocation.Branch, commitSHA); err != nil {
		return allocation, err
	}
	if _, err := gitOutputContext(ctx, identity, allocation.Path, "reset", "--hard", commitSHA); err != nil {
		return allocation, err
	}
	allocation.BaseSHA = commitSHA
	allocation.HeadSHA = commitSHA
	return allocation, nil
}

func adoptExistingPolicyOverlayHead(ctx context.Context, allocation contracts.WorktreeAllocation) (contracts.WorktreeAllocation, error) {
	out, err := gitOutputContext(ctx, identity, allocation.Path, "diff", "--name-only", "-z", allocation.BaseSHA, "HEAD", "--")
	if err != nil {
		return allocation, err
	}
	if strings.Trim(out, "\x00\r\n\t ") == "" {
		return allocation, nil
	}
	for _, entry := range strings.Split(out, "\x00") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if !policyartifact.Is(entry) || entry == policyartifact.ChecklistResultFile {
			return allocation, fmt.Errorf("step20: cannot prepare policy overlay on advanced implementation head: %s", entry)
		}
	}
	head, err := gitOutputContext(ctx, strings.TrimSpace, allocation.Path, "rev-parse", "HEAD")
	if err != nil {
		return allocation, err
	}
	allocation.BaseSHA = head
	allocation.HeadSHA = head
	return allocation, nil
}

func synthesizeSuccessCommit(ctx context.Context, allocation contracts.WorktreeAllocation, run RunContext) (string, string, error) {
	addArgs := append([]string{"add", "-A", "--", "."}, implementationCommitExcludedPathspecs...)
	if _, err := gitOutputContext(ctx, identity, allocation.Path, addArgs...); err != nil {
		return "", "", err
	}
	if err := unstagePolicyArtifacts(ctx, allocation); err != nil {
		return "", "", err
	}
	diffArgs := append([]string{"diff", "--no-ext-diff", "--cached", "--name-only", "--", "."}, implementationCommitExcludedPathspecs...)
	staged, err := gitOutputContext(ctx, strings.TrimSpace, allocation.Path, diffArgs...)
	if err != nil {
		return "", "", err
	}
	if staged == "" {
		return "", "", errors.New("step20: synthetic success commit found no staged changes")
	}
	parent, err := gitOutputContext(ctx, strings.TrimSpace, allocation.Path, "rev-parse", "HEAD")
	if err != nil {
		return "", "", err
	}
	tree, err := gitOutputContext(ctx, strings.TrimSpace, allocation.Path, "write-tree")
	if err != nil {
		return "", "", err
	}
	commitSHA, err := synthesizeSuccessCommitWithIdentity(ctx, allocation, run, tree, parent)
	if err != nil {
		return "", "", err
	}
	return commitSHA, parent, nil
}

func unstagePolicyArtifacts(ctx context.Context, allocation contracts.WorktreeAllocation) error {
	resetArgs := append([]string{"reset", "--quiet", "--"}, implementationCommitExcludedPathspecsForReset()...)
	_, err := gitOutputContext(ctx, identity, allocation.Path, resetArgs...)
	return err
}

func implementationCommitExcludedPathspecsForReset() []string {
	return []string{
		policyartifact.ChecklistResultFile,
		policyartifact.OverlayDir,
		policyartifact.RepoRegistryFile,
		policyartifact.RepoRulesDir,
	}
}

func rejectCommittedPolicyArtifactChanges(ctx context.Context, allocation contracts.WorktreeAllocation) error {
	out, err := gitOutputContext(ctx, identity, allocation.Path, "diff", "--name-only", "-z", allocation.BaseSHA, "HEAD", "--")
	if err != nil {
		return err
	}
	for _, entry := range strings.Split(out, "\x00") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if entry == policyartifact.ChecklistResultFile {
			continue
		}
		if policyartifact.Is(entry) {
			return fmt.Errorf("step20: committed policy artifact change is not allowed: %s", entry)
		}
	}
	return nil
}

func synthesizeSuccessCommitWithIdentity(ctx context.Context, allocation contracts.WorktreeAllocation, run RunContext, tree, parent string) (string, error) {
	return gitOutputContextWithEnv(
		ctx,
		strings.TrimSpace,
		allocation.Path,
		syntheticCommitEnv(),
		"commit-tree",
		tree,
		"-p",
		parent,
		"-m",
		fmt.Sprintf("auto-improve: synthesize step20 success for %s %s", run.IO.RunID, run.Agent),
	)
}

func finalizeSyntheticSuccessCommit(ctx context.Context, allocation contracts.WorktreeAllocation, commitSHA, parent, errPrefix string) error {
	if _, err := gitOutputContext(ctx, identity, allocation.Path, "update-ref", "refs/heads/"+allocation.Branch, commitSHA, parent); err != nil {
		return err
	}
	if _, err := gitOutputContext(ctx, identity, allocation.Path, "reset", "--mixed", "HEAD"); err != nil {
		return err
	}
	return agentrunner.ValidateSuccessHead(ctx, allocation, commitSHA, errPrefix)
}
