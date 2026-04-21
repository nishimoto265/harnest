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

	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/validation"
)

type RescueExhaustedError struct {
	Agent      contracts.AgentID
	RetryCount int
}

func (e *RescueExhaustedError) Error() string {
	return fmt.Sprintf("step20: rescue exhausted: agent=%s retry_count=%d", e.Agent, e.RetryCount)
}

type rescueLock struct {
	file *os.File
}

func tryAcquireRescueLock(path string) (*rescueLock, bool, error) {
	if err := ensureDir(filepath.Dir(path)); err != nil {
		return nil, false, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return &rescueLock{file: file}, true, nil
}

func (l *rescueLock) Unlock() error {
	if l == nil || l.file == nil {
		return nil
	}
	if err := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN); err != nil {
		_ = l.file.Close()
		return err
	}
	return l.file.Close()
}

type rescueFileDigest struct {
	Path   string `json:"path" validate:"required"`
	SHA256 string `json:"sha256" validate:"required,len=64,hexadecimal"`
}

type rescueState struct {
	ExpectedBaseSHA string             `json:"expected_base_sha" validate:"required,sha1_hex"`
	RescuedHeadSHA  string             `json:"rescued_head_sha" validate:"required,sha1_hex"`
	CommitCount     int                `json:"commit_count" validate:"gte=0"`
	BundleMode      string             `json:"bundle_mode" validate:"required,oneof=range full_head none"`
	RetryCount      int                `json:"retry_count" validate:"gte=0"`
	CreatedAt       time.Time          `json:"created_at" validate:"required"`
	Files           []rescueFileDigest `json:"files" validate:"required,dive"`
}

func (s rescueState) Validate() error {
	return validation.Instance().Struct(s)
}

func (s *Step) resumeAndRescueIfNeeded(_ context.Context, cfg *config.Config, run RunContext, allocation contracts.WorktreeAllocation, paths agentPaths) (int, error) {
	state, ok, err := loadResumeState(paths.resumeStatePath)
	if err != nil || !ok {
		return 0, err
	}
	maxRetries := rescueMaxRetries(cfg)
	if state.RetryCount >= maxRetries {
		return 0, &RescueExhaustedError{
			Agent:      run.Agent,
			RetryCount: state.RetryCount,
		}
	}
	stale, err := s.isStaleRun(paths.heartbeatPath, state)
	if err != nil {
		return 0, err
	}
	if !stale {
		return state.RetryCount, nil
	}

	lock, acquired, err := tryAcquireRescueLock(paths.rescueLockPath)
	if err != nil {
		return 0, err
	}
	if !acquired {
		return state.RetryCount, nil
	}
	defer lock.Unlock()

	state, ok, err = loadResumeState(paths.resumeStatePath)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, nil
	}
	if state.RetryCount >= maxRetries {
		return 0, &RescueExhaustedError{
			Agent:      run.Agent,
			RetryCount: state.RetryCount,
		}
	}
	stale, err = s.isStaleRun(paths.heartbeatPath, state)
	if err != nil {
		return 0, err
	}
	if !stale {
		return state.RetryCount, nil
	}
	return s.performRescue(run, allocation, paths, state)
}

func (s *Step) isStaleRun(heartbeatPath string, state resumeState) (bool, error) {
	info, err := os.Stat(heartbeatPath)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	lastBeat := state.LastHeartbeat
	if err == nil {
		lastBeat = info.ModTime().UTC()
	}
	if s.now().UTC().Sub(lastBeat) <= s.staleAfter {
		return false, nil
	}
	return !pidAlive(state.PID), nil
}

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func (s *Step) performRescue(run RunContext, allocation contracts.WorktreeAllocation, paths agentPaths, state resumeState) (int, error) {
	if err := ensureWorktreeExists(run.Config, allocation); err != nil {
		return 0, err
	}

	headSHA, err := gitOutput(allocation.Path, "rev-parse", "HEAD")
	if err != nil {
		return 0, err
	}

	nextRetry := state.RetryCount + 1
	if err := snapshotRescueArtifacts(allocation.Path, paths.rescuedDir, string(run.IO.RunID), run.Agent, state.ExpectedBaseSHA, headSHA, nextRetry, s.now().UTC()); err != nil {
		saveErr := internalio.WriteJSONAtomic(paths.resumeStatePath, resumeState{
			ExpectedBaseSHA: state.ExpectedBaseSHA,
			StartedAt:       s.now().UTC(),
			PID:             os.Getpid(),
			RetryCount:      nextRetry,
			LastHeartbeat:   s.now().UTC(),
		})
		if saveErr != nil {
			return 0, errors.Join(err, saveErr)
		}
		return 0, err
	}

	if err := gitRun(allocation.Path, "reset", "--hard", state.ExpectedBaseSHA); err != nil {
		return 0, err
	}
	if err := gitRun(allocation.Path, "clean", "-fd"); err != nil {
		return 0, err
	}

	if err := internalio.WriteJSONAtomic(paths.resumeStatePath, resumeState{
		ExpectedBaseSHA: state.ExpectedBaseSHA,
		StartedAt:       s.now().UTC(),
		PID:             os.Getpid(),
		RetryCount:      nextRetry,
		LastHeartbeat:   s.now().UTC(),
	}); err != nil {
		return 0, err
	}
	return nextRetry, nil
}

func ensureWorktreeExists(cfg *config.Config, allocation contracts.WorktreeAllocation) error {
	if _, err := os.Stat(allocation.Path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if cfg == nil {
		return os.ErrNotExist
	}
	repoRoot, err := cfg.RepoRoot()
	if err != nil {
		return err
	}
	if err := ensureDir(filepath.Dir(allocation.Path)); err != nil {
		return err
	}
	return gitRun(repoRoot, "worktree", "add", "--force", "-B", allocation.Branch, allocation.Path, allocation.BaseSHA)
}

func snapshotRescueArtifacts(worktreePath, rescuedRoot, runID string, agent contracts.AgentID, expectedBaseSHA, headSHA string, retryCount int, now time.Time) error {
	rescueID := fmt.Sprintf("%s-%s-rescue-%d-%d", runID, agent, retryCount, now.Unix())
	rescueDir := filepath.Join(rescuedRoot, rescueID)
	if err := ensureDir(rescueDir); err != nil {
		return err
	}

	state := rescueState{
		ExpectedBaseSHA: expectedBaseSHA,
		RescuedHeadSHA:  headSHA,
		RetryCount:      retryCount,
		CreatedAt:       now,
		Files:           make([]rescueFileDigest, 0, 8),
		BundleMode:      "none",
	}

	commits, err := gitOutputRaw(worktreePath, "rev-list", expectedBaseSHA+"..HEAD")
	if err == nil && strings.TrimSpace(string(commits)) != "" {
		bundlePath := filepath.Join(rescueDir, "commits.bundle")
		if err := gitRun(worktreePath, "bundle", "create", bundlePath, expectedBaseSHA+"..HEAD"); err == nil {
			state.CommitCount = strings.Count(strings.TrimSpace(string(commits)), "\n") + 1
			state.BundleMode = "range"
			digest, err := recordFileDigest(bundlePath, rescueDir)
			if err != nil {
				return err
			}
			state.Files = append(state.Files, digest)
		}
	}
	if state.BundleMode == "none" && headSHA != expectedBaseSHA {
		bundlePath := filepath.Join(rescueDir, "commits.bundle")
		if err := gitRun(worktreePath, "bundle", "create", bundlePath, "HEAD"); err != nil {
			return err
		}
		state.BundleMode = "full_head"
		digest, err := recordFileDigest(bundlePath, rescueDir)
		if err != nil {
			return err
		}
		state.Files = append(state.Files, digest)
	}

	trackedPatch := filepath.Join(rescueDir, "tracked.patch")
	trackedData, err := gitOutputRaw(worktreePath, "diff", "HEAD", "--binary", "--", ".")
	if err != nil {
		return err
	}
	if err := internalio.WriteAtomic(trackedPatch, trackedData); err != nil {
		return err
	}
	digest, err := recordFileDigest(trackedPatch, rescueDir)
	if err != nil {
		return err
	}
	state.Files = append(state.Files, digest)

	stagedPatch := filepath.Join(rescueDir, "staged.patch")
	stagedData, err := gitOutputRaw(worktreePath, "diff", "--cached", "--binary", "--", ".")
	if err != nil {
		return err
	}
	if err := internalio.WriteAtomic(stagedPatch, stagedData); err != nil {
		return err
	}
	digest, err = recordFileDigest(stagedPatch, rescueDir)
	if err != nil {
		return err
	}
	state.Files = append(state.Files, digest)

	untrackedFiles, err := gitOutputRaw(worktreePath, "ls-files", "--others", "--exclude-standard", "-z", "--", ".")
	if err != nil {
		return err
	}
	symlinkLines := make([]string, 0)
	for _, entry := range bytesSplitNUL(untrackedFiles) {
		cleaned := filepath.Clean(entry)
		if cleaned == "." {
			continue
		}
		if strings.HasPrefix(cleaned, "..") {
			return fmt.Errorf("step20: rescue rejected untracked path outside worktree: %q", entry)
		}
		src := filepath.Join(worktreePath, cleaned)
		dst := filepath.Join(rescueDir, "untracked", cleaned)
		if !strings.HasPrefix(dst, filepath.Join(rescueDir, "untracked")+string(os.PathSeparator)) && dst != filepath.Join(rescueDir, "untracked") {
			return fmt.Errorf("step20: rescue destination escaped base: %q", dst)
		}
		info, err := os.Lstat(src)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(src)
			if err != nil {
				return err
			}
			symlinkLines = append(symlinkLines, cleaned+" -> "+target)
			continue
		}
		if info.IsDir() {
			continue
		}
		if err := copyRegularFile(src, dst, info.Mode()); err != nil {
			return err
		}
		digest, err := recordFileDigest(dst, rescueDir)
		if err != nil {
			return err
		}
		state.Files = append(state.Files, digest)
	}
	if len(symlinkLines) > 0 {
		symlinkPath := filepath.Join(rescueDir, "untracked-symlinks.txt")
		if err := internalio.WriteAtomic(symlinkPath, []byte(strings.Join(symlinkLines, "\n")+"\n")); err != nil {
			return err
		}
		digest, err := recordFileDigest(symlinkPath, rescueDir)
		if err != nil {
			return err
		}
		state.Files = append(state.Files, digest)
	}

	ignoredPath := filepath.Join(rescueDir, "ignored.txt")
	ignored, err := gitOutputRaw(worktreePath, "ls-files", "--others", "-i", "--exclude-standard", "--", ".")
	if err != nil {
		return err
	}
	if err := internalio.WriteAtomic(ignoredPath, ignored); err != nil {
		return err
	}
	digest, err = recordFileDigest(ignoredPath, rescueDir)
	if err != nil {
		return err
	}
	state.Files = append(state.Files, digest)

	statePath := filepath.Join(rescueDir, "state.json")
	if err := internalio.WriteJSONAtomic(statePath, state); err != nil {
		return err
	}

	loadedState, err := internalio.ReadJSON[rescueState](statePath)
	if err != nil {
		return err
	}
	for _, file := range loadedState.Files {
		if err := verifyFileDigest(filepath.Join(rescueDir, file.Path), file.SHA256); err != nil {
			return err
		}
	}
	return nil
}

func bytesSplitNUL(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	parts := strings.Split(string(data), "\x00")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func recordFileDigest(path, base string) (rescueFileDigest, error) {
	sum, err := fileSHA256(path)
	if err != nil {
		return rescueFileDigest{}, err
	}
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return rescueFileDigest{}, err
	}
	return rescueFileDigest{
		Path:   rel,
		SHA256: sum,
	}, nil
}

func verifyFileDigest(path, expected string) error {
	got, err := fileSHA256(path)
	if err != nil {
		return err
	}
	if got != expected {
		return fmt.Errorf("step20: rescue digest mismatch: path=%s expected=%s got=%s", path, expected, got)
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func copyRegularFile(src, dst string, mode os.FileMode) error {
	if err := ensureDir(filepath.Dir(dst)); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode.Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
