// Package step10restorebase implements Phase 0-G step10 restore-base.
//
// step10 is the first step of an auto-improve run. For a merged PR it:
//
//  1. Fetches PR metadata (title/body/merge commit/linked issues) via `gh`.
//  2. Carves 6 git worktrees from the merge-base (pass1 × 3 agents + pass2 × 3
//     agents).
//  3. Reconstructs the raw task prompt (NOT sanitized; downstream sanitizes).
//  4. Atomically writes `<run>/task-package.json` and `<run>/base.sha`.
//  5. Returns a validated Step10Response.
//
// The orchestrator is responsible for appending the `started` event to
// processed.jsonl before calling this step (see orchestrator.go:116). step10
// does not touch processed.jsonl.
package step10restorebase

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/contracts/stepio"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/policyrepo"
	"github.com/nishimoto265/auto-improve/internal/validation"
)

// DefaultAgents is the canonical 3-agent roster (a1/a2/a3) that populates
// each pass of the worktree matrix when Input.Agents is empty.
var DefaultAgents = []contracts.AgentID{"a1", "a2", "a3"}

var ErrTaskPromptSourceUnavailable = errors.New("step10: task prompt source unavailable")

// Input is the Runner entry point parameter set.
type Input struct {
	PR               int
	BestBranch       string
	PolicyBranch     string
	TaskPromptSource string
	HarnessFiles     bool
	ExpectedRunID    contracts.RunID // optional; empty disables the guard
	RepoRoot         string          // clean absolute path to the managed repo
	Repo             string          // optional expected "owner/name"; validated against repoRoot remote
	RunCtx           internalio.RunContext
	Agents           []contracts.AgentID // defaults to DefaultAgents when empty
	Now              func() time.Time    // test hook; defaults to time.Now().UTC()
	Logger           *slog.Logger
}

// Result wraps the validated Step10Response.
type Result struct {
	Response stepio.Step10Response
}

// Runner is the public step10 entrypoint. Tests inject stub GH/Git clients.
type Runner struct {
	GH  GHClient
	Git GitClient
}

// NewRunner returns a Runner wired to the real `gh` and `git` CLIs.
func NewRunner() *Runner {
	return &Runner{
		GH:  NewGHClient(),
		Git: NewGitClient(),
	}
}

// Run executes the step10 pipeline for the given Input. The Response is
// guaranteed to have passed Step10Response.Validate (which in turn validates
// the embedded TaskPackage).
func (r *Runner) Run(ctx context.Context, in Input) (Result, error) {
	agents, err := r.validateInput(in)
	if err != nil {
		return Result{}, err
	}
	runID := in.RunCtx.RunID
	if in.ExpectedRunID != "" && in.ExpectedRunID != runID {
		return Result{}, fmt.Errorf("step10: expected_run_id mismatch: expected=%s actual=%s", in.ExpectedRunID, runID)
	}

	now := in.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}

	persistedBaseSHA, hasPersistedBaseSHA, err := readPersistedBaseSHA(in.RunCtx.BaseSHAPath())
	if err != nil {
		return Result{}, err
	}
	repoSlug, err := r.resolveRepoSlug(ctx, in.RepoRoot, in.Repo)
	if err != nil {
		return Result{}, err
	}
	pr, err := r.GH.PRView(ctx, in.PR, repoSlug)
	if err != nil {
		return Result{}, fmt.Errorf("step10: gh pr view: %w", err)
	}
	derivedBaseSHA, err := r.deriveBaseSHA(ctx, in.RepoRoot, pr, in.Logger)
	if err != nil {
		return Result{}, err
	}
	baseSHA := derivedBaseSHA
	if hasPersistedBaseSHA {
		if persistedBaseSHA != derivedBaseSHA {
			return Result{}, fmt.Errorf("step10: persisted base.sha=%s disagrees with merge-base=%s", persistedBaseSHA, derivedBaseSHA)
		}
		baseSHA = persistedBaseSHA
	}
	mode := normalizeTaskPromptSource(in.TaskPromptSource)
	usableIssues := usableLinkedIssues(pr.LinkedIssues)
	if mode == TaskPromptSourceIssue && len(usableIssues) == 0 {
		return Result{}, fmt.Errorf("%w: task_prompt.source=issue requires at least one usable linked issue", ErrTaskPromptSourceUnavailable)
	}
	var changedFiles []string
	var diffText string
	if includeDiffContext(mode, usableIssues) {
		diffFrom, diffTo, ok := taskPromptDiffRange(pr, baseSHA)
		if !ok {
			if mode == TaskPromptSourceDiffSynth {
				return Result{}, errors.New("step10: diff_synth requires an immutable merged diff source")
			}
		} else {
			changedFiles, err = r.Git.ChangedFiles(ctx, in.RepoRoot, diffFrom, diffTo)
			if err != nil {
				return Result{}, fmt.Errorf("step10: changed files for task brief: %w", err)
			}
			diffText, err = r.Git.Diff(ctx, in.RepoRoot, diffFrom, diffTo)
			if err != nil {
				return Result{}, fmt.Errorf("step10: diff for task brief: %w", err)
			}
		}
	}
	if in.HarnessFiles && strings.TrimSpace(in.PolicyBranch) != "" {
		if err := policyrepo.HydrateAndSnapshotFromBranch(ctx, in.RepoRoot, in.PolicyBranch, in.RunCtx.RunsBase, in.RunCtx.RunDir()); err != nil {
			return Result{}, fmt.Errorf("step10: hydrate harness files from policy_branch=%s: %w", in.PolicyBranch, err)
		}
	}

	worktrees, created, err := r.carveWorktrees(ctx, in, agents, baseSHA)
	if err != nil {
		return Result{}, err
	}
	prompt := SynthesizeTaskBrief(in.TaskPromptSource, TaskBriefInput{
		PR:           pr.Number,
		Title:        pr.Title,
		Body:         pr.Body,
		Issues:       pr.LinkedIssues,
		ChangedFiles: changedFiles,
		Diff:         diffText,
	})

	pkg := contracts.TaskPackage{
		SchemaVersion:           "1",
		RunID:                   runID,
		PR:                      in.PR,
		Title:                   pr.Title,
		BaseSHA:                 baseSHA,
		BestBranch:              in.BestBranch,
		ReconstructedTaskPrompt: prompt,
		Worktrees:               worktrees,
		CreatedAt:               now(),
	}
	if err := pkg.Validate(); err != nil {
		return Result{}, fmt.Errorf("step10: task_package invariant: %w", err)
	}

	if err := internalio.WriteAtomic(in.RunCtx.BaseSHAPath(), []byte(baseSHA+"\n")); err != nil {
		return Result{}, fmt.Errorf("step10: write base.sha: %w", err)
	}
	if err := internalio.WriteJSONAtomic(in.RunCtx.TaskPackagePath(), pkg); err != nil {
		return Result{}, fmt.Errorf("step10: write task-package.json: %w", err)
	}

	resp := stepio.Step10Response{
		RunID:            runID,
		TaskPackage:      pkg,
		BaseSHA:          baseSHA,
		WorktreesCreated: created,
	}
	if err := resp.Validate(); err != nil {
		return Result{}, fmt.Errorf("step10: response validation: %w", err)
	}
	return Result{Response: resp}, nil
}

func taskPromptDiffRange(pr PRInfo, baseSHA string) (string, string, bool) {
	if pr.MergeCommitOID != "" {
		return baseSHA, pr.MergeCommitOID, true
	}
	return "", "", false
}

func readPersistedBaseSHA(path string) (string, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("step10: read base.sha: %w", err)
	}
	sha := strings.TrimSpace(string(data))
	if err := validation.Instance().Var(sha, "required,sha1_hex"); err != nil {
		return "", true, fmt.Errorf("step10: persisted base.sha is not a 40-hex sha: %q: %w", sha, err)
	}
	return sha, true, nil
}

func (r *Runner) deriveBaseSHA(ctx context.Context, repoRoot string, pr PRInfo, logger *slog.Logger) (string, error) {
	if pr.MergeCommitOID != "" {
		if err := validation.Instance().Var(pr.MergeCommitOID, "required,sha1_hex"); err != nil {
			return "", fmt.Errorf("step10: merge_commit_oid is not a 40-hex sha: %q: %w", pr.MergeCommitOID, err)
		}
		if err := r.Git.FetchCommit(ctx, repoRoot, pr.MergeCommitOID); err != nil {
			return "", fmt.Errorf("step10: fetch merge_commit_oid=%s: %w", pr.MergeCommitOID, err)
		}
		baseSHA, err := r.Git.ResolveRef(ctx, repoRoot, pr.MergeCommitOID+"^1")
		if err != nil {
			return "", fmt.Errorf("step10: resolve merge-base from merge_commit=%s: %w", pr.MergeCommitOID, err)
		}
		if err := validation.Instance().Var(baseSHA, "required,sha1_hex"); err != nil {
			return "", fmt.Errorf("step10: merge-base is not a 40-hex sha: %q: %w", baseSHA, err)
		}
		return baseSHA, nil
	}
	if pr.State == "MERGED" {
		if err := validation.Instance().Var(pr.HeadRefOid, "required,sha1_hex"); err != nil {
			return "", fmt.Errorf("step10: head_ref_oid is not a 40-hex sha: %q: %w", pr.HeadRefOid, err)
		}
		if err := validation.Instance().Var(pr.BaseRefOid, "required,sha1_hex"); err != nil {
			return "", fmt.Errorf("step10: base_ref_oid is not a 40-hex sha: %q: %w", pr.BaseRefOid, err)
		}
		if err := r.Git.FetchCommit(ctx, repoRoot, pr.HeadRefOid); err != nil {
			return "", fmt.Errorf("step10: fetch head_ref_oid=%s: %w", pr.HeadRefOid, err)
		}
		if err := r.Git.FetchCommit(ctx, repoRoot, pr.BaseRefOid); err != nil {
			return "", fmt.Errorf("step10: fetch base_ref_oid=%s: %w", pr.BaseRefOid, err)
		}
		baseSHA, err := r.Git.MergeBase(ctx, repoRoot, pr.HeadRefOid, pr.BaseRefOid)
		if err != nil {
			return "", fmt.Errorf("step10: recover immutable base from head=%s base_tip=%s: %w", pr.HeadRefOid, pr.BaseRefOid, err)
		}
		if err := validation.Instance().Var(baseSHA, "required,sha1_hex"); err != nil {
			return "", fmt.Errorf("step10: recovered immutable base is not a 40-hex sha: %q: %w", baseSHA, err)
		}
		if logger == nil {
			logger = slog.Default()
		}
		logger.Warn("step10: mergeCommit absent; derived immutable base with git merge-base", "pr", pr.Number, "head_ref_oid", pr.HeadRefOid, "base_ref_oid", pr.BaseRefOid)
		return baseSHA, nil
	}
	if pr.State == "" {
		return "", fmt.Errorf("step10 requires a merged PR: state is empty")
	}
	return "", fmt.Errorf("step10 requires a merged PR: state=%s", pr.State)
}

func (r *Runner) validateInput(in Input) ([]contracts.AgentID, error) {
	if r == nil || r.GH == nil || r.Git == nil {
		return nil, fmt.Errorf("step10: runner is missing GH or Git client")
	}
	if in.PR <= 0 {
		return nil, fmt.Errorf("step10: pr must be > 0: got %d", in.PR)
	}
	if in.BestBranch == "" {
		return nil, fmt.Errorf("step10: best_branch is required")
	}
	if err := contracts.EnsureCleanAbsolutePath(in.RepoRoot); err != nil {
		return nil, fmt.Errorf("step10: repo_root: %w", err)
	}
	if err := validation.Instance().Var(in.RunCtx.RunID, "required,run_id_fmt"); err != nil {
		return nil, fmt.Errorf("step10: run_ctx.run_id: %w", err)
	}
	if err := contracts.EnsureCleanAbsolutePath(in.RunCtx.WorktreeBase); err != nil {
		return nil, fmt.Errorf("step10: run_ctx.worktree_base: %w", err)
	}
	if err := contracts.EnsureCleanAbsolutePath(in.RunCtx.RunsBase); err != nil {
		return nil, fmt.Errorf("step10: run_ctx.runs_base: %w", err)
	}

	agents := in.Agents
	if len(agents) == 0 {
		agents = DefaultAgents
	}
	if len(agents) != 3 {
		return nil, fmt.Errorf("step10: agents must have exactly 3 entries: got %d", len(agents))
	}
	seen := map[contracts.AgentID]struct{}{}
	for _, a := range agents {
		if err := validation.Instance().Var(a, "required,agent_id_fmt"); err != nil {
			return nil, fmt.Errorf("step10: agent %q: %w", a, err)
		}
		if _, dup := seen[a]; dup {
			return nil, fmt.Errorf("step10: duplicate agent: %s", a)
		}
		seen[a] = struct{}{}
	}
	return agents, nil
}

func (r *Runner) resolveRepoSlug(ctx context.Context, repoRoot, configuredRepo string) (string, error) {
	repoSlug, err := r.Git.RepoSlug(ctx, repoRoot)
	if configuredRepo == "" {
		if err != nil {
			return "", err
		}
		if repoSlug == "" {
			return "", fmt.Errorf("step10: resolved repo slug is empty for repo_root=%s", repoRoot)
		}
		return repoSlug, nil
	}
	if err == nil && repoSlug != "" && !strings.EqualFold(configuredRepo, repoSlug) {
		return "", fmt.Errorf("step10: repo mismatch: configured=%s resolved=%s", configuredRepo, repoSlug)
	}
	if configuredRepo != "" {
		return configuredRepo, nil
	}
	if err != nil {
		return "", err
	}
	if repoSlug == "" {
		return "", fmt.Errorf("step10: resolved repo slug is empty for repo_root=%s", repoRoot)
	}
	return repoSlug, nil
}

// carveWorktrees iterates (pass, agent) in deterministic order (pass 1 first,
// agents in the order given) calling GitClient.WorktreeAdd for each.
func (r *Runner) carveWorktrees(ctx context.Context, in Input, agents []contracts.AgentID, baseSHA string) ([]contracts.WorktreeAllocation, int, error) {
	runID := string(in.RunCtx.RunID)
	worktrees := make([]contracts.WorktreeAllocation, 0, len(agents)*2)
	created := 0
	for pass := 1; pass <= 2; pass++ {
		for _, agent := range agents {
			path := filepath.Join(in.RunCtx.WorktreeBase, fmt.Sprintf("%s-pass%d-%s", runID, pass, agent))
			branch := fmt.Sprintf("auto-improve/%s/pass%d/%s", runID, pass, agent)
			isNew, err := r.Git.WorktreeAdd(ctx, in.RepoRoot, path, branch, baseSHA)
			if err != nil {
				return nil, 0, fmt.Errorf("step10: worktree add (pass=%d agent=%s): %w", pass, agent, err)
			}
			if isNew {
				created++
			}
			worktrees = append(worktrees, contracts.WorktreeAllocation{
				Agent:   agent,
				Pass:    pass,
				Path:    path,
				Branch:  branch,
				BaseSHA: baseSHA,
				HeadSHA: baseSHA,
			})
		}
	}
	return worktrees, created, nil
}
