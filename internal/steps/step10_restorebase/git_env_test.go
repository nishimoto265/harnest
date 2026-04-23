package step10restorebase

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGitClient_RepoSlugParsesOriginURL verifies that repo discovery uses the
// local git remote and returns the owner/name slug step10 should pass to gh.
func TestGitClient_RepoSlugParsesOriginURL(t *testing.T) {
	repoRoot := "/tmp/auto-improve-r17-fake-repo"

	git := gitCLI{
		run: func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
			if name != "git" {
				return nil, nil, fmt.Errorf("unexpected binary: %s", name)
			}
			if len(args) == 5 && args[0] == "-C" && args[1] == repoRoot && args[2] == "remote" && args[3] == "get-url" && args[4] == "origin" {
				return []byte("git@github.com:owner/repo.git\n"), nil, nil
			}
			return nil, nil, fmt.Errorf("unexpected git args: %v", args)
		},
	}

	slug, err := git.RepoSlug(context.Background(), repoRoot)
	require.NoError(t, err)
	assert.Equal(t, "owner/repo", slug)
}
