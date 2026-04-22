package step20_implement

import (
	"context"
	"errors"
	"testing"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/steps/agentrunner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStepRun_CleanupFailureWritesErrorManifest(t *testing.T) {
	fx := newTestFixture(t, 5)
	t.Setenv("FAKE_CLAUDE_WRITE_FILE", fx.worktree+"/cleanup-failure.txt")

	originalCleanup := cleanupProcessTree
	cleanupProcessTree = func(lease agentrunner.ProcessLease, sessionID int, tracker *agentrunner.DescendantTracker) error {
		return errors.New("cleanup failed")
	}
	t.Cleanup(func() {
		cleanupProcessTree = originalCleanup
	})

	err := fx.step.Run(context.Background(), fx.run)
	require.NoError(t, err)

	manifest := fx.readManifest(t)
	assert.Equal(t, contracts.ManifestKindError, manifest.Kind)
	failure := manifest.Value.(contracts.ManifestError)
	assert.Contains(t, failure.Detail, "cleanup failed")
	assert.NoFileExists(t, fx.diffPath())
}
