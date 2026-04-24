package gitremote

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseGitHubRemoteAcceptsGitHubHTTPSAndSSH(t *testing.T) {
	for _, remoteURL := range []string{
		"https://github.com/owner/repo.git",
		"git@github.com:owner/repo.git",
		"ssh://git@github.com/owner/repo.git",
	} {
		t.Run(remoteURL, func(t *testing.T) {
			info, err := ParseGitHubRemote(remoteURL, nil)

			require.NoError(t, err)
			assert.Equal(t, "github.com", info.Host)
			assert.Equal(t, "owner/repo", info.Slug)
		})
	}
}

func TestParseGitHubRemoteRejectsSameSlugWrongHost(t *testing.T) {
	_, err := ParseGitHubRemote("https://evil.example.com/owner/repo.git", nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not an allowed GitHub host")
}

func TestParseGitHubRemoteAllowsConfiguredGHHost(t *testing.T) {
	info, err := ParseGitHubRemote("https://github.example.com/owner/repo.git", []string{"github.example.com"})

	require.NoError(t, err)
	assert.Equal(t, "github.example.com", info.Host)
	assert.Equal(t, "owner/repo", info.Slug)
}

func TestParseGitHubRemoteRejectsSchemeLessLocalPathStyleRemote(t *testing.T) {
	for _, remoteURL := range []string{
		"github.com/owner/repo",
		"github.example.com/owner/repo",
	} {
		t.Run(remoteURL, func(t *testing.T) {
			_, err := ParseGitHubRemote(remoteURL, []string{"github.example.com"})

			require.Error(t, err)
			assert.Contains(t, err.Error(), "could not parse GitHub remote url")
		})
	}
}
