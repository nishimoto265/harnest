package step20_implement

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/contracts/stepio"
	"github.com/nishimoto265/auto-improve/internal/processenv"
	"github.com/nishimoto265/auto-improve/internal/steps/agentrunner"
	"github.com/nishimoto265/auto-improve/internal/steps/implementrescue"
)

type RescueExhaustedError struct {
	Rescue stepio.RescueExhausted
}

func (e *RescueExhaustedError) Error() string {
	return fmt.Sprintf("step20: rescue exhausted: agent=%s retry_count=%d", e.Rescue.Agent, e.Rescue.RetryCount)
}

func (e *RescueExhaustedError) Result() stepio.RescueExhausted {
	return e.Rescue
}

func loadCommonResumeState(agentDir string) (implementrescue.State, bool, error) {
	state, ok, err := loadResumeState(agentDir)
	return implementrescue.State(state), ok, err
}

func (s *Step) resumeIfNeeded(ctx context.Context, run RunContext, allocation contracts.WorktreeAllocation, agentDir string) (int, error) {
	return implementrescue.ResumeIfNeeded(ctx, implementrescue.ResumeOptions{
		StepName:                "step20",
		Agent:                   run.Agent,
		AgentDir:                agentDir,
		Allocation:              allocation,
		RunConfig:               run.Config,
		DefaultConfig:           s.cfg,
		DefaultMaxRetries:       defaultRescueMaxRetries,
		StaleAfter:              s.staleAfter,
		Now:                     s.now,
		LoadState:               loadCommonResumeState,
		HeartbeatStale:          heartbeatStale,
		ShouldAttemptRescue:     shouldAttemptRescue,
		EnsureWorktreeForRescue: ensureAllocationWorktreeForRescue,
		PerformRescue: func(ctx context.Context, allocation contracts.WorktreeAllocation, agentDir string, state implementrescue.State) (int, error) {
			return s.performRescue(ctx, run, allocation, agentDir, resumeState(state))
		},
		LeaseActiveError: ErrRescueAbortedLeaseActive,
		NewRescueExhaustedError: func(agent contracts.AgentID, retryCount int) error {
			return &RescueExhaustedError{Rescue: implementrescue.ToExhaustedResult(agent, retryCount)}
		},
	})
}

func (s *Step) performRescue(ctx context.Context, run RunContext, allocation contracts.WorktreeAllocation, agentDir string, state resumeState) (int, error) {
	return implementrescue.Perform(ctx, implementrescue.PerformOptions{
		StepName:       "step20",
		RunID:          string(run.IO.RunID),
		Agent:          run.Agent,
		RunIO:          run.IO,
		Allocation:     allocation,
		AgentDir:       agentDir,
		RescuedDirName: rescuedDirName,
		State:          implementrescue.State(state),
		Now:            s.now,
		EnsureDir:      ensureDir,
		Quiesce: func(ctx context.Context, worktreePath string, state implementrescue.State) error {
			return ensureRescueLeaseQuiesced(ctx, worktreePath, resumeState(state))
		},
		GitOutput:      gitOutputContext,
		WriteGitOutput: writeGitOutputContext,
		WriteBundle:    writeCommitBundle,
		CopyUntracked:  copyUntrackedFilesWithBudget,
		WriteIgnored:   writeIgnoredList,
		FileDigest:     fileDigest,
		VerifyState:    verifyRescueState,
		FinishState: func(agentDir string, state implementrescue.State, nextRetry int) (int, error) {
			return finishRescueState(agentDir, resumeState(state), nextRetry)
		},
	})
}

type rescueLock = implementrescue.Lock

func tryAcquireRescueLock(path string) (*rescueLock, bool, error) {
	return implementrescue.TryAcquireLock(path, ensureDir)
}

type rescueArtifactDigest = agentrunner.RescueArtifactDigest

var rescueWorktreeProcessIDs = worktreeProcessIDs
var rescueKillPID = syscall.Kill
var rescueQuiesceMaxWait = 750 * time.Millisecond
var rescueQuiesceInterval = 25 * time.Millisecond
var rescueSleep = time.Sleep
var rescueExecLookPath = processenv.TrustedLookPath
var rescueCommandContext = processenv.TrustedCommandContext
var trustedGitCommandContext = processenv.TrustedCommandContext
var streamGitOutputWithLimit = agentrunner.StreamGitOutputWithLimit

func writeCommitBundle(ctx context.Context, worktreePath, rescueDir, baseSHA string) (int, string, error) {
	return implementrescue.WriteCommitBundle(ctx, worktreePath, rescueDir, baseSHA, gitOutputBytesContext, runGitCommand)
}

func runGitCommand(ctx context.Context, worktreePath string, args ...string) error {
	cmd, err := trustedGitCommandContext(ctx, "git", args...)
	if err != nil {
		return fmt.Errorf("step20: resolve git: %w", err)
	}
	cmd.Dir = worktreePath
	cmd.Env = processenv.GitLocalEnv()
	output, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("step20: git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func writeGitOutputContext(ctx context.Context, worktreePath, target string, args ...string) error {
	_, err := streamGitOutputWithLimit(ctx, worktreePath, "step20", target, agentrunner.RescueDiffLimitBytes, args...)
	return err
}

func copyUntrackedFilesWithBudget(ctx context.Context, worktreePath, rescueDir string, budget *agentrunner.RescueArtifactBudget) ([]rescueArtifactDigest, error) {
	return implementrescue.CopyUntrackedFilesWithBudget(ctx, "step20", worktreePath, rescueDir, budget, gitOutputBytesContext, ensureDir, copyOpenFileContext, fileDigest)
}

func writeIgnoredList(ctx context.Context, worktreePath, target string) error {
	return implementrescue.WriteIgnoredList(ctx, worktreePath, target, gitOutputBytesContext)
}

func verifyRescueState(rescueDir string) error {
	return agentrunner.VerifyRescueState(rescueDir, fileDigest, "step20")
}

func copyOpenFileContext(ctx context.Context, in *os.File, dst string, perm os.FileMode, sizeLimit int64) error {
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	written, err := io.CopyBuffer(out, io.LimitReader(&copyContextReader{ctx: ctx, reader: in}, sizeLimit+1), make([]byte, 32<<10))
	if err != nil {
		_ = out.Close()
		return err
	}
	if written > sizeLimit {
		_ = out.Close()
		return fmt.Errorf("step20: rescue untracked file exceeds size limit: path=%s size=%d limit=%d", dst, written, sizeLimit)
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return syncDir(filepath.Dir(dst))
}

type copyContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *copyContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.reader.Read(p)
	if err == nil {
		if ctxErr := r.ctx.Err(); ctxErr != nil {
			return n, ctxErr
		}
	}
	return n, err
}

func fileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func gitOutputContext(ctx context.Context, transform func(string) string, worktreePath string, args ...string) (string, error) {
	out, err := gitOutputBytesContext(ctx, worktreePath, args...)
	if err != nil {
		return "", err
	}
	return transform(string(out)), nil
}

func gitOutputContextWithEnv(ctx context.Context, transform func(string) string, worktreePath string, env []string, args ...string) (string, error) {
	out, err := gitOutputBytesContextWithEnv(ctx, worktreePath, env, args...)
	if err != nil {
		return "", err
	}
	return transform(string(out)), nil
}

func gitOutputBytesContext(ctx context.Context, worktreePath string, args ...string) ([]byte, error) {
	return gitOutputBytesContextWithEnv(ctx, worktreePath, processenv.GitLocalEnv(), args...)
}

func gitOutputBytesContextWithEnv(ctx context.Context, worktreePath string, env []string, args ...string) ([]byte, error) {
	cmd, err := trustedGitCommandContext(ctx, "git", args...)
	if err != nil {
		return nil, fmt.Errorf("step20: resolve git: %w", err)
	}
	cmd.Dir = worktreePath
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("step20: git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func syntheticCommitEnv() []string {
	return append(processenv.GitLocalEnv(),
		"GIT_AUTHOR_NAME=auto-improve",
		"GIT_AUTHOR_EMAIL=auto-improve@example.invalid",
		"GIT_COMMITTER_NAME=auto-improve",
		"GIT_COMMITTER_EMAIL=auto-improve@example.invalid",
	)
}

func identity(s string) string {
	return s
}

func restoreAllocationWorktree(ctx context.Context, allocation contracts.WorktreeAllocation, expectedBaseSHA string) error {
	targetRef := expectedBaseSHA
	currentHead, err := gitOutputContext(ctx, strings.TrimSpace, allocation.Path, "rev-parse", "HEAD")
	if err == nil && currentHead == expectedBaseSHA {
		targetRef = "HEAD"
	}
	if _, err := gitOutputContext(ctx, identity, allocation.Path, "checkout", "--force", "-B", allocation.Branch, targetRef); err != nil {
		return err
	}
	if _, err := gitOutputContext(ctx, identity, allocation.Path, "reset", "--hard", targetRef); err != nil {
		return err
	}
	if _, err := gitOutputContext(ctx, identity, allocation.Path, "clean", "-fdx"); err != nil {
		return err
	}
	return nil
}

func finishRescueState(agentDir string, state resumeState, nextRetry int) (int, error) {
	return implementrescue.FinishState(agentDir, implementrescue.State(state), nextRetry, heartbeatPath, func(agentDir string, state implementrescue.State) error {
		return saveResumeState(agentDir, resumeState(state))
	})
}

func ensureRescueLeaseQuiesced(ctx context.Context, worktreePath string, state resumeState) error {
	err := agentrunner.EnsureRescueLeaseQuiesced(ctx, worktreePath, agentrunner.RescueLeaseState{
		PID:             state.Pid,
		PGID:            state.Pgid,
		LeaderStartTime: state.LeaderStartTime,
	}, agentrunner.RescueLeaseQuiesceOptions{
		WorktreeProcessIDs:     rescueWorktreeProcessIDs,
		KillPID:                rescueKillPID,
		Sleep:                  rescueSleep,
		Now:                    time.Now,
		PIDAlive:               pidAlive,
		LookupProcessStartTime: lookupLeaseStartTime,
		MaxWait:                rescueQuiesceMaxWait,
		Interval:               rescueQuiesceInterval,
	})
	switch {
	case err == nil:
		return nil
	case errors.Is(err, agentrunner.ErrRescueLeaseQuiesceTimedOut):
		return &agentrunner.ManualRecoveryRequiredError{
			Reason: contracts.RollbackReasonLeaseFailure,
			Detail: "step20: rescue lease quiesce timed out while worktree remained busy",
			Err:    err,
		}
	case errors.Is(err, agentrunner.ErrRescueLeaseQuiesceEnumerate):
		return &agentrunner.ManualRecoveryRequiredError{
			Reason: contracts.RollbackReasonLeaseFailure,
			Detail: fmt.Sprintf("step20: rescue lease quiesce failed to enumerate worktree processes: %v", err),
			Err:    err,
		}
	default:
		return err
	}
}

func worktreeProcessIDs(ctx context.Context, worktreePath string) ([]int, error) {
	pids, err := agentrunner.WorktreeProcessIDs(ctx, worktreePath, agentrunner.WorktreeProcessIDsOptions{
		LookPath:       rescueExecLookPath,
		CommandContext: rescueCommandContext,
	})
	if err != nil {
		return nil, fmt.Errorf("step20: %w", err)
	}
	return pids, nil
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
