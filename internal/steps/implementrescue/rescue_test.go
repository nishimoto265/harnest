package implementrescue

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/steps/agentrunner"
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

func TestFindExistingDir_RequiresIgnoredCoverage(t *testing.T) {
	agentDir := t.TempDir()
	rescueRoot := filepath.Join(agentDir, "rescued")
	rescueDir := filepath.Join(rescueRoot, "candidate")
	require.NoError(t, os.MkdirAll(rescueDir, 0o755))

	state := agentrunner.RescueStateFile{
		ExpectedBaseSHA:  "1111111111111111111111111111111111111111",
		RescuedHeadSHA:   "2222222222222222222222222222222222222222",
		RetryCount:       1,
		CommitCount:      0,
		BundleMode:       agentrunner.RescueBundleModeNone,
		CreatedAt:        time.Now().UTC(),
		DirtyFingerprint: "dirty-fingerprint",
		Artifacts: []agentrunner.RescueArtifactDigest{{
			Path:   "ignored.txt",
			SHA256: strings.Repeat("a", 64),
		}},
	}
	require.NoError(t, agentrunner.WriteRescueState(filepath.Join(rescueDir, "state.json"), state))

	selected, adopted, err := FindExistingDir(agentDir, "rescued", state.ExpectedBaseSHA, 1, state.RescuedHeadSHA, state.DirtyFingerprint, func(string) error {
		return nil
	})
	require.NoError(t, err)
	assert.False(t, adopted)
	assert.Empty(t, selected)

	state.Artifacts = append(state.Artifacts, agentrunner.RescueArtifactDigest{
		Path:   "ignored-skipped.txt",
		SHA256: strings.Repeat("b", 64),
	})
	require.NoError(t, agentrunner.WriteRescueState(filepath.Join(rescueDir, "state.json"), state))

	selected, adopted, err = FindExistingDir(agentDir, "rescued", state.ExpectedBaseSHA, 1, state.RescuedHeadSHA, state.DirtyFingerprint, func(string) error {
		return nil
	})
	require.NoError(t, err)
	assert.True(t, adopted)
	assert.Equal(t, rescueDir, selected)
}

func TestFindExistingDir_SkipsCorruptRescueState(t *testing.T) {
	agentDir := t.TempDir()
	rescueRoot := filepath.Join(agentDir, "rescued")
	rescueDir := filepath.Join(rescueRoot, "candidate")
	require.NoError(t, os.MkdirAll(rescueDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(rescueDir, "ignored.txt"), nil, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(rescueDir, "ignored-skipped.txt"), nil, 0o644))

	state := agentrunner.RescueStateFile{
		ExpectedBaseSHA:  "1111111111111111111111111111111111111111",
		RescuedHeadSHA:   "2222222222222222222222222222222222222222",
		RetryCount:       1,
		CommitCount:      0,
		BundleMode:       agentrunner.RescueBundleModeNone,
		CreatedAt:        time.Now().UTC(),
		DirtyFingerprint: "dirty-fingerprint",
		Artifacts: []agentrunner.RescueArtifactDigest{
			{Path: "ignored.txt", SHA256: strings.Repeat("0", 64)},
			{Path: "ignored-skipped.txt", SHA256: strings.Repeat("0", 64)},
		},
	}
	require.NoError(t, agentrunner.WriteRescueState(filepath.Join(rescueDir, "state.json"), state))

	selected, adopted, err := FindExistingDir(agentDir, "rescued", state.ExpectedBaseSHA, 1, state.RescuedHeadSHA, state.DirtyFingerprint, func(candidateDir string) error {
		return agentrunner.VerifyRescueState(candidateDir, testFileDigest, "test")
	})
	require.NoError(t, err)
	assert.False(t, adopted)
	assert.Empty(t, selected)
}

func testFileDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
