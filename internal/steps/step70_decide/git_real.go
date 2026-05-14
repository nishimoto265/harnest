package step70_decide

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/nishimoto265/harnest/internal/contracts"
	"github.com/nishimoto265/harnest/internal/gitremote"
	"github.com/nishimoto265/harnest/internal/processenv"
	"github.com/nishimoto265/harnest/internal/worktreecleanup"
)

// RealGitOps executes the production git commands against the source repo.
type RealGitOps struct {
	RepoDir string
	Remote  string
}

func (g RealGitOps) RemoteHead(ctx context.Context, branch string) (string, error) {
	remote := g.remoteName()
	remoteURL := g.remoteURL(ctx, remote)
	cmd, err := processenv.TrustedCommandContext(ctx, "git", "-C", g.RepoDir, "ls-remote", "--heads", remote, branch)
	if err != nil {
		return "", err
	}
	// ls-remote hits the network; preserve ssh-agent/token auth and scope HTTPS token auth to the resolved remote host.
	cmd.Env = processenv.GitNetworkEnvForRemoteURL(remoteURL)
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", err
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return "", nil
	}
	return fields[0], nil
}

func (g RealGitOps) PushForceWithLease(ctx context.Context, branch, targetSHA, expected string) error {
	remote := g.remoteName()
	remoteURL := g.remotePushURL(ctx, remote)
	branchRef := remoteBranchRef(branch)
	refspec := fmt.Sprintf("%s:%s", targetSHA, branchRef)
	lease := fmt.Sprintf("--force-with-lease=%s:%s", branchRef, expected)
	cmd, err := processenv.TrustedCommandContext(ctx, "git", "-C", g.RepoDir, "push", remote, refspec, lease)
	if err != nil {
		return err
	}
	// push hits the network; preserve ssh-agent/token auth and scope HTTPS token auth to the resolved remote host.
	cmd.Env = processenv.GitNetworkEnvForRemoteURL(remoteURL)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		msg := stderr.String()
		if strings.Contains(msg, "stale info") || strings.Contains(msg, "fetch first") || strings.Contains(msg, "non-fast-forward") {
			return fmt.Errorf("%w: %s", ErrLeaseFailure, strings.TrimSpace(msg))
		}
		return err
	}
	return nil
}

func remoteBranchRef(branch string) string {
	if strings.HasPrefix(branch, "refs/") {
		return branch
	}
	return "refs/heads/" + branch
}

func (g RealGitOps) RemoveWorktree(ctx context.Context, path string) error {
	return worktreecleanup.RepoGit{RepoDir: g.RepoDir}.RemoveWorktree(ctx, path)
}

func (g RealGitOps) DeleteBranch(ctx context.Context, branch string) error {
	return worktreecleanup.RepoGit{RepoDir: g.RepoDir}.DeleteBranch(ctx, branch)
}

func (g RealGitOps) VerifyUnregisteredWorktreeRemoval(ctx context.Context, allocation contracts.WorktreeAllocation) error {
	return worktreecleanup.RepoGit{RepoDir: g.RepoDir}.VerifyUnregisteredWorktreeRemoval(ctx, allocation)
}

func (g RealGitOps) remoteName() string {
	if g.Remote != "" {
		return g.Remote
	}
	return "origin"
}

func (g RealGitOps) remoteURL(ctx context.Context, remote string) string {
	cmd, err := processenv.TrustedCommandContext(ctx, "git", "-C", g.RepoDir, "remote", "get-url", remote)
	if err != nil {
		return remote
	}
	cmd.Env = processenv.GitLocalEnv()
	out, err := cmd.Output()
	if err != nil {
		return remote
	}
	if remoteURL := strings.TrimSpace(string(out)); remoteURL != "" {
		return remoteURL
	}
	return remote
}

func (g RealGitOps) remotePushURL(ctx context.Context, remote string) string {
	cmd, err := processenv.TrustedCommandContext(ctx, "git", "-C", g.RepoDir, "remote", "get-url", "--push", "--all", remote)
	if err != nil {
		return g.remoteURL(ctx, remote)
	}
	cmd.Env = processenv.GitLocalEnv()
	out, err := cmd.Output()
	if err != nil {
		return g.remoteURL(ctx, remote)
	}
	if remoteURL := gitremote.PreferredRemoteURLForAuth(string(out)); remoteURL != "" {
		return remoteURL
	}
	return g.remoteURL(ctx, remote)
}
