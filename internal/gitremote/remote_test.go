package gitremote

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseGitHubRemoteAcceptsGitHubHTTPSAndSSH(t *testing.T) {
	tests := []struct {
		remoteURL string
		scheme    string
	}{
		{remoteURL: "https://github.com/owner/repo.git", scheme: "https"},
		{remoteURL: "git@github.com:owner/repo.git", scheme: "ssh"},
		{remoteURL: "ssh://git@github.com/owner/repo.git", scheme: "ssh"},
		{remoteURL: "ssh://git@github.com:22/owner/repo.git", scheme: "ssh"},
	}
	for _, tt := range tests {
		t.Run(tt.remoteURL, func(t *testing.T) {
			info, err := ParseGitHubRemote(tt.remoteURL, nil)

			require.NoError(t, err)
			assert.Equal(t, tt.scheme, info.Scheme)
			assert.Equal(t, "github.com", info.Host)
			assert.Equal(t, "owner/repo", info.Slug)
		})
	}
}

func TestParseGitHubRemoteRejectsUnsupportedSchemes(t *testing.T) {
	for _, remoteURL := range []string{
		"git://github.com/owner/repo.git",
		"file://github.com/owner/repo.git",
		"http://github.com/owner/repo.git",
	} {
		t.Run(remoteURL, func(t *testing.T) {
			_, err := ParseGitHubRemote(remoteURL, nil)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "unsupported GitHub remote URL scheme")
			assert.Contains(t, err.Error(), "supported schemes are https and ssh")
		})
	}
}

func TestParseGitHubRemoteRejectsCredentialsQueryAndFragments(t *testing.T) {
	tests := []struct {
		name      string
		remoteURL string
		want      string
	}{
		{
			name:      "https credentials",
			remoteURL: "https://token@github.com/owner/repo.git",
			want:      "must not include credentials",
		},
		{
			name:      "ssh password",
			remoteURL: "ssh://git:secret@github.com/owner/repo.git",
			want:      "must not include a password",
		},
		{
			name:      "ssh non git user",
			remoteURL: "ssh://deploy@github.com/owner/repo.git",
			want:      "user must be git",
		},
		{
			name:      "scp non git user",
			remoteURL: "deploy@github.com:owner/repo.git",
			want:      "user must be git",
		},
		{
			name:      "query",
			remoteURL: "https://github.com/owner/repo.git?token=secret",
			want:      "must not contain query strings or fragments",
		},
		{
			name:      "fragment",
			remoteURL: "https://github.com/owner/repo.git#secret",
			want:      "must not contain query strings or fragments",
		},
		{
			name:      "scp query",
			remoteURL: "git@github.com:owner/repo.git?token=secret",
			want:      "must not contain query strings or fragments",
		},
		{
			name:      "scp fragment",
			remoteURL: "git@github.com:owner/repo.git#secret",
			want:      "must not contain query strings or fragments",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseGitHubRemote(tt.remoteURL, nil)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
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

func TestParseGitHubRemoteAllowsConfiguredGHHostWithDefaultPorts(t *testing.T) {
	tests := []struct {
		name      string
		remoteURL string
		env       []string
	}{
		{
			name:      "https",
			remoteURL: "https://github.example.com:443/owner/repo.git",
			env:       []string{"GH_HOST=github.example.com:443"},
		},
		{
			name:      "ssh",
			remoteURL: "ssh://git@github.example.com:22/owner/repo.git",
			env:       []string{"GH_HOST=github.example.com:22"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, err := ParseGitHubRemote(tt.remoteURL, AllowedGitHubHostsFromEnv(tt.env))

			require.NoError(t, err)
			assert.Equal(t, "github.example.com", info.Host)
			assert.Equal(t, "owner/repo", info.Slug)
		})
	}
}

func TestParseGitHubRemotePreservesNonDefaultPorts(t *testing.T) {
	info, err := ParseGitHubRemote("https://github.example.com:8443/owner/repo.git", []string{"github.example.com:8443"})

	require.NoError(t, err)
	assert.Equal(t, "github.example.com:8443", info.Host)
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

func TestPreferredRemoteURLForAuthPrefersHTTPS(t *testing.T) {
	output := `
git@github.com:owner/repo.git
https://github.com/owner/repo.git
`

	assert.Equal(t, "https://github.com/owner/repo.git", PreferredRemoteURLForAuth(output))
}

func TestPreferredRemoteURLForAuthFallsBackToFirstRemote(t *testing.T) {
	output := `

git@github.com:owner/repo.git
ssh://git@github.com/owner/repo.git
`

	assert.Equal(t, "git@github.com:owner/repo.git", PreferredRemoteURLForAuth(output))
}

func TestCanonicalRemoteURL(t *testing.T) {
	tests := []struct {
		name string
		info Info
		want string
	}{
		{
			name: "https",
			info: Info{Scheme: "https", Host: "github.com", Slug: "owner/repo"},
			want: "https://github.com/owner/repo.git",
		},
		{
			name: "ssh",
			info: Info{Scheme: "ssh", Host: "github.example.com", Slug: "owner/repo"},
			want: "git@github.example.com:owner/repo.git",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, CanonicalRemoteURL(tt.info))
		})
	}
}
