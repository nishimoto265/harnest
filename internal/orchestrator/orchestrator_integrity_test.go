package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/nishimoto265/harnest/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadRunContext_RejectsTaskPackageRunIDMismatch(t *testing.T) {
	runsBase := t.TempDir()
	worktreeBase := t.TempDir()
	runCtx, err := internalio.NewRunContext("2026-04-21-PR83-abcdef0", runsBase, worktreeBase)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(runCtx.RunDir(), 0o755))

	pkg := stubTaskPackageForRun(runCtx, 83)
	pkg.RunID = "2026-04-21-PR84-badcafe"
	require.NoError(t, internalio.WriteJSONAtomic(runCtx.TaskPackagePath(), pkg))

	_, err = loadRunContext(runCtx.RunID, runsBase, worktreeBase)
	require.ErrorContains(t, err, "task package run_id mismatch")
}

func TestLoadRunContext_RejectsCandidatesRunIDMismatch(t *testing.T) {
	runsBase := t.TempDir()
	worktreeBase := t.TempDir()
	runCtx, err := internalio.NewRunContext("2026-04-21-PR84-abcdef0", runsBase, worktreeBase)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(runCtx.RunDir(), 0o755))

	pkg := stubTaskPackageForRun(runCtx, 84)
	require.NoError(t, internalio.WriteJSONAtomic(runCtx.TaskPackagePath(), pkg))

	candidatesPath, err := runCtx.ResolveRunRelative("40/candidates.json")
	require.NoError(t, err)
	candidates := forcedCandidate("2026-04-21-PR85-badcafe")
	require.NoError(t, internalio.WriteJSONAtomic(candidatesPath, candidates))

	_, err = loadRunContext(runCtx.RunID, runsBase, worktreeBase)
	require.ErrorContains(t, err, "candidates run_id mismatch")
}

func TestLoadRunContext_RejectsIntentionRunIDMismatch(t *testing.T) {
	runsBase := t.TempDir()
	worktreeBase := t.TempDir()
	runCtx, err := internalio.NewRunContext("2026-04-21-PR86-abcdef0", runsBase, worktreeBase)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(runCtx.RunDir(), 0o755))

	pkg := stubTaskPackageForRun(runCtx, 86)
	require.NoError(t, internalio.WriteJSONAtomic(runCtx.TaskPackagePath(), pkg))

	store := NewIntentionStore(runCtx)
	intention := validPlanningIntention("2026-04-21-PR87-badcafe")
	intention.RunID = "2026-04-21-PR87-badcafe"
	require.NoError(t, internalio.WriteJSONAtomic(filepath.Join(runCtx.RunDir(), "70", "intention.json"), intention))

	_, err = loadRunContext(runCtx.RunID, runsBase, worktreeBase)
	require.ErrorContains(t, err, "intention run_id mismatch")

	loaded, loadErr := store.Load()
	require.ErrorContains(t, loadErr, "run_id mismatch")
	assert.Nil(t, loaded)
}

func TestNewFreshSelection_RejectsRunIDPRMismatch(t *testing.T) {
	runsBase := t.TempDir()
	worktreeBase := t.TempDir()

	_, err := newFreshSelection(105, RunOptions{RunID: "2026-04-21-PR104-abcdef0"}, runsBase, worktreeBase)
	require.ErrorContains(t, err, "run_id PR mismatch")
}

func TestNewFreshSelection_RejectsCompletedRunIDReuse(t *testing.T) {
	runsBase := t.TempDir()
	worktreeBase := t.TempDir()
	runCtx, err := internalio.NewRunContext("2026-04-21-PR105-abcdef0", runsBase, worktreeBase)
	require.NoError(t, err)
	require.NoError(t, state.Append(runCtx, completedEntry(105, runCtx.RunID, contracts.FailedStep70, time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC))))

	_, err = newFreshSelection(105, RunOptions{RunID: runCtx.RunID}, runsBase, worktreeBase)
	require.ErrorContains(t, err, "terminal state")
}

func TestNewFreshSelection_RejectsNonEmptyRunDir(t *testing.T) {
	runsBase := t.TempDir()
	worktreeBase := t.TempDir()
	runCtx, err := internalio.NewRunContext("2026-04-21-PR110-abcdef0", runsBase, worktreeBase)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(runCtx.RunDir(), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(runCtx.RunDir(), "stale.txt"), []byte("stale\n"), 0o644))

	_, err = newFreshSelection(110, RunOptions{RunID: runCtx.RunID}, runsBase, worktreeBase)
	require.ErrorContains(t, err, "empty run dir")
}

func TestRun_RejectsSymlinkedRunStepDir(t *testing.T) {
	cfg := testConfig(t)
	runID := contracts.RunID("2026-04-21-PR111-abcdef0")
	runCtx, err := internalio.NewRunContext(runID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	require.NoError(t, seedResumeRun(t, runCtx, 111))

	escapeDir := filepath.Join(t.TempDir(), "escape")
	require.NoError(t, os.MkdirAll(escapeDir, 0o755))
	require.NoError(t, os.RemoveAll(filepath.Join(runCtx.RunDir(), "70")))
	require.NoError(t, os.Symlink(escapeDir, filepath.Join(runCtx.RunDir(), "70")))

	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)
	orch.steps = stubPipelineSteps(nil, nil)

	err = orch.Run(context.Background(), 111, RunOptions{RunID: runID})
	require.Error(t, err)
	assert.NoFileExists(t, filepath.Join(escapeDir, "decision.json"))
}
