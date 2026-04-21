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

	stale, _, err := heartbeatStale(agentDir, s.staleAfter, s.now().UTC())
	if err != nil {
		return 0, err
	}
	if !stale {
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
	rescueID := fmt.Sprintf("%s-%s-rescue-%d-%d", filepath.Base(run.IO.RunDir()), run.Agent, state.RetryCount+1, s.now().UTC().Unix())
	rescueDir := filepath.Join(agentDir, rescuedDirName, rescueID)
	if err := ensureDir(filepath.Join(rescueDir, "untracked")); err != nil {
		return 0, err
	}

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

	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := writeGitOutputContext(ctx, allocation.Path, filepath.Join(rescueDir, "tracked.patch"), "diff", "HEAD", "--binary"); err != nil {
		return 0, err
	}
	if digest, err := fileDigest(filepath.Join(rescueDir, "tracked.patch")); err == nil {
		artifacts = append(artifacts, rescueArtifactDigest{Path: "tracked.patch", SHA256: digest})
	} else {
		return 0, err
	}

	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := writeGitOutputContext(ctx, allocation.Path, filepath.Join(rescueDir, "staged.patch"), "diff", "--cached", "--binary"); err != nil {
		return 0, err
	}
	if digest, err := fileDigest(filepath.Join(rescueDir, "staged.patch")); err == nil {
		artifacts = append(artifacts, rescueArtifactDigest{Path: "staged.patch", SHA256: digest})
	} else {
		return 0, err
	}

	if err := ctx.Err(); err != nil {
		return 0, err
	}
	untrackedArtifacts, err := copyUntrackedFiles(ctx, allocation.Path, rescueDir)
	if err != nil {
		return 0, err
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
	if err := writeJSONAtomicImpl(filepath.Join(rescueDir, "state.json"), rescueState); err != nil {
		return 0, err
	}
	if err := verifyRescueState(rescueDir); err != nil {
		return 0, err
	}

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
	state.Pid = os.Getpid()
	state.StartedAt = s.now().UTC()
	state.LastHeartbeat = state.StartedAt
	if err := touchHeartbeat(agentDir, state.LastHeartbeat); err != nil {
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

type rescueArtifactDigest struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type rescueStateFile struct {
	ExpectedBaseSHA string               `json:"expected_base_sha"`
	RescuedHeadSHA  string               `json:"rescued_head_sha"`
	RetryCount      int                  `json:"retry_count"`
	CommitCount     int                  `json:"commit_count"`
	BundleMode      string               `json:"bundle_mode"`
	CreatedAt       time.Time            `json:"created_at"`
	Artifacts       []rescueArtifactDigest `json:"artifacts"`
}

func verifyRescueState(rescueDir string) error {
	state, err := readJSON[rescueStateFile](filepath.Join(rescueDir, "state.json"))
	if err != nil {
		return err
	}
	for _, artifact := range state.Artifacts {
		digest, err := fileDigest(filepath.Join(rescueDir, artifact.Path))
		if err != nil {
			return err
		}
		if digest != artifact.SHA256 {
			return fmt.Errorf("step50: rescue artifact digest mismatch: path=%s", artifact.Path)
		}
	}
	return nil
}

func writeCommitBundle(ctx context.Context, repoPath, rescueDir, expectedBaseSHA string) (int, string, error) {
	revListOutput, err := gitOutputBytesContext(ctx, repoPath, "rev-list", expectedBaseSHA+"..HEAD")
	if err == nil && len(strings.Fields(string(revListOutput))) > 0 {
		bundlePath := filepath.Join(rescueDir, "commits.bundle")
		if err := runGitCommand(ctx, repoPath, "bundle", "create", bundlePath, expectedBaseSHA+"..HEAD"); err != nil {
			return 0, "", err
		}
		return len(strings.Fields(string(revListOutput))), "reachable_range", nil
	}

	headOutput, err := gitOutputBytesContext(ctx, repoPath, "rev-list", "HEAD")
	if err != nil {
		return 0, "", err
	}
	bundlePath := filepath.Join(rescueDir, "commits.bundle")
	if err := runGitCommand(ctx, repoPath, "bundle", "create", bundlePath, "HEAD"); err != nil {
		return 0, "", err
	}
	return len(strings.Fields(string(headOutput))), "full_head", nil
}

func runGitCommand(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("step50: git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func gitOutputBytesContext(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
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
	output, err := gitOutputBytesContext(ctx, dir, args...)
	if err != nil {
		return err
	}
	return writeAtomicImpl(dest, output)
}

func copyUntrackedFiles(ctx context.Context, repoPath, rescueDir string) ([]rescueArtifactDigest, error) {
	output, err := gitOutputBytesContext(ctx, repoPath, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	entries := strings.Split(string(output), "\x00")
	rescueBase := filepath.Join(rescueDir, "untracked")
	symlinkLog := make([]string, 0)
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
			symlinkLog = append(symlinkLog, cleaned)
			continue
		}
		if err := ensureDir(filepath.Dir(dst)); err != nil {
			return nil, err
		}
		if err := copyFile(src, dst, info.Mode().Perm()); err != nil {
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
	if err := writeAtomicImpl(symlinkPath, []byte(strings.Join(symlinkLog, "\n"))); err != nil {
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

func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
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
	if err := out.Close(); err != nil {
		return err
	}
	return syncDir(filepath.Dir(dst))
}

func syncDir(path string) error {
	dir, err := os.Open(filepath.Clean(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
