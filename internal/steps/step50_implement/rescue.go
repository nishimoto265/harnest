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
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/contracts/stepio"
	"github.com/nishimoto265/auto-improve/internal/processenv"
	"github.com/nishimoto265/auto-improve/internal/steps/agentrunner"
)

type RescueExhaustedError struct {
	Rescue stepio.RescueExhausted
}

const maxRescueUntrackedBytes = 32 << 20

func (e *RescueExhaustedError) Error() string {
	return fmt.Sprintf("step50: rescue exhausted: agent=%s retry_count=%d", e.Rescue.Agent, e.Rescue.RetryCount)
}

func (e *RescueExhaustedError) Result() stepio.RescueExhausted {
	return e.Rescue
}

var rescueKillProcessGroupUntilGone = agentrunner.KillProcessGroupUntilGone
var rescueWorktreeProcessIDs = worktreeProcessIDs
var rescueKillPID = syscall.Kill
var rescueQuiesceMaxWait = 750 * time.Millisecond
var rescueQuiesceInterval = 25 * time.Millisecond
var rescueSleep = time.Sleep
var rescueExecLookPath = exec.LookPath
var rescueCommandContext = exec.CommandContext

func (s *Step) resumeIfNeeded(ctx context.Context, run RunContext, allocation contracts.WorktreeAllocation, agentDir string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	state, ok, err := loadResumeState(agentDir)
	if err != nil || !ok {
		return 0, err
	}
	if state.ExpectedBaseSHA != allocation.BaseSHA {
		return 0, fmt.Errorf("step50: resume state base mismatch: expected=%s got=%s", state.ExpectedBaseSHA, allocation.BaseSHA)
	}
	if state.Pid == 0 {
		if state.RetryCount >= rescueMaxRetries(run.Config, s.cfg) {
			return 0, &RescueExhaustedError{
				Rescue: stepio.RescueExhausted{
					Agent:      run.Agent,
					RetryCount: state.RetryCount,
				},
			}
		}
		return state.RetryCount, nil
	}

	stale, _, err := heartbeatStale(agentDir, s.staleAfter, s.now().UTC())
	if err != nil {
		return 0, err
	}
	if !shouldAttemptRescue(stale, state.Pid, state.Pgid, state.LeaderStartTime) {
		return 0, fmt.Errorf("%w: agent %s", ErrRescueAbortedLeaseActive, run.Agent)
	}
	if state.RetryCount >= rescueMaxRetries(run.Config, s.cfg) {
		return 0, &RescueExhaustedError{
			Rescue: stepio.RescueExhausted{
				Agent:      run.Agent,
				RetryCount: state.RetryCount,
			},
		}
	}

	nextRetry, err := s.performRescue(ctx, run, allocation, agentDir, state)
	if err != nil {
		return 0, err
	}
	if nextRetry >= rescueMaxRetries(run.Config, s.cfg) {
		return 0, &RescueExhaustedError{
			Rescue: stepio.RescueExhausted{
				Agent:      run.Agent,
				RetryCount: nextRetry,
			},
		}
	}
	return nextRetry, nil
}

func rescueMaxRetries(runCfg, defaultCfg *config.Config) int {
	switch {
	case runCfg != nil && runCfg.RescueMaxRetries > 0:
		return runCfg.RescueMaxRetries
	case defaultCfg != nil && defaultCfg.RescueMaxRetries > 0:
		return defaultCfg.RescueMaxRetries
	default:
		return defaultRescueMaxRetries
	}
}

func (s *Step) performRescue(ctx context.Context, run RunContext, allocation contracts.WorktreeAllocation, agentDir string, state resumeState) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := run.IO.ValidateWorktreeAllocation(allocation); err != nil {
		return 0, err
	}
	if err := ensureRescueLeaseQuiesced(ctx, allocation.Path, state); err != nil {
		return 0, err
	}
	currentBranch, err := gitOutputContext(ctx, stringsTrimSpace, allocation.Path, "branch", "--show-current")
	if err != nil {
		return 0, err
	}
	if currentBranch == "" || currentBranch != allocation.Branch {
		return 0, &agentrunner.ManualRecoveryRequiredError{
			Reason: contracts.RollbackReasonLeaseFailure,
			Detail: fmt.Sprintf("step50: rescue aborted because worktree branch drifted: got=%q want=%q", currentBranch, allocation.Branch),
		}
	}
	rescueID := fmt.Sprintf("%s-%s-rescue-%d-%d", filepath.Base(run.IO.RunDir()), run.Agent, state.RetryCount+1, s.now().UTC().Unix())
	rescueDir := filepath.Join(agentDir, rescuedDirName, rescueID)
	if err := ensureDir(filepath.Join(rescueDir, "untracked")); err != nil {
		return 0, err
	}
	rescueStateVerified := false
	defer func() {
		if !rescueStateVerified {
			_ = os.RemoveAll(rescueDir)
		}
	}()
	budget := agentrunner.NewRescueArtifactBudget()

	headSHA, err := gitOutputContext(ctx, stringsTrimSpace, allocation.Path, "rev-parse", "HEAD")
	if err != nil {
		return 0, err
	}
	artifacts := make([]rescueArtifactDigest, 0, 8)

	commitCount, bundleMode, err := writeCommitBundle(ctx, allocation.Path, rescueDir, state.ExpectedBaseSHA)
	if err != nil {
		return 0, err
	}
	if digest, err := fileDigest(filepath.Join(rescueDir, "commits.bundle")); err == nil {
		artifacts = append(artifacts, rescueArtifactDigest{Path: "commits.bundle", SHA256: digest})
	} else {
		return 0, err
	}
	if err := mapRescueCaptureError("step50", recordRescueArtifact(&budget, filepath.Join(rescueDir, "commits.bundle"), "commits.bundle")); err != nil {
		return 0, err
	}

	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := mapRescueCaptureError("step50", writeGitOutputContext(ctx, allocation.Path, filepath.Join(rescueDir, "tracked.patch"), "diff", "HEAD", "--binary", "--no-ext-diff", "--no-textconv")); err != nil {
		return 0, err
	}
	if digest, err := fileDigest(filepath.Join(rescueDir, "tracked.patch")); err == nil {
		artifacts = append(artifacts, rescueArtifactDigest{Path: "tracked.patch", SHA256: digest})
	} else {
		return 0, err
	}
	if err := mapRescueCaptureError("step50", recordRescueArtifact(&budget, filepath.Join(rescueDir, "tracked.patch"), "tracked.patch")); err != nil {
		return 0, err
	}

	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := mapRescueCaptureError("step50", writeGitOutputContext(ctx, allocation.Path, filepath.Join(rescueDir, "staged.patch"), "diff", "--cached", "--binary", "--no-ext-diff", "--no-textconv")); err != nil {
		return 0, err
	}
	if digest, err := fileDigest(filepath.Join(rescueDir, "staged.patch")); err == nil {
		artifacts = append(artifacts, rescueArtifactDigest{Path: "staged.patch", SHA256: digest})
	} else {
		return 0, err
	}
	if err := mapRescueCaptureError("step50", recordRescueArtifact(&budget, filepath.Join(rescueDir, "staged.patch"), "staged.patch")); err != nil {
		return 0, err
	}

	if err := ctx.Err(); err != nil {
		return 0, err
	}
	untrackedArtifacts, err := copyUntrackedFilesWithBudget(ctx, allocation.Path, rescueDir, &budget)
	if err != nil {
		return 0, mapRescueCaptureError("step50", err)
	}
	artifacts = append(artifacts, untrackedArtifacts...)

	ignoredPath := filepath.Join(rescueDir, "ignored.txt")
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := writeIgnoredList(ctx, allocation.Path, ignoredPath); err != nil {
		return 0, err
	}
	if digest, err := fileDigest(ignoredPath); err == nil {
		artifacts = append(artifacts, rescueArtifactDigest{Path: "ignored.txt", SHA256: digest})
	} else {
		return 0, err
	}
	if err := mapRescueCaptureError("step50", recordRescueArtifact(&budget, ignoredPath, "ignored.txt")); err != nil {
		return 0, err
	}

	nextRetry := state.RetryCount + 1
	rescueState := rescueStateFile{
		ExpectedBaseSHA: state.ExpectedBaseSHA,
		RescuedHeadSHA:  headSHA,
		RetryCount:      nextRetry,
		CommitCount:     commitCount,
		BundleMode:      bundleMode,
		CreatedAt:       s.now().UTC(),
		Artifacts:       artifacts,
	}
	if err := agentrunner.WriteRescueState(filepath.Join(rescueDir, "state.json"), rescueState); err != nil {
		return 0, err
	}
	if err := verifyRescueState(rescueDir); err != nil {
		return 0, err
	}
	rescueStateVerified = true

	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if _, err := gitOutputContext(ctx, identity, allocation.Path, "reset", "--hard", state.ExpectedBaseSHA); err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if _, err := gitOutputContext(ctx, identity, allocation.Path, "clean", "-fd"); err != nil {
		return 0, err
	}

	state.RetryCount = nextRetry
	state.StartedAt = time.Time{}
	state.LastHeartbeat = time.Time{}
	state.Pid = 0
	state.Pgid = 0
	state.LeaderStartTime = ""
	if err := os.Remove(heartbeatPath(agentDir)); err != nil && !os.IsNotExist(err) {
		return 0, err
	}
	if err := saveResumeState(agentDir, state); err != nil {
		return 0, err
	}
	return nextRetry, nil
}

type rescueLock struct {
	file *os.File
}

func tryAcquireRescueLock(path string) (*rescueLock, bool, error) {
	if err := ensureDir(filepath.Dir(path)); err != nil {
		return nil, false, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return &rescueLock{file: f}, true, nil
}

func (l *rescueLock) Unlock() error {
	if l == nil || l.file == nil {
		return nil
	}
	defer func() {
		l.file = nil
	}()
	if err := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN); err != nil {
		_ = l.file.Close()
		return err
	}
	return l.file.Close()
}

type rescueArtifactDigest = agentrunner.RescueArtifactDigest

type rescueStateFile = agentrunner.RescueStateFile

func verifyRescueState(rescueDir string) error {
	return agentrunner.VerifyRescueState(rescueDir, fileDigest, "step50")
}

func writeCommitBundle(ctx context.Context, repoPath, rescueDir, expectedBaseSHA string) (int, string, error) {
	revListOutput, err := gitOutputBytesContext(ctx, repoPath, "rev-list", expectedBaseSHA+"..HEAD")
	if err == nil {
		commits := strings.Fields(string(revListOutput))
		if len(commits) == 0 {
			bundlePath := filepath.Join(rescueDir, "commits.bundle")
			if err := writeAtomicImpl(bundlePath, nil); err != nil {
				return 0, "", err
			}
			return 0, agentrunner.RescueBundleModeNone, nil
		}
		bundlePath := filepath.Join(rescueDir, "commits.bundle")
		if err := runGitCommand(ctx, repoPath, "bundle", "create", bundlePath, expectedBaseSHA+"..HEAD"); err != nil {
			return 0, "", err
		}
		return len(commits), agentrunner.RescueBundleModeRange, nil
	}

	headOutput, err := gitOutputBytesContext(ctx, repoPath, "rev-list", "HEAD")
	if err != nil {
		return 0, "", err
	}
	bundlePath := filepath.Join(rescueDir, "commits.bundle")
	if err := runGitCommand(ctx, repoPath, "bundle", "create", bundlePath, "HEAD"); err != nil {
		return 0, "", err
	}
	return len(strings.Fields(string(headOutput))), agentrunner.RescueBundleModeFullHead, nil
}

func runGitCommand(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = processenv.Sanitize()
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
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = processenv.Sanitize()
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

func writeGitOutputContext(ctx context.Context, dir, dest string, args ...string) error {
	_, err := agentrunner.StreamGitOutputWithLimit(ctx, dir, "step50", dest, agentrunner.RescueDiffLimitBytes, args...)
	return err
}

func copyUntrackedFiles(ctx context.Context, repoPath, rescueDir string) ([]rescueArtifactDigest, error) {
	budget := agentrunner.NewRescueArtifactBudget()
	return copyUntrackedFilesWithBudget(ctx, repoPath, rescueDir, &budget)
}

func copyUntrackedFilesWithBudget(ctx context.Context, repoPath, rescueDir string, budget *agentrunner.RescueArtifactBudget) ([]rescueArtifactDigest, error) {
	output, err := gitOutputBytesContext(ctx, repoPath, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	entries := strings.Split(string(output), "\x00")
	rescueBase := filepath.Join(rescueDir, "untracked")
	skipLog := make([]string, 0)
	artifacts := make([]rescueArtifactDigest, 0, len(entries)+1)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry == "" {
			continue
		}
		cleaned := filepath.Clean(entry)
		src := filepath.Join(repoPath, cleaned)
		dst := filepath.Join(rescueBase, cleaned)
		if !strings.HasPrefix(dst, rescueBase+string(os.PathSeparator)) && dst != rescueBase {
			return nil, fmt.Errorf("step50: untracked file escapes rescue dir: %s", entry)
		}
		info, err := os.Lstat(src)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			skipLog = append(skipLog, "symlink:"+cleaned)
			continue
		}
		file, perm, size, err := agentrunner.OpenValidatedRegularFile(src)
		if err != nil {
			if errors.Is(err, agentrunner.ErrArtifactNotRegular) {
				skipLog = append(skipLog, "skipped_non_regular:"+cleaned)
				continue
			}
			return nil, err
		}
		if size > maxRescueUntrackedBytes {
			_ = file.Close()
			skipLog = append(skipLog, fmt.Sprintf("skipped_too_large:%s:%d", cleaned, size))
			continue
		}
		if err := budget.RecordFile(filepath.ToSlash(filepath.Join("untracked", cleaned)), size); err != nil {
			_ = file.Close()
			return nil, err
		}
		if err := ensureDir(filepath.Dir(dst)); err != nil {
			_ = file.Close()
			return nil, err
		}
		if err := copyOpenFileContext(ctx, file, dst, perm, maxRescueUntrackedBytes); err != nil {
			return nil, err
		}
		digest, err := fileDigest(dst)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, rescueArtifactDigest{
			Path:   filepath.ToSlash(filepath.Join("untracked", cleaned)),
			SHA256: digest,
		})
	}
	symlinkPath := filepath.Join(rescueDir, "untracked-symlinks.txt")
	if err := writeAtomicImpl(symlinkPath, []byte(strings.Join(skipLog, "\n"))); err != nil {
		return nil, err
	}
	if err := recordRescueArtifact(budget, symlinkPath, "untracked-symlinks.txt"); err != nil {
		return nil, err
	}
	digest, err := fileDigest(symlinkPath)
	if err != nil {
		return nil, err
	}
	artifacts = append(artifacts, rescueArtifactDigest{Path: "untracked-symlinks.txt", SHA256: digest})
	return artifacts, nil
}

func writeIgnoredList(ctx context.Context, repoPath, dest string) error {
	output, err := gitOutputBytesContext(ctx, repoPath, "ls-files", "--others", "-i", "--exclude-standard")
	if err != nil {
		return err
	}
	return writeAtomicImpl(dest, output)
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

func identity(s string) string {
	return s
}

func stringsTrimSpace(s string) string {
	return strings.TrimSpace(s)
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
		KillProcessGroupUntilGone: rescueKillProcessGroupUntilGone,
		WorktreeProcessIDs:        rescueWorktreeProcessIDs,
		KillPID:                   rescueKillPID,
		Sleep:                     rescueSleep,
		Now:                       time.Now,
		PIDAlive:                  pidAlive,
		LookupProcessStartTime:    lookupLeaseStartTime,
		MaxWait:                   rescueQuiesceMaxWait,
		Interval:                  rescueQuiesceInterval,
	})
	switch {
	case err == nil:
		return nil
	case errors.Is(err, agentrunner.ErrRescueLeaseQuiesceTimedOut):
		return &agentrunner.ManualRecoveryRequiredError{
			Reason: contracts.RollbackReasonLeaseFailure,
			Detail: "step50: rescue lease quiesce timed out while worktree remained busy",
		}
	case errors.Is(err, agentrunner.ErrRescueLeaseQuiesceEnumerate):
		return &agentrunner.ManualRecoveryRequiredError{
			Reason: contracts.RollbackReasonLeaseFailure,
			Detail: fmt.Sprintf("step50: rescue lease quiesce failed to enumerate worktree processes: %v", err),
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

func shouldKillSavedProcessGroup(state resumeState) bool {
	return agentrunner.ShouldKillSavedProcessGroup(agentrunner.RescueLeaseState{
		PID:             state.Pid,
		PGID:            state.Pgid,
		LeaderStartTime: state.LeaderStartTime,
	}, pidAlive, lookupLeaseStartTime)
}

func parsePIDList(output string) []int {
	return agentrunner.ParsePIDList(output)
}

func recordRescueArtifact(budget *agentrunner.RescueArtifactBudget, path, logicalPath string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	return budget.RecordFile(logicalPath, info.Size())
}

func mapRescueCaptureError(step string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, agentrunner.ErrRescueDiffOverLimit) || errors.Is(err, agentrunner.ErrRescueStorageOverLimit) {
		return errors.Join(
			&agentrunner.ManualRecoveryRequiredError{
				Reason: contracts.RollbackReasonLeaseFailure,
				Detail: fmt.Sprintf("%s: rescue capture exceeded storage limits: %v", step, err),
			},
			err,
		)
	}
	return err
}
