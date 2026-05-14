package implementrescue

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/steps/agentrunner"
)

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
	if err := writeGitBundleAtomic(ctx, repoPath, bundlePath, runGit, expectedBaseSHA+"..HEAD"); err == nil {
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
	if err := writeGitBundleAtomic(ctx, repoPath, bundlePath, runGit, "HEAD", "--objects"); err != nil {
		return 0, err
	}
	return len(strings.Fields(string(headOutput))), nil
}

func writeGitBundleAtomic(ctx context.Context, repoPath, bundlePath string, runGit RunGitFunc, revArgs ...string) error {
	tempDir, err := os.MkdirTemp("", "auto-improve-rescue-bundle-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempDir)

	tempBundlePath := filepath.Join(tempDir, "commits.bundle")
	args := append([]string{"bundle", "create", tempBundlePath}, revArgs...)
	if err := runGit(ctx, repoPath, args...); err != nil {
		return err
	}
	data, err := readRegularFileWithLimit(tempBundlePath, agentrunner.RescueArtifactTotalLimitBytes)
	if err != nil {
		return err
	}
	if err := internalio.WriteAtomic(bundlePath, data); err != nil {
		return err
	}
	return nil
}

func readRegularFileWithLimit(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("implementrescue: bundle is not a regular file: %s", path)
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("%w: path=%s bytes=%d limit=%d", agentrunner.ErrRescueStorageOverLimit, path, info.Size(), limit)
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w: path=%s bytes=%d limit=%d", agentrunner.ErrRescueStorageOverLimit, path, len(data), limit)
	}
	return data, nil
}
