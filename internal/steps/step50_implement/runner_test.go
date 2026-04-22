package step50_implement

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/steps/agentrunner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStepRun_CleanupFailureWritesErrorManifest(t *testing.T) {
	t.Setenv("FAKE_RUN_ID", "2026-04-21-PR42-abcdef0")
	t.Setenv("FAKE_AGENT", "a1")

	env := newStepTestEnv(t, "fake-claude-success.sh", 30)

	originalCleanup := cleanupProcessTree
	cleanupProcessTree = func(lease agentrunner.ProcessLease, sessionID int, tracker *agentrunner.DescendantTracker) error {
		return errors.New("cleanup failed")
	}
	t.Cleanup(func() {
		cleanupProcessTree = originalCleanup
	})

	err := (Step{}).Run(context.Background(), env.run)
	require.NoError(t, err)

	manifest := readManifest(t, env.manifestPath)
	assert.Equal(t, contracts.ManifestKindError, manifest.Kind)
	failure := manifest.Value.(contracts.ManifestError)
	assert.Contains(t, failure.Detail, "cleanup failed")
	assert.NoFileExists(t, filepath.Join(env.run.IO.RunDir(), "50-pass2", "a1", "diff.patch"))
}
