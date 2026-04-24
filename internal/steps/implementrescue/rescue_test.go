package implementrescue

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResumeIfNeeded_RejectsMissingCallbacks(t *testing.T) {
	_, err := ResumeIfNeeded(context.Background(), ResumeOptions{
		StepName: "step20",
		AgentDir: t.TempDir(),
	})

	require.Error(t, err)
	assert.ErrorContains(t, err, "implementrescue: resume missing LoadState")
}

func TestPerform_RejectsMissingCallbacks(t *testing.T) {
	_, err := Perform(context.Background(), PerformOptions{
		StepName:       "step20",
		RunID:          "2026-04-21-PR1-abcdef0",
		AgentDir:       t.TempDir(),
		RescuedDirName: "rescued",
	})

	require.Error(t, err)
	assert.ErrorContains(t, err, "implementrescue: perform missing EnsureDir")
}

func TestWriteIgnoredList_QuotesEntries(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "ignored.txt")
	err := WriteIgnoredList(context.Background(), "/repo", dest, func(context.Context, string, ...string) ([]byte, error) {
		return []byte("plain.txt\x00dir/line\nbreak.txt\x00"), nil
	})
	require.NoError(t, err)

	data, err := os.ReadFile(dest)
	require.NoError(t, err)
	lines := strings.Split(string(data), "\n")
	require.Len(t, lines, 2)

	first, err := strconv.Unquote(lines[0])
	require.NoError(t, err)
	second, err := strconv.Unquote(lines[1])
	require.NoError(t, err)
	assert.Equal(t, "plain.txt", first)
	assert.Equal(t, "dir/line\nbreak.txt", second)
}
