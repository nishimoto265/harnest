package step10restorebase

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/contracts/stepio"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/validation"
)

var defaultAgents = []contracts.AgentID{"a1", "a2", "a3"}

type Input struct {
	PR            int
	BestBranch    string
	HarnessFiles  bool
	ExpectedRunID contracts.RunID
	RepoRoot      string
	RunCtx        internalio.RunContext
	Agents        []contracts.AgentID
	Now           func() time.Time
}

type Result struct {
	Response stepio.Step10Response
}

type Runner struct {
	GH  GHClient
	Git GitClient
}

func NewRunner() *Runner {
	return &Runner{
		GH:  NewRealGHClient(""),
		Git: NewRealGitClient(),
	}
}

func (r *Runner) Run(ctx context.Context, in Input) (Result, error) {
	if r == nil {
		return Result{}, fmt.Errorf("step10: runner is nil")
	}
	if r.GH == nil {
		return Result{}, fmt.Errorf("step10: gh client is required")
	}
	if r.Git == nil {
		return Result{}, fmt.Errorf("step10: git client is required")
	}

	agents, err := in.validatedAgents()
	if err != nil {
		return Result{}, err
	}
	if err := in.validate(agents); err != nil {
		return Result{}, err
	}

	runID := in.RunCtx.RunID
	if in.ExpectedRunID != "" && runID != in.ExpectedRunID {
		return Result{}, fmt.Errorf(
			"%w: run_id=%s expected_run_id=%s",
			stepio.ErrStep10ExpectedRunIDMismatch,
			runID,
			in.ExpectedRunID,
		)
	}

	prInfo, err := r.GH.PRView(ctx, in.PR, in.RepoRoot)
	if err != nil {
		return Result{}, fmt.Errorf("step10: gh pr view: %w", err)
	}
	if prInfo.Number != in.PR {
		return Result{}, fmt.Errorf("step10: gh pr view: response.pr=%d request.pr=%d", prInfo.Number, in.PR)
	}
	if err := validation.Instance().Var(prInfo.BaseRefOid, "required,sha1_hex"); err != nil {
		return Result{}, fmt.Errorf("step10: base sha: %w", err)
	}
	baseSHA := prInfo.BaseRefOid

	worktrees := make([]contracts.WorktreeAllocation, 0, len(agents)*2)
	worktreesCreated := 0
	for pass := 1; pass <= 2; pass++ {
		for _, agent := range agents {
			path := filepath.Join(in.RunCtx.WorktreeBase, fmt.Sprintf("%s-pass%d-%s", runID, pass, agent))
			branch := fmt.Sprintf("auto-improve/%s/pass%d/%s", runID, pass, agent)

			created, err := r.Git.WorktreeAdd(ctx, in.RepoRoot, path, branch, baseSHA)
			if err != nil {
				return Result{}, fmt.Errorf(
					"step10: git worktree add: pass=%d agent=%s path=%q branch=%q: %w",
					pass,
					agent,
					path,
					branch,
					err,
				)
			}
			if created {
				worktreesCreated++
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

	now := time.Now
	if in.Now != nil {
		now = in.Now
	}

	pkg := contracts.TaskPackage{
		SchemaVersion:           "1",
		RunID:                   runID,
		PR:                      in.PR,
		Title:                   prInfo.Title,
		BaseSHA:                 baseSHA,
		BestBranch:              in.BestBranch,
		ReconstructedTaskPrompt: ReconstructTaskPrompt(in.PR, prInfo.Title, prInfo.Body, prInfo.LinkedIssues),
		Worktrees:               worktrees,
		CreatedAt:               now().UTC(),
	}
	if err := pkg.Validate(); err != nil {
		return Result{}, fmt.Errorf("step10: task package validate: %w", err)
	}

	resp := stepio.Step10Response{
		RunID:            runID,
		TaskPackage:      pkg,
		BaseSHA:          baseSHA,
		WorktreesCreated: worktreesCreated,
	}
	if err := resp.Validate(); err != nil {
		return Result{}, fmt.Errorf("step10: response validate: %w", err)
	}

	if err := internalio.WriteAtomic(in.RunCtx.BaseSHAPath(), []byte(baseSHA+"\n")); err != nil {
		return Result{}, fmt.Errorf("step10: write base sha: %w", err)
	}
	if err := internalio.WriteJSONAtomic(in.RunCtx.TaskPackagePath(), pkg); err != nil {
		return Result{}, fmt.Errorf("step10: write task package: %w", err)
	}

	return Result{Response: resp}, nil
}

func (in Input) validate(agents []contracts.AgentID) error {
	req := stepio.Step10Request{
		PR:            in.PR,
		BestBranch:    in.BestBranch,
		ExpectedRunID: in.ExpectedRunID,
		HarnessFiles:  in.HarnessFiles,
	}
	if err := req.Validate(); err != nil {
		return err
	}
	if err := contracts.EnsureCleanAbsolutePath(in.RepoRoot); err != nil {
		return err
	}
	if err := validation.Instance().Var(in.RunCtx.RunID, "required,run_id_fmt"); err != nil {
		return err
	}
	if err := contracts.EnsureCleanAbsolutePath(in.RunCtx.RunsBase); err != nil {
		return err
	}
	if err := contracts.EnsureCleanAbsolutePath(in.RunCtx.WorktreeBase); err != nil {
		return err
	}
	if len(agents) != 3 {
		return fmt.Errorf("step10: agents must contain exactly 3 entries: got=%d", len(agents))
	}
	return nil
}

func (in Input) validatedAgents() ([]contracts.AgentID, error) {
	agents := in.Agents
	if len(agents) == 0 {
		agents = append([]contracts.AgentID(nil), defaultAgents...)
	} else {
		agents = append([]contracts.AgentID(nil), agents...)
	}

	seen := make(map[contracts.AgentID]struct{}, len(agents))
	for _, agent := range agents {
		if err := validation.Instance().Var(agent, "required,agent_id_fmt"); err != nil {
			return nil, err
		}
		if _, ok := seen[agent]; ok {
			return nil, fmt.Errorf("step10: duplicate agent: %s", agent)
		}
		seen[agent] = struct{}{}
	}
	return agents, nil
}
