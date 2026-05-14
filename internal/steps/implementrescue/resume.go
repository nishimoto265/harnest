package implementrescue

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nishimoto265/harnest/internal/config"
	"github.com/nishimoto265/harnest/internal/contracts"
)

type ResumeOptions struct {
	StepName                string
	Agent                   contracts.AgentID
	AgentDir                string
	Allocation              contracts.WorktreeAllocation
	RunConfig               *config.Config
	DefaultConfig           *config.Config
	DefaultMaxRetries       int
	StaleAfter              time.Duration
	Now                     func() time.Time
	LoadState               func(string) (State, bool, error)
	HeartbeatStale          func(string, time.Duration, time.Time) (bool, time.Time, error)
	ShouldAttemptRescue     func(bool, int, int, string) bool
	EnsureWorktreeForRescue func(context.Context, *config.Config, contracts.WorktreeAllocation) error
	PerformRescue           func(context.Context, contracts.WorktreeAllocation, string, State) (int, error)
	LeaseActiveError        error
	NewRescueExhaustedError func(contracts.AgentID, int) error
}

func ResumeIfNeeded(ctx context.Context, opts ResumeOptions) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := validateResumeOptions(opts); err != nil {
		return 0, err
	}
	state, ok, err := opts.LoadState(opts.AgentDir)
	if err != nil || !ok {
		return 0, err
	}
	if state.ExpectedBaseSHA != opts.Allocation.BaseSHA {
		return 0, fmt.Errorf("%s: resume state base mismatch: expected=%s got=%s", opts.StepName, state.ExpectedBaseSHA, opts.Allocation.BaseSHA)
	}
	maxRetries := MaxRetries(opts.RunConfig, opts.DefaultConfig, opts.DefaultMaxRetries)
	if state.Pid == 0 {
		if state.RetryCount >= maxRetries {
			return 0, opts.NewRescueExhaustedError(opts.Agent, state.RetryCount)
		}
		return state.RetryCount, nil
	}

	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	stale, _, err := opts.HeartbeatStale(opts.AgentDir, opts.StaleAfter, now().UTC())
	if err != nil {
		return 0, err
	}
	if !opts.ShouldAttemptRescue(stale, state.Pid, state.Pgid, state.LeaderStartTime) {
		return 0, fmt.Errorf("%w: agent %s", opts.LeaseActiveError, opts.Agent)
	}
	if state.RetryCount >= maxRetries {
		return 0, opts.NewRescueExhaustedError(opts.Agent, state.RetryCount)
	}

	if err := opts.EnsureWorktreeForRescue(ctx, opts.RunConfig, opts.Allocation); err != nil {
		return 0, err
	}
	nextRetry, err := opts.PerformRescue(ctx, opts.Allocation, opts.AgentDir, state)
	if err != nil {
		return 0, err
	}
	if nextRetry >= maxRetries {
		return 0, opts.NewRescueExhaustedError(opts.Agent, nextRetry)
	}
	return nextRetry, nil
}

func validateResumeOptions(opts ResumeOptions) error {
	if strings.TrimSpace(opts.StepName) == "" {
		return errors.New("implementrescue: resume missing StepName")
	}
	if strings.TrimSpace(opts.AgentDir) == "" {
		return errors.New("implementrescue: resume missing AgentDir")
	}
	if opts.LoadState == nil {
		return errors.New("implementrescue: resume missing LoadState")
	}
	if opts.HeartbeatStale == nil {
		return errors.New("implementrescue: resume missing HeartbeatStale")
	}
	if opts.ShouldAttemptRescue == nil {
		return errors.New("implementrescue: resume missing ShouldAttemptRescue")
	}
	if opts.EnsureWorktreeForRescue == nil {
		return errors.New("implementrescue: resume missing EnsureWorktreeForRescue")
	}
	if opts.PerformRescue == nil {
		return errors.New("implementrescue: resume missing PerformRescue")
	}
	if opts.LeaseActiveError == nil {
		return errors.New("implementrescue: resume missing LeaseActiveError")
	}
	if opts.NewRescueExhaustedError == nil {
		return errors.New("implementrescue: resume missing NewRescueExhaustedError")
	}
	return nil
}

func MaxRetries(runCfg, defaultCfg *config.Config, fallback int) int {
	switch {
	case runCfg != nil && runCfg.RescueMaxRetries > 0:
		return runCfg.RescueMaxRetries
	case defaultCfg != nil && defaultCfg.RescueMaxRetries > 0:
		return defaultCfg.RescueMaxRetries
	default:
		return fallback
	}
}
