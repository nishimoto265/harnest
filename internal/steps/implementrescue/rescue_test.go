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
	"github.com/nishimoto265/auto-improve/internal/steps/rescuetest"
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
	assert.ErrorContains(t, err, "implementrescue: perform missing Quiesce")
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

func TestFindExistingDir_RequiresCompleteCaptureCoverage(t *testing.T) {
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

	selected, adopted, err := FindExistingDir(agentDir, "rescued", state.ExpectedBaseSHA, 1, state.RescuedHeadSHA, state.DirtyFingerprint, nil, func(string, agentrunner.RescueStateFile) error {
		return nil
	})
	require.NoError(t, err)
	assert.False(t, adopted)
	assert.Empty(t, selected)

	artifacts, err := rescuetest.WriteCompleteCaptureArtifacts(rescueDir, testFileDigest)
	require.NoError(t, err)
	state.Artifacts = artifacts
	require.NoError(t, agentrunner.WriteRescueState(filepath.Join(rescueDir, "state.json"), state))

	selected, adopted, err = FindExistingDir(agentDir, "rescued", state.ExpectedBaseSHA, 1, state.RescuedHeadSHA, state.DirtyFingerprint, nil, func(string, agentrunner.RescueStateFile) error {
		return nil
	})
	require.NoError(t, err)
	assert.True(t, adopted)
	assert.Equal(t, rescueDir, selected)
}

func TestFindExistingDir_RequiresCurrentUntrackedCoverage(t *testing.T) {
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
	}
	artifacts, err := rescuetest.WriteCompleteCaptureArtifacts(rescueDir, testFileDigest)
	require.NoError(t, err)
	state.Artifacts = artifacts
	require.NoError(t, agentrunner.WriteRescueState(filepath.Join(rescueDir, "state.json"), state))

	dirtyEntries := []string{"?? dirty.txt"}
	selected, adopted, err := FindExistingDir(agentDir, "rescued", state.ExpectedBaseSHA, 1, state.RescuedHeadSHA, state.DirtyFingerprint, dirtyEntries, func(candidateDir string, state agentrunner.RescueStateFile) error {
		return agentrunner.VerifyRescueStateFile(candidateDir, state, testFileDigest, "test")
	})
	require.NoError(t, err)
	assert.False(t, adopted)
	assert.Empty(t, selected)

	artifacts, err = rescuetest.WriteCompleteCaptureArtifacts(rescueDir, testFileDigest, "untracked/dirty.txt")
	require.NoError(t, err)
	state.Artifacts = artifacts
	require.NoError(t, agentrunner.WriteRescueState(filepath.Join(rescueDir, "state.json"), state))

	selected, adopted, err = FindExistingDir(agentDir, "rescued", state.ExpectedBaseSHA, 1, state.RescuedHeadSHA, state.DirtyFingerprint, dirtyEntries, func(candidateDir string, state agentrunner.RescueStateFile) error {
		return agentrunner.VerifyRescueStateFile(candidateDir, state, testFileDigest, "test")
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
	artifacts, err := rescuetest.WriteCompleteCaptureArtifacts(rescueDir, testFileDigest)
	require.NoError(t, err)
	artifacts[0].SHA256 = strings.Repeat("0", 64)

	state := agentrunner.RescueStateFile{
		ExpectedBaseSHA:  "1111111111111111111111111111111111111111",
		RescuedHeadSHA:   "2222222222222222222222222222222222222222",
		RetryCount:       1,
		CommitCount:      0,
		BundleMode:       agentrunner.RescueBundleModeNone,
		CreatedAt:        time.Now().UTC(),
		DirtyFingerprint: "dirty-fingerprint",
		Artifacts:        artifacts,
	}
	require.NoError(t, agentrunner.WriteRescueState(filepath.Join(rescueDir, "state.json"), state))

	selected, adopted, err := FindExistingDir(agentDir, "rescued", state.ExpectedBaseSHA, 1, state.RescuedHeadSHA, state.DirtyFingerprint, nil, func(candidateDir string, state agentrunner.RescueStateFile) error {
		return agentrunner.VerifyRescueStateFile(candidateDir, state, testFileDigest, "test")
	})
	require.NoError(t, err)
	assert.False(t, adopted)
	assert.Empty(t, selected)
}

func TestFindExistingDir_SkipsMalformedStateJSON(t *testing.T) {
	agentDir := t.TempDir()
	rescueRoot := filepath.Join(agentDir, "rescued")
	badDir := filepath.Join(rescueRoot, "bad-candidate")
	validDir := filepath.Join(rescueRoot, "valid-candidate")
	require.NoError(t, os.MkdirAll(badDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(badDir, "state.json"), []byte("{"), 0o644))
	require.NoError(t, os.MkdirAll(validDir, 0o755))

	state := agentrunner.RescueStateFile{
		ExpectedBaseSHA:  "1111111111111111111111111111111111111111",
		RescuedHeadSHA:   "2222222222222222222222222222222222222222",
		RetryCount:       1,
		CommitCount:      0,
		BundleMode:       agentrunner.RescueBundleModeNone,
		CreatedAt:        time.Now().UTC(),
		DirtyFingerprint: "dirty-fingerprint",
	}
	artifacts, err := rescuetest.WriteCompleteCaptureArtifacts(validDir, testFileDigest)
	require.NoError(t, err)
	state.Artifacts = artifacts
	require.NoError(t, agentrunner.WriteRescueState(filepath.Join(validDir, "state.json"), state))

	selected, adopted, err := FindExistingDir(agentDir, "rescued", state.ExpectedBaseSHA, 1, state.RescuedHeadSHA, state.DirtyFingerprint, nil, func(candidateDir string, state agentrunner.RescueStateFile) error {
		return agentrunner.VerifyRescueStateFile(candidateDir, state, testFileDigest, "test")
	})
	require.NoError(t, err)
	assert.True(t, adopted)
	assert.Equal(t, validDir, selected)
}

func TestFindExistingDirRejectsSymlinkRescueRoot(t *testing.T) {
	agentDir := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(agentDir, "rescued")))

	selected, adopted, err := FindExistingDir(agentDir, "rescued", strings.Repeat("1", 40), 1, strings.Repeat("2", 40), "dirty", nil, func(string, agentrunner.RescueStateFile) error {
		return nil
	})

	require.Error(t, err)
	assert.False(t, adopted)
	assert.Empty(t, selected)
}

func TestCreateFreshRescueDirRejectsSymlinkRescueRoot(t *testing.T) {
	agentDir := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(agentDir, "rescued")))

	rescueDir, err := createFreshRescueDir(agentDir, "rescued", "candidate")

	require.Error(t, err)
	assert.Empty(t, rescueDir)
	assert.NoDirExists(t, filepath.Join(outside, "candidate"))
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
