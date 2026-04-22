package step20_implement

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/steps/agentrunner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunner_CleanupErrorOnSuccessPathFailsClosed covers M5. If
// cleanupProcessTree returns an error after cmd.Wait succeeded, the pre-fix
// runner discarded it with `_ = ...` and returned a clean success, which
// caused a success manifest to be written even though descendants might have
// survived. Post-fix the success path is failed-closed by default.
func TestRunner_CleanupErrorOnSuccessPathFailsClosed(t *testing.T) {
	// Sanity check: fail-closed is the default.
	require.True(t, cleanupProcessTreeFailClosed,
		"M5: fail-closed must be the production default")

	origCleanup := cleanupProcessTree
	cleanupProcessTree = func(lease agentrunner.ProcessLease, rootPID int, tracker *agentrunner.DescendantTracker) error {
		return errors.New("injected-cleanup-fail")
	}
	t.Cleanup(func() { cleanupProcessTree = origCleanup })

	fx := newTestFixture(t, 10)
	// Normal success config — agent writes a file AND commits it.
	t.Setenv("FAKE_CLAUDE_WRITE_FILE", filepath.Join(fx.worktree, "ok.txt"))
	t.Setenv("FAKE_CLAUDE_COMMIT", "1")

	err := fx.step.Run(context.Background(), fx.run)
	require.Error(t, err, "success path must surface cleanup error rather than silently writing success manifest")
	require.Contains(t, err.Error(), "cleanup process tree after success")
	assert.NoFileExists(t, fx.manifestPath())
}

// TestStepRun_UncommittedOutputDoesNotWriteSuccessManifest covers H3. When
// the agent writes files, exits 0, but never runs `git commit`, HEAD still
// equals BaseSHA. The pre-fix success path wrote a success manifest with a
// diff that captured the working-tree changes — effectively adopting an
// unstaged, never-committed tree. Post-fix the step refuses: no success
// manifest, operators see an error manifest instead.
func TestStepRun_UncommittedOutputDoesNotWriteSuccessManifest(t *testing.T) {
	fx := newTestFixture(t, 30)
	// fake-claude.sh honors FAKE_CLAUDE_WRITE_FILE to write a file, but
	// FAKE_CLAUDE_COMMIT unset means no git commit is issued.
	t.Setenv("FAKE_CLAUDE_WRITE_FILE", filepath.Join(fx.worktree, "uncommitted.txt"))
	// Explicitly ensure no commit occurs.
	t.Setenv("FAKE_CLAUDE_COMMIT", "")

	err := fx.step.Run(context.Background(), fx.run)
	require.NoError(t, err)

	// Manifest must exist and be an Error, not a Success.
	path := fx.manifestPath()
	data, rerr := os.ReadFile(path)
	require.NoError(t, rerr)
	content := string(data)
	require.Contains(t, content, `"kind":"error"`,
		"uncommitted-output run must produce an error manifest; got: %s", content)
	require.NotContains(t, content, `"kind":"success"`)
}

// TestRescue_TrackedPatchOverflowEscalatesManualRecovery covers H4. Without
// the streaming/cap fix, git diff HEAD output with a 48MB file would read into
// memory unbounded and eventually OOM. With the fix the writer returns
// ErrRescuePatchOverflow, which is escalated to a ManualRecoveryRequiredError
// so operators inspect the worktree.
func TestRescue_TrackedPatchOverflowEscalatesManualRecovery(t *testing.T) {
	fx := newTestFixture(t, 30)
	stubQuiescentRescueWorktree(t)
	allocation, err := worktreeFor(fx.run.TaskPackage, 1, "a1")
	require.NoError(t, err)

	// Write a large text file that is tracked but dirty — git diff HEAD
	// will produce > 32 MiB of patch output because each byte of the file
	// will appear in the diff.
	size := maxRescuePatchBytes + (8 << 20)
	rng := make([]byte, size)
	for i := range rng {
		// Keep bytes printable ASCII so git treats the file as text and
		// emits a full line-by-line diff rather than a "Binary files
		// differ" placeholder.
		rng[i] = byte(33 + (i % 90))
		if i%80 == 79 {
			rng[i] = '\n'
		}
	}
	require.NoError(t, os.WriteFile(filepath.Join(fx.worktree, "huge.txt"), rng, 0o644))
	runGit(t, fx.worktree, "add", "huge.txt")
	runGit(t, fx.worktree, "commit", "-m", "huge staged blob")

	for i := range rng {
		rng[i] = byte(33 + ((i * 7) % 90))
		if i%80 == 79 {
			rng[i] = '\n'
		}
	}
	require.NoError(t, os.WriteFile(filepath.Join(fx.worktree, "huge.txt"), rng, 0o644))

	_, err = fx.step.performRescue(context.Background(), fx.run, allocation, fx.agentDir, resumeState{
		ExpectedBaseSHA: fx.baseSHA,
		StartedAt:       time.Now().Add(-2 * time.Hour).UTC(),
		Pid:             999999,
		RetryCount:      0,
		LastHeartbeat:   time.Now().Add(-2 * time.Hour).UTC(),
		LeaderStartTime: "0",
	})
	require.Error(t, err)

	// The failure must be escalated to manual recovery; we must not leave a
	// silent partial rescue dir claiming success.
	var manual *agentrunner.ManualRecoveryRequiredError
	if errors.As(err, &manual) {
		// Detail message should mention the overflow so operators know
		// what happened.
		require.Contains(t, strings.ToLower(manual.Detail), "exceed", "manual recovery detail must explain overflow: %v", manual.Detail)
	} else {
		// Alternatively the raw overflow error may bubble up.
		require.ErrorIs(t, err, ErrRescuePatchOverflow)
	}
}

// TestResumeState_ActiveLeaseRequiresLeaderStartTime covers M6. Without the
// fix a persisted active lease with blank leader_start_time passed Validate,
// allowing a stale lease to masquerade as current.
func TestResumeState_ActiveLeaseRequiresLeaderStartTime(t *testing.T) {
	now := time.Now().UTC()
	state := resumeState{
		ExpectedBaseSHA: "0000000000000000000000000000000000000000",
		StartedAt:       now,
		Pid:             42,
		Pgid:            42,
		LeaderStartTime: "",
		LastHeartbeat:   now,
	}
	err := state.Validate()
	require.Error(t, err, "Validate must reject active lease without leader_start_time")
	require.Contains(t, err.Error(), "leader_start_time")

	state.LeaderStartTime = "12345"
	require.NoError(t, state.Validate(), "fully-populated active lease must pass")
}

// TestStepRun_RejectsTaskPackageRunIDMismatch covers M3. Without the guard,
// a TaskPackage with the wrong run_id would write artifacts into the active
// RunContext's run dir, cross-contaminating the manifest stream.
func TestStepRun_RejectsTaskPackageRunIDMismatch(t *testing.T) {
	fx := newTestFixture(t, 5)
	// Alter the TaskPackage RunID after fixture construction so it
	// disagrees with the IO context.
	other := contracts.RunID("2026-04-22-PR99-deadbee")
	fx.run.TaskPackage.RunID = other

	err := fx.step.Run(context.Background(), fx.run)
	require.Error(t, err)
	require.Contains(t, err.Error(), "run_id mismatch")
	assert.NoFileExists(t, fx.manifestPath())
}
