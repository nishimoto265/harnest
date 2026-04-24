package step70_decide

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nishimoto265/auto-improve/internal/processenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRealGitOpsRemoteHeadAndPushForceWithLeaseLocalBareOrigin(t *testing.T) {
	fixture := newRealGitFixture(t)
	ctx := context.Background()
	gitOps := RealGitOps{RepoDir: fixture.repo, Remote: "origin"}

	base := fixture.revParse(t, fixture.repo, "HEAD")
	remoteHead, err := gitOps.RemoteHead(ctx, realGitBranch)
	require.NoError(t, err)
	assert.Equal(t, base, remoteHead)

	target := fixture.commit(t, fixture.repo, "next.txt", "next\n", "next")
	require.NoError(t, gitOps.PushForceWithLease(ctx, realGitBranch, target, base))

	remoteHead, err = gitOps.RemoteHead(ctx, realGitBranch)
	require.NoError(t, err)
	assert.Equal(t, target, remoteHead)
}

func TestRealGitOpsPushForceWithLeaseClassifiesStaleLease(t *testing.T) {
	fixture := newRealGitFixture(t)
	ctx := context.Background()
	gitOps := RealGitOps{RepoDir: fixture.repo, Remote: "origin"}

	base := fixture.revParse(t, fixture.repo, "HEAD")
	staleTarget := fixture.commit(t, fixture.repo, "stale.txt", "stale\n", "stale target")

	other := filepath.Join(fixture.root, "other")
	fixture.runGit(t, "", "clone", fixture.origin, other)
	fixture.runGit(t, other, "checkout", "-b", realGitBranch, "origin/"+realGitBranch)
	fixture.configureUser(t, other)
	advanced := fixture.commit(t, other, "advanced.txt", "advanced\n", "advance remote")
	fixture.runGit(t, other, "push", "origin", "HEAD:"+realGitBranch)

	err := gitOps.PushForceWithLease(ctx, realGitBranch, staleTarget, base)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrLeaseFailure), "err=%v", err)

	remoteHead, headErr := gitOps.RemoteHead(ctx, realGitBranch)
	require.NoError(t, headErr)
	assert.Equal(t, advanced, remoteHead)
}

func TestRealGitOpsRemoveWorktreeRegisteredAndUnregisteredPaths(t *testing.T) {
	fixture := newRealGitFixture(t)
	ctx := context.Background()
	gitOps := RealGitOps{RepoDir: fixture.repo, Remote: "origin"}

	worktreePath := filepath.Join(fixture.root, "registered-worktree")
	fixture.runGit(t, fixture.repo, "worktree", "add", "-b", "cleanup-worktree", worktreePath, "HEAD")

	require.NoError(t, gitOps.RemoveWorktree(ctx, worktreePath))
	assert.NoDirExists(t, worktreePath)

	unregisteredPath := filepath.Join(fixture.root, "unregistered-worktree")
	require.NoError(t, os.MkdirAll(unregisteredPath, 0o755))

	err := gitOps.RemoveWorktree(ctx, unregisteredPath)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrWorktreeUnregistered), "err=%v", err)
	assert.DirExists(t, unregisteredPath)
}

const realGitBranch = "auto-improve/best"

type realGitFixture struct {
	root   string
	origin string
	repo   string
	git    string
}

func newRealGitFixture(t *testing.T) realGitFixture {
	t.Helper()

	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git executable not available")
	}
	restoreTrustedPath := processenv.SetTrustedPathForTest(filepath.Dir(gitPath) + string(os.PathListSeparator) + processenv.TrustedPath())
	t.Cleanup(restoreTrustedPath)

	root := t.TempDir()
	fixture := realGitFixture{
		root:   root,
		origin: filepath.Join(root, "origin.git"),
		repo:   filepath.Join(root, "repo"),
		git:    gitPath,
	}
	fixture.runGit(t, "", "init", "--bare", fixture.origin)
	fixture.runGit(t, "", "init", "-b", "main", fixture.repo)
	fixture.configureUser(t, fixture.repo)
	require.NoError(t, os.WriteFile(filepath.Join(fixture.repo, "README.md"), []byte("base\n"), 0o644))
	fixture.runGit(t, fixture.repo, "add", "README.md")
	fixture.runGit(t, fixture.repo, "commit", "-m", "base")
	fixture.runGit(t, fixture.repo, "remote", "add", "origin", fixture.origin)
	fixture.runGit(t, fixture.repo, "push", "origin", "HEAD:"+realGitBranch)
	return fixture
}

func (f realGitFixture) configureUser(t *testing.T, repo string) {
	t.Helper()
	f.runGit(t, repo, "config", "user.name", "Auto Improve Tests")
	f.runGit(t, repo, "config", "user.email", "auto-improve-tests@example.invalid")
}

func (f realGitFixture) commit(t *testing.T, repo, name, body, message string) string {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(repo, name), []byte(body), 0o644))
	f.runGit(t, repo, "add", name)
	f.runGit(t, repo, "commit", "-m", message)
	return f.revParse(t, repo, "HEAD")
}

func (f realGitFixture) revParse(t *testing.T, repo, rev string) string {
	t.Helper()
	return strings.TrimSpace(f.runGit(t, repo, "rev-parse", rev))
}

func (f realGitFixture) runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(f.git, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %s\n%s", strings.Join(args, " "), string(out))
	return string(out)
}
