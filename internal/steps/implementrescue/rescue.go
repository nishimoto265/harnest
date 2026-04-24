package implementrescue

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/contracts/stepio"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/steps/agentrunner"
)

const (
	MaxUntrackedBytes = 32 << 20
)

type State struct {
	ExpectedBaseSHA string    `json:"expected_base_sha" validate:"required,sha1_hex"`
	StartedAt       time.Time `json:"started_at,omitempty"`
	Pid             int       `json:"pid,omitempty" validate:"gte=0"`
	Pgid            int       `json:"pgid,omitempty" validate:"gte=0"`
	LeaderStartTime string    `json:"leader_start_time,omitempty"`
	RetryCount      int       `json:"retry_count" validate:"gte=0"`
	LastHeartbeat   time.Time `json:"last_heartbeat,omitempty"`
}

type Lock struct {
	file *os.File
}

func TryAcquireLock(path string, ensureDir func(string) error) (*Lock, bool, error) {
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
	return &Lock{file: f}, true, nil
}

func (l *Lock) Unlock() error {
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
	currentDirtyFingerprint, err := agentrunner.ComputeDirtyFingerprint(ctx, opts.Allocation.Path)
	if err != nil {
		return 0, err
	}
	rescueDir, adopted, err := FindExistingDir(opts.AgentDir, opts.RescuedDirName, opts.State.ExpectedBaseSHA, nextRetry, currentHead, currentDirtyFingerprint, opts.VerifyState)
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
	if opts.VerifyState == nil {
		return errors.New("implementrescue: perform missing VerifyState")
	}
	if opts.FinishState == nil {
		return errors.New("implementrescue: perform missing FinishState")
	}
	return nil
}

func CaptureArtifacts(ctx context.Context, opts PerformOptions, rescueDir, headSHA, dirtyFingerprint string, nextRetry int) error {
	budget := agentrunner.NewRescueArtifactBudget()
	artifacts := make([]agentrunner.RescueArtifactDigest, 0, 8)

	commitCount, bundleMode, err := opts.WriteBundle(ctx, opts.Allocation.Path, rescueDir, opts.State.ExpectedBaseSHA)
	if err != nil {
		return err
	}
	if digest, err := opts.FileDigest(filepath.Join(rescueDir, "commits.bundle")); err == nil {
		artifacts = append(artifacts, agentrunner.RescueArtifactDigest{Path: "commits.bundle", SHA256: digest})
	} else {
		return err
	}
	if err := MapCaptureError(opts.StepName, recordArtifact(&budget, filepath.Join(rescueDir, "commits.bundle"), "commits.bundle")); err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		return err
	}
	if err := MapCaptureError(opts.StepName, opts.WriteGitOutput(ctx, opts.Allocation.Path, filepath.Join(rescueDir, "tracked.patch"), "diff", "HEAD", "--binary", "--no-ext-diff", "--no-textconv")); err != nil {
		return err
	}
	if digest, err := opts.FileDigest(filepath.Join(rescueDir, "tracked.patch")); err == nil {
		artifacts = append(artifacts, agentrunner.RescueArtifactDigest{Path: "tracked.patch", SHA256: digest})
	} else {
		return err
	}
	if err := MapCaptureError(opts.StepName, recordArtifact(&budget, filepath.Join(rescueDir, "tracked.patch"), "tracked.patch")); err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		return err
	}
	if err := MapCaptureError(opts.StepName, opts.WriteGitOutput(ctx, opts.Allocation.Path, filepath.Join(rescueDir, "staged.patch"), "diff", "--cached", "--binary", "--no-ext-diff", "--no-textconv")); err != nil {
		return err
	}
	if digest, err := opts.FileDigest(filepath.Join(rescueDir, "staged.patch")); err == nil {
		artifacts = append(artifacts, agentrunner.RescueArtifactDigest{Path: "staged.patch", SHA256: digest})
	} else {
		return err
	}
	if err := MapCaptureError(opts.StepName, recordArtifact(&budget, filepath.Join(rescueDir, "staged.patch"), "staged.patch")); err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		return err
	}
	untrackedArtifacts, err := opts.CopyUntracked(ctx, opts.Allocation.Path, rescueDir, &budget)
	if err != nil {
		return MapCaptureError(opts.StepName, err)
	}
	artifacts = append(artifacts, untrackedArtifacts...)

	ignoredArtifacts, err := opts.CopyIgnored(ctx, opts.Allocation.Path, rescueDir, &budget)
	if err != nil {
		return MapCaptureError(opts.StepName, err)
	}
	artifacts = append(artifacts, ignoredArtifacts...)

	ignoredPath := filepath.Join(rescueDir, "ignored.txt")
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := opts.WriteIgnored(ctx, opts.Allocation.Path, ignoredPath); err != nil {
		return err
	}
	if digest, err := opts.FileDigest(ignoredPath); err == nil {
		artifacts = append(artifacts, agentrunner.RescueArtifactDigest{Path: "ignored.txt", SHA256: digest})
	} else {
		return err
	}
	if err := MapCaptureError(opts.StepName, recordArtifact(&budget, ignoredPath, "ignored.txt")); err != nil {
		return err
	}

	rescueState := agentrunner.RescueStateFile{
		ExpectedBaseSHA:  opts.State.ExpectedBaseSHA,
		RescuedHeadSHA:   headSHA,
		RetryCount:       nextRetry,
		CommitCount:      commitCount,
		BundleMode:       bundleMode,
		CreatedAt:        rescueNow(opts.Now).UTC(),
		Artifacts:        artifacts,
		DirtyFingerprint: dirtyFingerprint,
	}
	if err := agentrunner.WriteRescueState(filepath.Join(rescueDir, "state.json"), rescueState); err != nil {
		return err
	}
	return opts.VerifyState(rescueDir)
}

func FindExistingDir(agentDir, rescuedDirName, expectedBaseSHA string, nextRetry int, currentHead, currentDirtyFingerprint string, verifyState func(string) error) (string, bool, error) {
	rescueRoot := filepath.Join(agentDir, rescuedDirName)
	entries, err := os.ReadDir(rescueRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}

	var selectedDir string
	var selectedState agentrunner.RescueStateFile
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		candidateDir := filepath.Join(rescueRoot, entry.Name())
		state, err := agentrunner.ReadRescueState(filepath.Join(candidateDir, "state.json"))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", false, err
		}
		if state.ExpectedBaseSHA != expectedBaseSHA || state.RetryCount != nextRetry {
			continue
		}
		if state.RescuedHeadSHA != currentHead {
			continue
		}
		if state.DirtyFingerprint == "" || state.DirtyFingerprint != currentDirtyFingerprint {
			continue
		}
		if !rescueStateHasIgnoredCoverage(state) {
			continue
		}
		if err := verifyState(candidateDir); err != nil {
			continue
		}
		if selectedDir == "" || state.CreatedAt.After(selectedState.CreatedAt) {
			selectedDir = candidateDir
			selectedState = state
		}
	}
	if selectedDir == "" {
		return "", false, nil
	}
	return selectedDir, true, nil
}

func rescueStateHasIgnoredCoverage(state agentrunner.RescueStateFile) bool {
	hasIgnoredList := false
	hasIgnoredSkipped := false
	for _, artifact := range state.Artifacts {
		switch artifact.Path {
		case "ignored.txt":
			hasIgnoredList = true
		case "ignored-skipped.txt":
			hasIgnoredSkipped = true
		}
	}
	return hasIgnoredList && hasIgnoredSkipped
}

type GitOutputBytesFunc func(context.Context, string, ...string) ([]byte, error)
type RunGitFunc func(context.Context, string, ...string) error

func WriteCommitBundle(ctx context.Context, repoPath, rescueDir, expectedBaseSHA string, gitOutputBytes GitOutputBytesFunc, runGit RunGitFunc) (int, string, error) {
	bundlePath := filepath.Join(rescueDir, "commits.bundle")
	revListOutput, err := gitOutputBytes(ctx, repoPath, "rev-list", expectedBaseSHA+"..HEAD")
	if err != nil {
		commitCount, err := writeFullHeadBundle(ctx, repoPath, bundlePath, gitOutputBytes, runGit)
		if err != nil {
			return 0, "", err
		}
		return commitCount, agentrunner.RescueBundleModeFullHead, nil
	}
	commits := strings.Fields(string(revListOutput))
	if len(commits) == 0 {
		if err := internalio.WriteAtomic(bundlePath, nil); err != nil {
			return 0, "", err
		}
		return 0, agentrunner.RescueBundleModeNone, nil
	}
	if err := runGit(ctx, repoPath, "bundle", "create", bundlePath, expectedBaseSHA+"..HEAD"); err == nil {
		return len(commits), agentrunner.RescueBundleModeRange, nil
	}
	commitCount, err := writeFullHeadBundle(ctx, repoPath, bundlePath, gitOutputBytes, runGit)
	if err != nil {
		return 0, "", err
	}
	return commitCount, agentrunner.RescueBundleModeFullHead, nil
}

func writeFullHeadBundle(ctx context.Context, repoPath, bundlePath string, gitOutputBytes GitOutputBytesFunc, runGit RunGitFunc) (int, error) {
	headOutput, err := gitOutputBytes(ctx, repoPath, "rev-list", "HEAD")
	if err != nil {
		return 0, err
	}
	if err := runGit(ctx, repoPath, "bundle", "create", bundlePath, "HEAD", "--objects"); err != nil {
		return 0, err
	}
	return len(strings.Fields(string(headOutput))), nil
}

type CopyOpenFileFunc func(context.Context, *os.File, string, os.FileMode, int64) error

func CopyUntrackedFilesWithBudget(ctx context.Context, stepName, repoPath, rescueDir string, budget *agentrunner.RescueArtifactBudget, gitOutputBytes GitOutputBytesFunc, ensureDir func(string) error, copyOpenFile CopyOpenFileFunc, fileDigest func(string) (string, error)) ([]agentrunner.RescueArtifactDigest, error) {
	return copyOtherFilesWithBudget(ctx, stepName, repoPath, rescueDir, "untracked", "untracked-symlinks.txt", []string{"ls-files", "--others", "--exclude-standard", "-z"}, budget, gitOutputBytes, ensureDir, copyOpenFile, fileDigest)
}

func CopyIgnoredFilesWithBudget(ctx context.Context, stepName, repoPath, rescueDir string, budget *agentrunner.RescueArtifactBudget, gitOutputBytes GitOutputBytesFunc, ensureDir func(string) error, copyOpenFile CopyOpenFileFunc, fileDigest func(string) (string, error)) ([]agentrunner.RescueArtifactDigest, error) {
	return copyOtherFilesWithBudget(ctx, stepName, repoPath, rescueDir, "ignored", "ignored-skipped.txt", []string{"ls-files", "--others", "-i", "--exclude-standard", "-z"}, budget, gitOutputBytes, ensureDir, copyOpenFile, fileDigest)
}

func copyOtherFilesWithBudget(ctx context.Context, stepName, repoPath, rescueDir, rescueSubdir, skipLogName string, listArgs []string, budget *agentrunner.RescueArtifactBudget, gitOutputBytes GitOutputBytesFunc, ensureDir func(string) error, copyOpenFile CopyOpenFileFunc, fileDigest func(string) (string, error)) ([]agentrunner.RescueArtifactDigest, error) {
	output, err := gitOutputBytes(ctx, repoPath, listArgs...)
	if err != nil {
		return nil, err
	}
	entries := strings.Split(string(output), "\x00")
	rescueBase := filepath.Join(rescueDir, rescueSubdir)
	skipLog := make([]string, 0)
	artifacts := make([]agentrunner.RescueArtifactDigest, 0, len(entries)+1)
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
			return nil, fmt.Errorf("%s: untracked file escapes rescue dir: %s", stepName, entry)
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
		if size > MaxUntrackedBytes {
			_ = file.Close()
			skipLog = append(skipLog, fmt.Sprintf("skipped_too_large:%s:%d", cleaned, size))
			continue
		}
		artifactPath := filepath.ToSlash(filepath.Join(rescueSubdir, cleaned))
		if err := budget.RecordFile(artifactPath, size); err != nil {
			_ = file.Close()
			return nil, err
		}
		if err := ensureDir(filepath.Dir(dst)); err != nil {
			_ = file.Close()
			return nil, err
		}
		if err := copyOpenFile(ctx, file, dst, perm, MaxUntrackedBytes); err != nil {
			return nil, err
		}
		digest, err := fileDigest(dst)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, agentrunner.RescueArtifactDigest{
			Path:   artifactPath,
			SHA256: digest,
		})
	}
	symlinkPath := filepath.Join(rescueDir, skipLogName)
	if err := internalio.WriteAtomic(symlinkPath, []byte(strings.Join(skipLog, "\n"))); err != nil {
		return nil, err
	}
	if err := recordArtifact(budget, symlinkPath, skipLogName); err != nil {
		return nil, err
	}
	digest, err := fileDigest(symlinkPath)
	if err != nil {
		return nil, err
	}
	artifacts = append(artifacts, agentrunner.RescueArtifactDigest{Path: skipLogName, SHA256: digest})
	return artifacts, nil
}

func WriteIgnoredList(ctx context.Context, repoPath, dest string, gitOutputBytes GitOutputBytesFunc) error {
	output, err := gitOutputBytes(ctx, repoPath, "ls-files", "--others", "-i", "--exclude-standard", "-z")
	if err != nil {
		return err
	}
	entries := strings.Split(strings.Trim(string(output), "\x00"), "\x00")
	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry == "" {
			continue
		}
		lines = append(lines, strconv.Quote(entry))
	}
	return internalio.WriteAtomic(dest, []byte(strings.Join(lines, "\n")))
}

func FinishState(agentDir string, state State, nextRetry int, heartbeatPath func(string) string, saveState func(string, State) error) (int, error) {
	state.RetryCount = nextRetry
	state.StartedAt = time.Time{}
	state.LastHeartbeat = time.Time{}
	state.Pid = 0
	state.Pgid = 0
	state.LeaderStartTime = ""
	if err := os.Remove(heartbeatPath(agentDir)); err != nil && !os.IsNotExist(err) {
		return 0, err
	}
	if err := saveState(agentDir, state); err != nil {
		return 0, err
	}
	return nextRetry, nil
}

func MapCaptureError(stepName string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, agentrunner.ErrRescueDiffOverLimit) || errors.Is(err, agentrunner.ErrRescueStorageOverLimit) {
		return errors.Join(
			&agentrunner.ManualRecoveryRequiredError{
				Reason: contracts.RollbackReasonLeaseFailure,
				Detail: fmt.Sprintf("%s: rescue capture exceeded storage limits: %v", stepName, err),
			},
			err,
		)
	}
	return err
}

func ToExhaustedResult(agent contracts.AgentID, retryCount int) stepio.RescueExhausted {
	return stepio.RescueExhausted{
		Agent:      agent,
		RetryCount: retryCount,
	}
}

func recordArtifact(budget *agentrunner.RescueArtifactBudget, path, logicalPath string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	return budget.RecordFile(logicalPath, info.Size())
}

func identity(s string) string {
	return s
}

func rescueNow(now func() time.Time) time.Time {
	if now == nil {
		return time.Now()
	}
	return now()
}
