package implementrescue

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nishimoto265/harnest/internal/steps/agentrunner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteCommitBundle_WritesFinalBundleAtomically(t *testing.T) {
	rescueDir := t.TempDir()
	finalBundlePath := filepath.Join(rescueDir, "commits.bundle")

	var gitTarget string
	commitCount, bundleMode, err := WriteCommitBundle(
		context.Background(),
		"/repo",
		rescueDir,
		strings.Repeat("a", 40),
		func(context.Context, string, ...string) ([]byte, error) {
			return []byte("commit-sha\n"), nil
		},
		func(_ context.Context, _ string, args ...string) error {
			require.Len(t, args, 4)
			require.Equal(t, []string{"bundle", "create"}, args[:2])
			gitTarget = args[2]
			require.NotEqual(t, finalBundlePath, gitTarget)
			return os.WriteFile(gitTarget, []byte("bundle data\n"), 0o600)
		},
	)

	require.NoError(t, err)
	assert.Equal(t, 1, commitCount)
	assert.Equal(t, agentrunner.RescueBundleModeRange, bundleMode)
	assert.NotEmpty(t, gitTarget)
	assert.FileExists(t, finalBundlePath)
	data, readErr := os.ReadFile(finalBundlePath)
	require.NoError(t, readErr)
	assert.Equal(t, "bundle data\n", string(data))
}

func TestWriteCommitBundle_RejectsSymlinkRescueDir(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	rescueDir := filepath.Join(root, "rescued-link")
	require.NoError(t, os.Symlink(outside, rescueDir))

	_, _, err := WriteCommitBundle(
		context.Background(),
		"/repo",
		rescueDir,
		strings.Repeat("a", 40),
		func(context.Context, string, ...string) ([]byte, error) {
			return []byte("commit-sha\n"), nil
		},
		func(_ context.Context, _ string, args ...string) error {
			if len(args) < 3 {
				return fmt.Errorf("unexpected args: %v", args)
			}
			return os.WriteFile(args[2], []byte("bundle data\n"), 0o600)
		},
	)

	require.Error(t, err)
	assert.NoFileExists(t, filepath.Join(outside, "commits.bundle"))
}
