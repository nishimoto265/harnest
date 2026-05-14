package step20_implement

import (
	"context"
	"time"

	"github.com/nishimoto265/harnest/internal/agents"
	"github.com/nishimoto265/harnest/internal/steps/agentrunner"
)

var (
	startDescendantTracker = agentrunner.StartDescendantTracker
	cleanupProcessTree     = agentrunner.CleanupProcessTree
)

type runner interface {
	Run(ctx context.Context, req runnerRequest) (runnerResult, error)
}

type runnerRequest struct {
	Binary      string
	Args        []string
	Workdir     string
	Prompt      string
	SessionPath string
	Timeout     time.Duration
	Provider    agents.Provider
	Env         []string
	OnStart     func(agentrunner.ProcessLease, time.Time) error
}

type runnerResult struct {
	ExitCode      int
	TimedOut      bool
	StdoutSnippet []byte
	StderrSnippet []byte
	StartedAt     time.Time
	FinishedAt    time.Time
	Lease         agentrunner.ProcessLease
	CleanupErr    error
}

type commandRunner struct {
	now func() time.Time
}

func (r commandRunner) Run(ctx context.Context, req runnerRequest) (runnerResult, error) {
	res, err := agentrunner.RunCommand(ctx, agentrunner.CommandRequest{
		Binary:                 req.Binary,
		Args:                   req.Args,
		Workdir:                req.Workdir,
		Prompt:                 req.Prompt,
		SessionPath:            req.SessionPath,
		Timeout:                req.Timeout,
		Provider:               req.Provider,
		Env:                    req.Env,
		OnStart:                req.OnStart,
		ErrPrefix:              "step20",
		Now:                    r.now,
		StartDescendantTracker: startDescendantTracker,
		CleanupProcessTree:     cleanupProcessTree,
	})
	if err != nil {
		return runnerResult{}, err
	}
	return runnerResult{
		ExitCode:      res.ExitCode,
		TimedOut:      res.TimedOut,
		StdoutSnippet: res.StdoutSnippet,
		StderrSnippet: res.StderrSnippet,
		StartedAt:     res.StartedAt,
		FinishedAt:    res.FinishedAt,
		Lease:         res.Lease,
		CleanupErr:    res.CleanupErr,
	}, nil
}
