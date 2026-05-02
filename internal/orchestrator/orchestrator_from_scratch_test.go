package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/processenv"
	"github.com/nishimoto265/auto-improve/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun_FromScratchSupersedesNonTerminalRunAndPrunesWorktrees(t *testing.T) {
	if _, err := processenv.TrustedLookPath("git"); err != nil {
		t.Skipf("git not available in trusted PATH: %v", err)
	}
	cfg := testConfig(t)
	cfg.Repo.Root = repoRootFromTestFile(t)
	oldRunID := contracts.RunID("2026-04-21-PR455-abcdef0")
	oldRunCtx, err := internalio.NewRunContext(oldRunID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(oldRunCtx.RunDir(), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(oldRunCtx.RunDir(), "config.snapshot.yaml"), []byte(
		"repo:\n"+
			"  root: "+cfg.Repo.Root+"\n"+
			"  default_branch: main\n"+
			"  best_branch: best\n"+
			"paths:\n"+
			"  runs: "+cfg.Paths.Runs+"\n"+
			"worktree:\n"+
			"  base: "+cfg.Worktree.Base+"\n",
	), 0o644))
	pkg := stubTaskPackageForRun(oldRunCtx, 455)
	require.NoError(t, internalio.WriteJSONAtomic(oldRunCtx.TaskPackagePath(), pkg))
	require.NoError(t, state.NewWriter(oldRunCtx).Append(startedEntry(455, oldRunID, time.Now().UTC())))
	require.NoError(t, state.NewWriter(oldRunCtx).Append(contracts.StateEntry{
		Kind: contracts.StateKindInterrupted,
		Value: contracts.StateEntryInterrupted{
			Kind:   contracts.StateKindInterrupted,
			PR:     455,
			RunID:  oldRunID,
			Step:   contracts.FailedStep20,
			Reason: contracts.InterruptedReasonUnknown,
			At:     time.Now().UTC(),
		},
	}))

	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)
	orch.steps = stubPipelineSteps(nil, stubStep70{})

	require.NoError(t, orch.Run(context.Background(), 455, RunOptions{FromScratch: true}))

	oldEvents, err := state.ScanEventsForRun(oldRunCtx, oldRunID)
	require.NoError(t, err)
	require.NotEmpty(t, oldEvents)
	lastOld := oldEvents[len(oldEvents)-1]
	assert.Equal(t, contracts.StateKindSkipped, lastOld.Kind)
	skipped, ok := lastOld.Value.(contracts.StateEntrySkipped)
	require.True(t, ok)
	assert.Equal(t, "superseded_by_from_scratch", skipped.Detail)
	for _, wt := range pkg.Worktrees {
		assert.NoDirExists(t, wt.Path)
	}

	latest, err := state.LatestRunForPR(oldRunCtx, 455)
	require.NoError(t, err)
	require.NotNil(t, latest.LastEvent)
	assert.NotEqual(t, oldRunID, latest.RunID)
	assert.Equal(t, state.NextActionFreshStart, latest.Action)
}

func TestRun_FromScratchCleanupFailureLeavesStartedReplacementRun(t *testing.T) {
	if _, err := processenv.TrustedLookPath("git"); err != nil {
		t.Skipf("git not available in trusted PATH: %v", err)
	}
	cfg := testConfig(t)
	repoRoot := filepath.Join(t.TempDir(), "invalid-repo")
	require.NoError(t, os.MkdirAll(filepath.Join(repoRoot, ".git"), 0o755))
	cfg.Repo.Root = repoRoot

	oldRunID := contracts.RunID("2026-04-21-PR457-abcdef0")
	oldRunCtx, err := internalio.NewRunContext(oldRunID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(oldRunCtx.RunDir(), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(oldRunCtx.RunDir(), "config.snapshot.yaml"), []byte(
		"repo:\n"+
			"  root: "+cfg.Repo.Root+"\n"+
			"  default_branch: main\n"+
			"  best_branch: best\n"+
			"paths:\n"+
			"  runs: "+cfg.Paths.Runs+"\n"+
			"worktree:\n"+
			"  base: "+cfg.Worktree.Base+"\n",
	), 0o644))
	pkg := stubTaskPackageForRun(oldRunCtx, 457)
	require.NoError(t, internalio.WriteJSONAtomic(oldRunCtx.TaskPackagePath(), pkg))
	for _, wt := range pkg.Worktrees {
		require.NoError(t, os.MkdirAll(wt.Path, 0o755))
	}
	require.NoError(t, state.NewWriter(oldRunCtx).Append(startedEntry(457, oldRunID, time.Now().UTC())))
	require.NoError(t, state.NewWriter(oldRunCtx).Append(contracts.StateEntry{
		Kind: contracts.StateKindInterrupted,
		Value: contracts.StateEntryInterrupted{
			Kind:   contracts.StateKindInterrupted,
			PR:     457,
			RunID:  oldRunID,
			Step:   contracts.FailedStep20,
			Reason: contracts.InterruptedReasonUnknown,
			At:     time.Now().UTC(),
		},
	}))

	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)
	orch.steps = stubPipelineSteps(nil, stubStep70{})

	err = orch.Run(context.Background(), 457, RunOptions{FromScratch: true})
	require.Error(t, err)

	oldEvents, err := state.ScanEventsForRun(oldRunCtx, oldRunID)
	require.NoError(t, err)
	require.NotEmpty(t, oldEvents)
	assert.Equal(t, contracts.StateKindSkipped, oldEvents[len(oldEvents)-1].Kind)
	assert.Contains(t, eventKinds(oldEvents), contracts.StateKindSkipped)
	for _, wt := range pkg.Worktrees {
		assert.DirExists(t, wt.Path)
	}

	latest, err := state.LatestRunForPR(oldRunCtx, 457)
	require.NoError(t, err)
	require.NotNil(t, latest.LastEvent)
	assert.Equal(t, state.NextActionResume, latest.Action)
	assert.Equal(t, contracts.FailedStep10, latest.Step)
	assert.NotEqual(t, oldRunID, latest.RunID)
	assert.Equal(t, contracts.StateKindStarted, latest.LastEvent.Kind)

	replacementCtx, err := internalio.NewRunContext(latest.RunID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	assert.DirExists(t, replacementCtx.RunDir())
	assert.FileExists(t, filepath.Join(replacementCtx.RunDir(), "config.snapshot.yaml"))
	replacementEvents, err := state.ScanEventsForRun(replacementCtx, latest.RunID)
	require.NoError(t, err)
	require.Len(t, replacementEvents, 1)
	assert.Equal(t, contracts.StateKindStarted, replacementEvents[0].Kind)

	targets, err := state.ResumeTargetPath(oldRunCtx.ProcessedPath())
	require.NoError(t, err)
	require.Len(t, targets, 1)
	assert.Equal(t, 457, targets[0].PR)
	assert.Equal(t, latest.RunID, targets[0].RunID)
	assert.Equal(t, contracts.FailedStep10, targets[0].Step)
}

func TestRun_FromScratchRefusesUnfinishedStep70(t *testing.T) {
	cfg := testConfig(t)
	oldRunID := contracts.RunID("2026-04-21-PR458-abcdef0")
	oldRunCtx, err := internalio.NewRunContext(oldRunID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(oldRunCtx.RunDir(), 0o755))
	pkg := stubTaskPackageForRun(oldRunCtx, 458)
	require.NoError(t, internalio.WriteJSONAtomic(oldRunCtx.TaskPackagePath(), pkg))
	for _, wt := range pkg.Worktrees {
		require.NoError(t, os.MkdirAll(wt.Path, 0o755))
	}
	require.NoError(t, state.NewWriter(oldRunCtx).Append(startedEntry(458, oldRunID, time.Now().UTC())))
	require.NoError(t, state.NewWriter(oldRunCtx).Append(contracts.StateEntry{
		Kind: contracts.StateKindInterrupted,
		Value: contracts.StateEntryInterrupted{
			Kind:   contracts.StateKindInterrupted,
			PR:     458,
			RunID:  oldRunID,
			Step:   contracts.FailedStep70,
			Reason: contracts.InterruptedReasonUnknown,
			At:     time.Now().UTC(),
		},
	}))

	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)
	orch.steps = stubPipelineSteps(nil, stubStep70{})

	err = orch.Run(context.Background(), 458, RunOptions{FromScratch: true})
	require.Error(t, err)
	assert.ErrorContains(t, err, "unfinished step70")

	oldEvents, err := state.ScanEventsForRun(oldRunCtx, oldRunID)
	require.NoError(t, err)
	assert.NotContains(t, eventKinds(oldEvents), contracts.StateKindSkipped)
	for _, wt := range pkg.Worktrees {
		assert.DirExists(t, wt.Path)
	}
}

func TestRun_FromScratchRefusesPersistedStep70Intention(t *testing.T) {
	cfg := testConfig(t)
	oldRunID := contracts.RunID("2026-04-21-PR459-abcdef0")
	oldRunCtx, err := internalio.NewRunContext(oldRunID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(oldRunCtx.RunDir(), 0o755))
	pkg := stubTaskPackageForRun(oldRunCtx, 459)
	require.NoError(t, internalio.WriteJSONAtomic(oldRunCtx.TaskPackagePath(), pkg))
	for _, wt := range pkg.Worktrees {
		require.NoError(t, os.MkdirAll(wt.Path, 0o755))
	}
	require.NoError(t, NewIntentionStore(oldRunCtx).Save(validPlanningIntention(oldRunID)))
	require.NoError(t, state.NewWriter(oldRunCtx).Append(startedEntry(459, oldRunID, time.Now().UTC())))
	require.NoError(t, state.NewWriter(oldRunCtx).Append(contracts.StateEntry{
		Kind: contracts.StateKindInterrupted,
		Value: contracts.StateEntryInterrupted{
			Kind:   contracts.StateKindInterrupted,
			PR:     459,
			RunID:  oldRunID,
			Step:   contracts.FailedStep20,
			Reason: contracts.InterruptedReasonUnknown,
			At:     time.Now().UTC(),
		},
	}))

	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)
	orch.steps = stubPipelineSteps(nil, stubStep70{})

	err = orch.Run(context.Background(), 459, RunOptions{FromScratch: true})
	require.Error(t, err)
	assert.ErrorContains(t, err, "persisted step70 intention")

	oldEvents, err := state.ScanEventsForRun(oldRunCtx, oldRunID)
	require.NoError(t, err)
	assert.NotContains(t, eventKinds(oldEvents), contracts.StateKindSkipped)
	for _, wt := range pkg.Worktrees {
		assert.DirExists(t, wt.Path)
	}
}

func TestRun_FromScratchPrunesRegisteredGitWorktrees(t *testing.T) {
	if _, err := processenv.TrustedLookPath("git"); err != nil {
		t.Skipf("git not available in trusted PATH: %v", err)
	}
	root := t.TempDir()
	repoRoot := filepath.Join(root, "repo")
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	require.NoError(t, os.MkdirAll(worktreeBase, 0o755))
	runGit(t, "", "init", "-b", "main", repoRoot)
	runGit(t, repoRoot, "config", "user.email", "auto-improve@example.test")
	runGit(t, repoRoot, "config", "user.name", "auto-improve test")
	require.NoError(t, os.WriteFile(filepath.Join(repoRoot, "README.md"), []byte("fixture\n"), 0o644))
	runGit(t, repoRoot, "add", "README.md")
	runGit(t, repoRoot, "commit", "-m", "initial")

	cfg := testConfig(t)
	cfg.Repo.Root = repoRoot
	cfg.Paths.Runs = runsBase
	cfg.Worktree.Base = worktreeBase

	oldRunID := contracts.RunID("2026-04-21-PR456-abcdef0")
	oldRunCtx, err := internalio.NewRunContext(oldRunID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(oldRunCtx.RunDir(), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(oldRunCtx.RunDir(), "config.snapshot.yaml"), []byte(
		"repo:\n"+
			"  root: "+cfg.Repo.Root+"\n"+
			"  default_branch: main\n"+
			"  best_branch: best\n"+
			"paths:\n"+
			"  runs: "+cfg.Paths.Runs+"\n"+
			"worktree:\n"+
			"  base: "+cfg.Worktree.Base+"\n",
	), 0o644))
	pkg := stubTaskPackageForRun(oldRunCtx, 456)
	require.NoError(t, internalio.WriteJSONAtomic(oldRunCtx.TaskPackagePath(), pkg))
	for _, wt := range pkg.Worktrees {
		runGit(t, repoRoot, "worktree", "add", "-b", wt.Branch, wt.Path, "HEAD")
	}
	require.NoError(t, state.NewWriter(oldRunCtx).Append(startedEntry(456, oldRunID, time.Now().UTC())))
	require.NoError(t, state.NewWriter(oldRunCtx).Append(contracts.StateEntry{
		Kind: contracts.StateKindInterrupted,
		Value: contracts.StateEntryInterrupted{
			Kind:   contracts.StateKindInterrupted,
			PR:     456,
			RunID:  oldRunID,
			Step:   contracts.FailedStep20,
			Reason: contracts.InterruptedReasonUnknown,
			At:     time.Now().UTC(),
		},
	}))

	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)
	orch.steps = stubPipelineSteps(nil, stubStep70{})

	require.NoError(t, orch.Run(context.Background(), 456, RunOptions{FromScratch: true}))

	list := runGit(t, repoRoot, "worktree", "list", "--porcelain")
	for _, wt := range pkg.Worktrees {
		assert.NoDirExists(t, wt.Path)
		assert.NotContains(t, list, wt.Path)
	}
}

func TestRun_FromScratchCleanupUsesOldRunConfigSnapshotRepoRoot(t *testing.T) {
	if _, err := processenv.TrustedLookPath("git"); err != nil {
		t.Skipf("git not available in trusted PATH: %v", err)
	}
	root := t.TempDir()
	oldRepoRoot := filepath.Join(root, "old-repo")
	liveRepoRoot := filepath.Join(root, "live-repo-without-git")
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	require.NoError(t, os.MkdirAll(worktreeBase, 0o755))
	require.NoError(t, os.MkdirAll(liveRepoRoot, 0o755))
	runGit(t, "", "init", "-b", "main", oldRepoRoot)
	runGit(t, oldRepoRoot, "config", "user.email", "auto-improve@example.test")
	runGit(t, oldRepoRoot, "config", "user.name", "auto-improve test")
	require.NoError(t, os.WriteFile(filepath.Join(oldRepoRoot, "README.md"), []byte("fixture\n"), 0o644))
	runGit(t, oldRepoRoot, "add", "README.md")
	runGit(t, oldRepoRoot, "commit", "-m", "initial")

	cfg := testConfig(t)
	cfg.Repo.Root = liveRepoRoot
	cfg.Paths.Runs = runsBase
	cfg.Worktree.Base = worktreeBase

	oldRunID := contracts.RunID("2026-04-21-PR460-abcdef0")
	oldRunCtx, err := internalio.NewRunContext(oldRunID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(oldRunCtx.RunDir(), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(oldRunCtx.RunDir(), "config.snapshot.yaml"), []byte(
		"repo:\n"+
			"  root: "+oldRepoRoot+"\n"+
			"  default_branch: main\n"+
			"  best_branch: best\n"+
			"paths:\n"+
			"  runs: "+cfg.Paths.Runs+"\n"+
			"worktree:\n"+
			"  base: "+cfg.Worktree.Base+"\n",
	), 0o644))
	pkg := stubTaskPackageForRun(oldRunCtx, 460)
	require.NoError(t, internalio.WriteJSONAtomic(oldRunCtx.TaskPackagePath(), pkg))
	for _, wt := range pkg.Worktrees {
		runGit(t, oldRepoRoot, "worktree", "add", "-b", wt.Branch, wt.Path, "HEAD")
	}
	require.NoError(t, state.NewWriter(oldRunCtx).Append(startedEntry(460, oldRunID, time.Now().UTC())))
	require.NoError(t, state.NewWriter(oldRunCtx).Append(contracts.StateEntry{
		Kind: contracts.StateKindInterrupted,
		Value: contracts.StateEntryInterrupted{
			Kind:   contracts.StateKindInterrupted,
			PR:     460,
			RunID:  oldRunID,
			Step:   contracts.FailedStep20,
			Reason: contracts.InterruptedReasonUnknown,
			At:     time.Now().UTC(),
		},
	}))

	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)
	orch.steps = stubPipelineSteps(nil, stubStep70{})

	require.NoError(t, orch.Run(context.Background(), 460, RunOptions{FromScratch: true}))

	list := runGit(t, oldRepoRoot, "worktree", "list", "--porcelain")
	for _, wt := range pkg.Worktrees {
		assert.NoDirExists(t, wt.Path)
		assert.NotContains(t, list, wt.Path)
	}
	oldEvents, err := state.ScanEventsForRun(oldRunCtx, oldRunID)
	require.NoError(t, err)
	require.NotEmpty(t, oldEvents)
	assert.Equal(t, contracts.StateKindSkipped, oldEvents[len(oldEvents)-1].Kind)
}
