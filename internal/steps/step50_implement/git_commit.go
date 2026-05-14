package step50_implement

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nishimoto265/harnest/internal/contracts"
	"github.com/nishimoto265/harnest/internal/policyartifact"
	"github.com/nishimoto265/harnest/internal/steps/agentrunner"
)

func successDiffBytes(ctx context.Context, worktreePath, baseSHA string) ([]byte, error) {
	return collectSuccessDiffBytes(ctx, worktreePath, baseSHA, "step50")
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
	policyPathspecs := policyartifact.ExistingPolicyBasePathspecs(allocation.Path)
	if len(policyPathspecs) == 0 {
		return adoptExistingPolicyOverlayHead(ctx, allocation)
	}
	addArgs := append([]string{"add", "-A", "-f", "--"}, policyPathspecs...)
	if _, err := gitOutputContext(ctx, identity, allocation.Path, addArgs...); err != nil {
		return allocation, err
	}
	diffArgs := append([]string{"diff", "--cached", "--name-only", "--"}, policyPathspecs...)
	staged, err := gitOutputContext(ctx, strings.TrimSpace, allocation.Path, diffArgs...)
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
		fmt.Sprintf("auto-improve: prepare step50 policy overlay for %s %s", runID, allocation.Agent),
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

func commitPolicyOverlayPassBase(ctx context.Context, allocation contracts.PassBaseAllocation, runID contracts.RunID) (contracts.PassBaseAllocation, error) {
	if err := unstagePolicyArtifactsPath(ctx, allocation.Path); err != nil {
		return allocation, err
	}
	policyPathspecs := policyartifact.ExistingPolicyBasePathspecs(allocation.Path)
	if len(policyPathspecs) == 0 {
		head, err := gitOutputContext(ctx, strings.TrimSpace, allocation.Path, "rev-parse", "HEAD")
		if err != nil {
			return allocation, err
		}
		allocation.HeadSHA = head
		return allocation, nil
	}
	addArgs := append([]string{"add", "-A", "-f", "--"}, policyPathspecs...)
	if _, err := gitOutputContext(ctx, identity, allocation.Path, addArgs...); err != nil {
		return allocation, err
	}
	diffArgs := append([]string{"diff", "--cached", "--name-only", "--"}, policyPathspecs...)
	staged, err := gitOutputContext(ctx, strings.TrimSpace, allocation.Path, diffArgs...)
	if err != nil {
		return allocation, err
	}
	if staged == "" {
		head, err := gitOutputContext(ctx, strings.TrimSpace, allocation.Path, "rev-parse", "HEAD")
		if err != nil {
			return allocation, err
		}
		allocation.HeadSHA = head
		return allocation, nil
	}
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
		allocation.BaseSHA,
		"-m",
		fmt.Sprintf("auto-improve: prepare pass%d policy base for %s", allocation.Pass, runID),
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
		if !policyartifact.IsPolicyBasePath(entry) || entry == policyartifact.ChecklistResultFile {
			return allocation, fmt.Errorf("step50: cannot prepare policy overlay on advanced implementation head: %s", entry)
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
	if err := stageImplementationChanges(ctx, allocation.Path); err != nil {
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
		return "", "", errors.New("step50: synthetic success commit found no staged changes")
	}
	parent, err := gitOutputContext(ctx, strings.TrimSpace, allocation.Path, "rev-parse", "HEAD")
	if err != nil {
		return "", "", err
	}
	tree, err := gitOutputContext(ctx, strings.TrimSpace, allocation.Path, "write-tree")
	if err != nil {
		return "", "", err
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
		fmt.Sprintf("auto-improve: synthesize step50 success for %s %s", run.IO.RunID, run.Agent),
	)
	if err != nil {
		return "", "", err
	}
	return commitSHA, parent, nil
}

func stageImplementationChanges(ctx context.Context, worktreePath string) error {
	if _, err := gitOutputContext(ctx, identity, worktreePath, "add", "-u"); err != nil {
		return err
	}
	untracked, err := gitOutputBytesContext(ctx, worktreePath, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return err
	}
	for _, entry := range strings.Split(string(untracked), "\x00") {
		if entry == "" || policyartifact.Is(entry) {
			continue
		}
		if err := contracts.EnsureCleanRelativePath(entry); err != nil {
			return err
		}
		if _, err := gitOutputContext(ctx, identity, worktreePath, "add", "--", entry); err != nil {
			return err
		}
	}
	return nil
}

func unstagePolicyArtifacts(ctx context.Context, allocation contracts.WorktreeAllocation) error {
	return unstagePolicyArtifactsPath(ctx, allocation.Path)
}

func unstagePolicyArtifactsPath(ctx context.Context, worktreePath string) error {
	resetArgs := append([]string{"reset", "--quiet", "--"}, implementationCommitExcludedPathspecsForReset()...)
	_, err := gitOutputContext(ctx, identity, worktreePath, resetArgs...)
	return err
}

func implementationCommitExcludedPathspecsForReset() []string {
	return policyartifact.GitResetPathspecs()
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
			return fmt.Errorf("step50: committed policy artifact change is not allowed: %s", entry)
		}
	}
	return nil
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
