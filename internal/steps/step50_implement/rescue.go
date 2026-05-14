package step50_implement

import (
	"bytes"
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

	"github.com/nishimoto265/harnest/internal/contracts"
	"github.com/nishimoto265/harnest/internal/contracts/stepio"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/nishimoto265/harnest/internal/processenv"
	"github.com/nishimoto265/harnest/internal/steps/agentrunner"
	"github.com/nishimoto265/harnest/internal/steps/implementrescue"
	"golang.org/x/sys/unix"
)

type RescueExhaustedError struct {
	Rescue stepio.RescueExhausted
}

func (e *RescueExhaustedError) Error() string {
	return fmt.Sprintf("step50: rescue exhausted: agent=%s retry_count=%d", e.Rescue.Agent, e.Rescue.RetryCount)
}

func (e *RescueExhaustedError) Result() stepio.RescueExhausted {
	return e.Rescue
}

func loadCommonResumeState(agentDir string) (implementrescue.State, bool, error) {
	state, ok, err := loadResumeState(agentDir)
	return implementrescue.State(state), ok, err
}

var rescueWorktreeProcessIDs = worktreeProcessIDs
var rescueKillPID = syscall.Kill
var rescueQuiesceMaxWait = 750 * time.Millisecond
var rescueQuiesceInterval = 25 * time.Millisecond
var rescueSleep = time.Sleep
var rescueExecLookPath = processenv.TrustedLookPath
var rescueCommandContext = processenv.TrustedCommandContext
var trustedGitCommandContext = processenv.TrustedCommandContext
var streamGitOutputWithLimit = agentrunner.StreamGitOutputWithLimit

func (s *Step) resumeIfNeeded(ctx context.Context, run RunContext, allocation contracts.WorktreeAllocation, agentDir string) (int, error) {
	return implementrescue.ResumeIfNeeded(ctx, implementrescue.ResumeOptions{
		StepName:                "step50",
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
		StepName:       "step50",
		RunID:          string(run.IO.RunID),
		Agent:          run.Agent,
		RunIO:          run.IO,
		Allocation:     allocation,
		AgentDir:       agentDir,
		RescuedDirName: rescuedDirName,
		State:          implementrescue.State(state),
		Now:            s.now,
		Quiesce: func(ctx context.Context, worktreePath string, state implementrescue.State) error {
			return ensureRescueLeaseQuiesced(ctx, worktreePath, resumeState(state))
		},
		GitOutput:      gitOutputContext,
		WriteGitOutput: writeGitOutputContext,
		WriteBundle:    writeCommitBundle,
		CopyUntracked:  copyUntrackedFilesWithBudget,
		CopyIgnored:    copyIgnoredFilesWithBudget,
		WriteIgnored:   writeIgnoredList,
		FileDigest:     fileDigest,
		ComputeDirty:   agentrunner.ComputeDirtyState,
		VerifyState:    verifyRescueState,
		FinishState: func(agentDir string, state implementrescue.State, nextRetry int) (int, error) {
			return implementrescue.FinishState(agentDir, state, nextRetry, heartbeatPath, func(agentDir string, state implementrescue.State) error {
				return saveResumeState(agentDir, resumeState(state))
			})
		},
	})
}

type rescueLock = implementrescue.Lock

func tryAcquireRescueLock(path string) (*rescueLock, bool, error) {
	return implementrescue.TryAcquireLock(path, ensureDir)
}

type rescueArtifactDigest = agentrunner.RescueArtifactDigest

func verifyRescueState(rescueDir string, state agentrunner.RescueStateFile) error {
	return agentrunner.VerifyRescueStateFile(rescueDir, state, fileDigest, "step50")
}

func writeCommitBundle(ctx context.Context, repoPath, rescueDir, expectedBaseSHA string) (int, string, error) {
	return implementrescue.WriteCommitBundle(ctx, repoPath, rescueDir, expectedBaseSHA, gitOutputBytesContext, runGitCommand)
}

func runGitCommand(ctx context.Context, dir string, args ...string) error {
	cmd, err := trustedGitCommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	if err != nil {
		return fmt.Errorf("step50: resolve git: %w", err)
	}
	cmd.Env = processenv.GitLocalEnv()
	output, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("step50: git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func gitOutputBytesContext(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return gitOutputBytesContextWithEnv(ctx, dir, processenv.GitLocalEnv(), args...)
}

func gitOutputBytesContextWithEnv(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	cmd, err := trustedGitCommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	if err != nil {
		return nil, fmt.Errorf("step50: resolve git: %w", err)
	}
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("step50: git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return output, nil
}

func gitOutputContext(ctx context.Context, mapFn func(string) string, dir string, args ...string) (string, error) {
	output, err := gitOutputBytesContext(ctx, dir, args...)
	if err != nil {
		return "", err
	}
	return mapFn(string(output)), nil
}

func gitOutputContextWithEnv(ctx context.Context, mapFn func(string) string, dir string, env []string, args ...string) (string, error) {
	output, err := gitOutputBytesContextWithEnv(ctx, dir, env, args...)
	if err != nil {
		return "", err
	}
	return mapFn(string(output)), nil
}

func syntheticCommitEnv() []string {
	return append(processenv.GitLocalEnv(),
		"GIT_AUTHOR_NAME=auto-improve",
		"GIT_AUTHOR_EMAIL=auto-improve@example.invalid",
		"GIT_AUTHOR_DATE=2000-01-01T00:00:00Z",
		"GIT_COMMITTER_NAME=auto-improve",
		"GIT_COMMITTER_EMAIL=auto-improve@example.invalid",
		"GIT_COMMITTER_DATE=2000-01-01T00:00:00Z",
	)
}

func writeGitOutputContext(ctx context.Context, dir, dest string, args ...string) error {
	_, err := streamGitOutputWithLimit(ctx, dir, "step50", dest, agentrunner.RescueDiffLimitBytes, args...)
	return err
}

func copyUntrackedFilesWithBudget(ctx context.Context, repoPath, rescueDir string, budget *agentrunner.RescueArtifactBudget) ([]rescueArtifactDigest, error) {
	return implementrescue.CopyUntrackedFilesWithBudget(ctx, "step50", repoPath, rescueDir, budget, gitOutputBytesContext, ensureDir, copyOpenFileContext, fileDigest)
}

func copyIgnoredFilesWithBudget(ctx context.Context, repoPath, rescueDir string, budget *agentrunner.RescueArtifactBudget) ([]rescueArtifactDigest, error) {
	return implementrescue.CopyIgnoredFilesWithBudget(ctx, "step50", repoPath, rescueDir, budget, gitOutputBytesContext, ensureDir, copyOpenFileContext, fileDigest)
}

func writeIgnoredList(ctx context.Context, repoPath, dest string) error {
	return implementrescue.WriteIgnoredList(ctx, repoPath, dest, gitOutputBytesContext)
}

func fileDigest(path string) (string, error) {
	f, _, _, err := agentrunner.OpenValidatedRegularFile(path)
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

func identity(s string) string {
	return s
}

func copyOpenFileContext(ctx context.Context, in *os.File, dst string, perm os.FileMode, sizeLimit int64) error {
	defer in.Close()
	if err := internalio.EnsureNoSymlinkPathComponents(filepath.Dir(dst)); err != nil {
		return err
	}
	fd, err := unix.Open(dst, unix.O_CREAT|unix.O_WRONLY|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, uint32(perm))
	if err != nil {
		return err
	}
	out := os.NewFile(uintptr(fd), dst)
	if out == nil {
		_ = unix.Close(fd)
		return fmt.Errorf("step50: rescue output open returned nil: %s", dst)
	}
	if err := out.Chmod(0o600); err != nil {
		_ = out.Close()
		return err
	}
	written, err := io.CopyBuffer(out, io.LimitReader(&copyContextReader{ctx: ctx, reader: in}, sizeLimit+1), make([]byte, 32<<10))
	if err != nil {
		_ = out.Close()
		return err
	}
	if written > sizeLimit {
		_ = out.Close()
		return fmt.Errorf("step50: rescue untracked file exceeds size limit: path=%s size=%d limit=%d", dst, written, sizeLimit)
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

func syncDir(path string) error {
	dir, err := os.Open(filepath.Clean(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
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
			Detail: "step50: rescue lease quiesce timed out while worktree remained busy",
			Err:    err,
		}
	case errors.Is(err, agentrunner.ErrRescueLeaseQuiesceEnumerate):
		return &agentrunner.ManualRecoveryRequiredError{
			Reason: contracts.RollbackReasonLeaseFailure,
			Detail: fmt.Sprintf("step50: rescue lease quiesce failed to enumerate worktree processes: %v", err),
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
		return nil, fmt.Errorf("step50: %w", err)
	}
	return pids, nil
}
