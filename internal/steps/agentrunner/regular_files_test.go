package agentrunner

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenValidatedRegularFileRejectsSymlinkParent(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret\n"), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "linked")))

	file, _, _, err := OpenValidatedRegularFile(filepath.Join(root, "linked", "secret.txt"))

	require.Error(t, err)
	require.Nil(t, file)
}
