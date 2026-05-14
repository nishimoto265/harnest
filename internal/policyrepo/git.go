package policyrepo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/nishimoto265/harnest/internal/gitremote"
	"github.com/nishimoto265/harnest/internal/processenv"
)

var runGit = func(ctx context.Context, env []string, args ...string) ([]byte, error) {
	cmd, err := processenv.TrustedCommandContext(ctx, "git", args...)
	if err != nil {
		return nil, err
	}
	cmd.Env = env
	return cmd.CombinedOutput()
}

func fetchBranch(ctx context.Context, repoRoot, branch string) error {
	remoteURL, err := originRemoteURL(ctx, repoRoot)
	if err != nil {
		return err
	}
	refspec := fmt.Sprintf("+refs/heads/%s:refs/remotes/origin/%s", branch, branch)
	out, err := runGit(ctx, processenv.GitNetworkEnvForRemoteURL(remoteURL), "-C", repoRoot, "fetch", "--no-tags", "origin", refspec)
	if err != nil {
		return fmt.Errorf("policyrepo: fetch branch %s: %w: %s", branch, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func originRemoteURL(ctx context.Context, repoRoot string) (string, error) {
	out, err := gitText(ctx, repoRoot, "remote", "get-url", "origin")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func originPushURL(ctx context.Context, repoRoot string) (string, error) {
	out, err := gitText(ctx, repoRoot, "remote", "get-url", "--push", "--all", "origin")
	if err != nil {
		return "", err
	}
	return gitremote.PreferredRemoteURLForAuth(string(out)), nil
}

func branchHead(ctx context.Context, repoRoot, branch string) (string, error) {
	head, err := gitText(ctx, repoRoot, "rev-parse", "origin/"+branch)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(head)), nil
}

func gitRaw(ctx context.Context, repoRoot string, args ...string) ([]byte, error) {
	out, err := runGit(ctx, processenv.GitLocalEnv(), append([]string{"-C", repoRoot}, args...)...)
	if err != nil {
		return nil, fmt.Errorf("policyrepo: git %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

func gitText(ctx context.Context, repoRoot string, args ...string) ([]byte, error) {
	out, err := gitRaw(ctx, repoRoot, args...)
	if err != nil {
		return nil, err
	}
	return []byte(strings.TrimSpace(string(out))), nil
}

func hasStagedDiff(ctx context.Context, repoRoot string) (bool, error) {
	_, err := runGit(ctx, processenv.GitLocalEnv(), "-C", repoRoot, "diff", "--no-ext-diff", "--cached", "--quiet", "--", ".")
	if err == nil {
		return false, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return true, nil
	}
	return false, fmt.Errorf("policyrepo: git diff --cached --quiet -- .: %w", err)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func removeWorktree(repoRoot, path string) error {
	out, err := runGit(context.Background(), processenv.GitLocalEnv(), "-C", repoRoot, "worktree", "remove", "--force", path)
	if err != nil {
		return fmt.Errorf("policyrepo: remove worktree %s: %w: %s", path, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func sanitizeRunID(runID string) string {
	replacer := strings.NewReplacer("/", "-", "\\", "-", " ", "-")
	return replacer.Replace(runID)
}
