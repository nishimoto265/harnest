package step10restorebase

import (
	"context"
	"errors"
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

func (in Input) Validate() error {
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
	type agentList struct {
		Agents []contracts.AgentID `validate:"required,len=3,unique,dive,agent_id_fmt"`
	}
	return validation.Instance().Struct(agentList{Agents: in.Agents})
}

func (r *Runner) Run(ctx context.Context, in Input) (Result, error) {
	if r == nil {
		return Result{}, errors.New("step10: runner is required")
	}
	if r.GH == nil {
		return Result{}, errors.New("step10: gh client is required")
	}
	if r.Git == nil {
		return Result{}, errors.New("step10: git client is required")
	}

	if len(in.Agents) == 0 {
		in.Agents = append([]contracts.AgentID(nil), defaultAgents...)
	}
	if in.Now == nil {
		in.Now = func() time.Time { return time.Now().UTC() }
	}
	if err := in.Validate(); err != nil {
		return Result{}, fmt.Errorf("step10: validate input: %w", err)
	}

	runID := in.RunCtx.RunID
	if in.ExpectedRunID != "" && runID != in.ExpectedRunID {
		return Result{}, fmt.Errorf("%w: run_ctx.run_id=%s expected_run_id=%s", stepio.ErrStep10ExpectedRunIDMismatch, runID, in.ExpectedRunID)
	}

	prInfo, err := r.GH.PRView(ctx, in.PR, in.RepoRoot)
	if err != nil {
		return Result{}, fmt.Errorf("step10: %w", err)
	}
	if prInfo.Number != in.PR {
		return Result{}, fmt.Errorf("step10: gh pr view returned unexpected pr number: got=%d want=%d", prInfo.Number, in.PR)
	}
	if err := validation.Instance().Var(prInfo.BaseRefOid, "required,sha1_hex"); err != nil {
		return Result{}, fmt.Errorf("step10: invalid base sha: %w", err)
	}
	baseSHA := prInfo.BaseRefOid

	worktrees := make([]contracts.WorktreeAllocation, 0, len(in.Agents)*2)
	worktreesCreated := 0
	for pass := 1; pass <= 2; pass++ {
		for _, agent := range in.Agents {
			path := filepath.Join(in.RunCtx.WorktreeBase, fmt.Sprintf("%s-pass%d-%s", runID, pass, agent))
			branch := fmt.Sprintf("auto-improve/%s/pass%d/%s", runID, pass, agent)
			created, err := r.Git.WorktreeAdd(ctx, in.RepoRoot, path, branch, baseSHA)
			if err != nil {
				return Result{}, fmt.Errorf("step10: %w", err)
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

	prompt := ReconstructTaskPrompt(
		fmt.Sprintf("PR #%d: %s", prInfo.Number, prInfo.Title),
		prInfo.Body,
		prInfo.LinkedIssues,
	)

	pkg := contracts.TaskPackage{
		SchemaVersion:           "1",
		RunID:                   runID,
		PR:                      in.PR,
		Title:                   prInfo.Title,
		BaseSHA:                 baseSHA,
		BestBranch:              in.BestBranch,
		ReconstructedTaskPrompt: prompt,
		Worktrees:               worktrees,
		CreatedAt:               in.Now().UTC(),
	}
	if err := pkg.Validate(); err != nil {
		return Result{}, fmt.Errorf("step10: task package validate: %w", err)
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
		WorktreesCreated: worktreesCreated,
	}
	if err := resp.Validate(); err != nil {
		return Result{}, fmt.Errorf("step10: response validate: %w", err)
	}
	return Result{Response: resp}, nil
}
