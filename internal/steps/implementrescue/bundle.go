package implementrescue

import (
	"context"
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
