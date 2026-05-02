package implementrescue

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/steps/agentrunner"
)

type PerformOptions struct {
	StepName       string
	RunID          string
	Agent          contracts.AgentID
	RunIO          internalio.RunContext
	Allocation     contracts.WorktreeAllocation
	AgentDir       string
	RescuedDirName string
	State          State
	Now            func() time.Time
	EnsureDir      func(string) error
	Quiesce        func(context.Context, string, State) error
	GitOutput      func(context.Context, func(string) string, string, ...string) (string, error)
	WriteGitOutput func(context.Context, string, string, ...string) error
	WriteBundle    func(context.Context, string, string, string) (int, string, error)
	CopyUntracked  func(context.Context, string, string, *agentrunner.RescueArtifactBudget) ([]agentrunner.RescueArtifactDigest, error)
	CopyIgnored    func(context.Context, string, string, *agentrunner.RescueArtifactBudget) ([]agentrunner.RescueArtifactDigest, error)
	WriteIgnored   func(context.Context, string, string) error
	FileDigest     func(string) (string, error)
	ComputeDirty   func(context.Context, string) (string, []string, error)
	VerifyState    func(string) error
	FinishState    func(string, State, int) (int, error)
}

func Perform(ctx context.Context, opts PerformOptions) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := validatePerformOptions(opts); err != nil {
		return 0, err
	}
	if err := opts.RunIO.ValidateWorktreeAllocation(opts.Allocation); err != nil {
		return 0, err
	}
	if err := opts.Quiesce(ctx, opts.Allocation.Path, opts.State); err != nil {
		return 0, err
	}
	currentBranch, err := opts.GitOutput(ctx, strings.TrimSpace, opts.Allocation.Path, "branch", "--show-current")
	if err != nil {
		return 0, err
	}
	if currentBranch == "" || currentBranch != opts.Allocation.Branch {
		return 0, &agentrunner.ManualRecoveryRequiredError{
			Reason: contracts.RollbackReasonLeaseFailure,
			Detail: fmt.Sprintf("%s: rescue aborted because worktree branch drifted: got=%q want=%q", opts.StepName, currentBranch, opts.Allocation.Branch),
		}
	}
	nextRetry := opts.State.RetryCount + 1
	currentHead, err := opts.GitOutput(ctx, strings.TrimSpace, opts.Allocation.Path, "rev-parse", "HEAD")
	if err != nil {
		return 0, err
	}
	currentDirtyFingerprint, currentDirtyEntries, err := opts.ComputeDirty(ctx, opts.Allocation.Path)
	if err != nil {
		return 0, err
	}
	rescueDir, adopted, err := FindExistingDir(opts.AgentDir, opts.RescuedDirName, opts.State.ExpectedBaseSHA, nextRetry, currentHead, currentDirtyFingerprint, currentDirtyEntries, opts.VerifyState)
	if err != nil {
		return 0, err
	}
	if !adopted {
		now := time.Now
		if opts.Now != nil {
			now = opts.Now
		}
		rescueID := fmt.Sprintf("%s-%s-rescue-%d-%d", opts.RunID, opts.Agent, nextRetry, now().UTC().Unix())
		rescueDir = filepath.Join(opts.AgentDir, opts.RescuedDirName, rescueID)
		if err := opts.EnsureDir(filepath.Join(rescueDir, "untracked")); err != nil {
			return 0, err
		}
		rescueStateVerified := false
		defer func() {
			if !rescueStateVerified {
				_ = os.RemoveAll(rescueDir)
			}
		}()
		if err := CaptureArtifacts(ctx, opts, rescueDir, currentHead, currentDirtyFingerprint, nextRetry); err != nil {
			return 0, err
		}
		rescueStateVerified = true
	}

	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if _, err := opts.GitOutput(ctx, identity, opts.Allocation.Path, "reset", "--hard", opts.State.ExpectedBaseSHA); err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if _, err := opts.GitOutput(ctx, identity, opts.Allocation.Path, "clean", "-fdx"); err != nil {
		return 0, err
	}

	return opts.FinishState(opts.AgentDir, opts.State, nextRetry)
}

func validatePerformOptions(opts PerformOptions) error {
	if strings.TrimSpace(opts.StepName) == "" {
		return errors.New("implementrescue: perform missing StepName")
	}
	if strings.TrimSpace(opts.RunID) == "" {
		return errors.New("implementrescue: perform missing RunID")
	}
	if strings.TrimSpace(opts.AgentDir) == "" {
		return errors.New("implementrescue: perform missing AgentDir")
	}
	if strings.TrimSpace(opts.RescuedDirName) == "" {
		return errors.New("implementrescue: perform missing RescuedDirName")
	}
	if opts.EnsureDir == nil {
		return errors.New("implementrescue: perform missing EnsureDir")
	}
	if opts.Quiesce == nil {
		return errors.New("implementrescue: perform missing Quiesce")
	}
	if opts.GitOutput == nil {
		return errors.New("implementrescue: perform missing GitOutput")
	}
	if opts.WriteGitOutput == nil {
		return errors.New("implementrescue: perform missing WriteGitOutput")
	}
	if opts.WriteBundle == nil {
		return errors.New("implementrescue: perform missing WriteBundle")
	}
	if opts.CopyUntracked == nil {
		return errors.New("implementrescue: perform missing CopyUntracked")
	}
	if opts.CopyIgnored == nil {
		return errors.New("implementrescue: perform missing CopyIgnored")
	}
	if opts.WriteIgnored == nil {
		return errors.New("implementrescue: perform missing WriteIgnored")
	}
	if opts.FileDigest == nil {
		return errors.New("implementrescue: perform missing FileDigest")
	}
	if opts.ComputeDirty == nil {
		return errors.New("implementrescue: perform missing ComputeDirty")
	}
	if opts.VerifyState == nil {
		return errors.New("implementrescue: perform missing VerifyState")
	}
	if opts.FinishState == nil {
		return errors.New("implementrescue: perform missing FinishState")
	}
	return nil
}

func identity(s string) string {
	return s
}
