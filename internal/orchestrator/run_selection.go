package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nishimoto265/harnest/internal/config"
	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/nishimoto265/harnest/internal/state"
)

func acquirePRRunLock(ctx context.Context, runsBase string, pr int) (*internalio.FileLock, error) {
	lockCtx, cancel := context.WithTimeout(ctx, 25*time.Millisecond)
	defer cancel()
	lockPath := filepath.Join(runsBase, "pr-locks", fmt.Sprintf("pr-%d.lock", pr))
	lock, err := internalio.AcquireFileLockContext(lockCtx, lockPath)
	if errors.Is(err, context.DeadlineExceeded) {
		return nil, errConcurrentPRRun
	}
	return lock, err
}

func (o *Orchestrator) selectRun(ctx context.Context, pr int, opts RunOptions) (runSelection, error) {
	runsBase, err := o.cfg.RunsBase()
	if err != nil {
		return runSelection{}, err
	}
	worktreeBase, err := o.cfg.WorktreeBase()
	if err != nil {
		return runSelection{}, err
	}
	probeRunID := opts.RunID
	if probeRunID == "" {
		probeRunID = internalio.NewRunID(pr)
	}
	probeCtx, err := internalio.NewRunContext(probeRunID, runsBase, worktreeBase)
	if err != nil {
		return runSelection{}, err
	}

	latest, err := state.LatestRunForPR(probeCtx, pr)
	if err != nil {
		return runSelection{}, err
	}
	if latest.LastEvent == nil {
		return newFreshSelection(pr, opts, runsBase, worktreeBase)
	}
	if opts.FromScratch {
		replacement, err := o.prepareFromScratchReplacement(pr, latest, runsBase, worktreeBase)
		if err != nil {
			return runSelection{}, err
		}
		freshOpts := opts
		freshOpts.RunID = ""
		selection, err := newFreshSelection(pr, freshOpts, runsBase, worktreeBase)
		if err != nil {
			return runSelection{}, err
		}
		selection.fromScratch = replacement
		return selection, nil
	}

	action := latest.Action
	switch action {
	case state.NextActionResume:
		if latest.LastEvent != nil && isPolicySnapshotStaleInterrupted(*latest.LastEvent) {
			freshOpts := opts
			freshOpts.RunID = ""
			return newFreshSelection(pr, freshOpts, runsBase, worktreeBase)
		}
		runID, ok := stateRunID(*latest.LastEvent)
		if !ok {
			return runSelection{}, errors.New("orchestrator: latest resume event is missing run_id")
		}
		runCtx, err := loadRunContext(runID, runsBase, worktreeBase)
		if err != nil {
			return runSelection{}, err
		}
		return runSelection{
			runContext: runCtx,
			fresh:      false,
		}, nil
	case state.NextActionNeedsManualRecovery:
		runID, _ := stateRunID(*latest.LastEvent)
		runCtx, err := loadRunContext(runID, runsBase, worktreeBase)
		if err != nil {
			return runSelection{}, err
		}
		if err := ensureNeedsRecoverySentinelFromState(runCtx, latest.LastEvent); err != nil {
			return runSelection{}, err
		}
		return runSelection{}, fmt.Errorf("orchestrator: PR %d is blocked by needs_manual_recovery: run_id=%s", pr, runID)
	default:
		return newFreshSelection(pr, opts, runsBase, worktreeBase)
	}
}

func (o *Orchestrator) prepareFromScratchReplacement(pr int, latest state.LatestRun, runsBase, worktreeBase string) (*fromScratchReplacement, error) {
	if latest.LastEvent == nil || latest.LastEvent.Kind.IsTerminal() {
		return nil, nil
	}
	runID, ok := stateRunID(*latest.LastEvent)
	if !ok {
		return nil, errors.New("orchestrator: latest non-terminal event is missing run_id")
	}
	runCtx, err := loadRunContext(runID, runsBase, worktreeBase)
	if err != nil {
		return nil, err
	}
	var pkg *contracts.TaskPackage
	if fileExists(runCtx.TaskPackagePath()) {
		loaded, err := internalio.ReadJSON[contracts.TaskPackage](runCtx.TaskPackagePath())
		if err != nil {
			return nil, err
		}
		pkg = &loaded
	}
	step := latest.Step
	if step == "" {
		step = contracts.FailedStep10
	}
	if step == contracts.FailedStep70 {
		return nil, fmt.Errorf("orchestrator: --from-scratch refused for run_id=%s with unfinished step70; resume or recover first", runID)
	}
	if has, err := hasPersistedIntention(runCtx); err != nil {
		return nil, err
	} else if has {
		return nil, fmt.Errorf("orchestrator: --from-scratch refused for run_id=%s with persisted step70 intention; resume or recover first", runID)
	}
	repoRoot, err := fromScratchCleanupRepoRoot(runCtx, o.cfg)
	if err != nil {
		return nil, err
	}
	value := contracts.StateEntrySkipped{
		Kind:   contracts.StateKindSkipped,
		PR:     pr,
		RunID:  runID,
		Step:   step,
		Detail: "superseded_by_from_scratch",
		At:     time.Now().UTC(),
	}
	return &fromScratchReplacement{
		oldRunContext: runCtx,
		oldPackage:    pkg,
		repoRoot:      repoRoot,
		skippedEntry:  contracts.StateEntry{Kind: value.Kind, Value: value},
	}, nil
}

func (o *Orchestrator) startFromScratchReplacement(ctx context.Context, run *StepRunContext, replacement *fromScratchReplacement, started contracts.StateEntry) error {
	entries := []contracts.StateEntry{started}
	if replacement != nil {
		entries = []contracts.StateEntry{replacement.skippedEntry, started}
	}
	if err := state.NewWriter(run.IO).AppendAll(entries); err != nil {
		return err
	}
	if replacement == nil {
		return nil
	}
	return cleanupWorktreesWithGit(ctx, replacement.oldRunContext, replacement.oldPackage, replacement.repoRoot)
}

func hasPersistedIntention(runCtx internalio.RunContext) (bool, error) {
	path, err := NewIntentionStore(runCtx).Path()
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(path); err == nil {
		return true, nil
	} else if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	return false, nil
}

func isPolicySnapshotStaleInterrupted(entry contracts.StateEntry) bool {
	if entry.Kind != contracts.StateKindInterrupted {
		return false
	}
	switch v := entry.Value.(type) {
	case contracts.StateEntryInterrupted:
		return strings.HasPrefix(v.Detail, "policy_snapshot_stale")
	case *contracts.StateEntryInterrupted:
		return v != nil && strings.HasPrefix(v.Detail, "policy_snapshot_stale")
	default:
		return false
	}
}

func newFreshSelection(pr int, opts RunOptions, runsBase, worktreeBase string) (runSelection, error) {
	runID := opts.RunID
	if runID == "" {
		runID = internalio.NewRunID(pr)
	} else {
		runPR, err := runIDPR(runID)
		if err != nil {
			return runSelection{}, err
		}
		if runPR != pr {
			return runSelection{}, fmt.Errorf("orchestrator: run_id PR mismatch: run_id=%s pr=%d", runID, pr)
		}
	}
	runCtx, err := internalio.NewRunContext(runID, runsBase, worktreeBase)
	if err != nil {
		return runSelection{}, err
	}
	if err := internalio.EnsureNoSymlinkPathComponents(runCtx.RunDir()); err != nil {
		return runSelection{}, err
	}
	if info, err := os.Stat(runCtx.RunDir()); err == nil {
		if !info.IsDir() {
			return runSelection{}, fmt.Errorf("orchestrator: fresh run path is not a directory: %s", runCtx.RunDir())
		}
		entries, err := os.ReadDir(runCtx.RunDir())
		if err != nil {
			return runSelection{}, err
		}
		if len(entries) > 0 {
			return runSelection{}, fmt.Errorf("orchestrator: fresh run requires an empty run dir: %s", runCtx.RunDir())
		}
	} else if err != nil && !os.IsNotExist(err) {
		return runSelection{}, err
	}
	events, err := state.ScanEventsForRun(runCtx, runID)
	if err != nil {
		return runSelection{}, err
	}
	for _, entry := range events {
		if entry.Kind.IsTerminal() {
			return runSelection{}, fmt.Errorf("orchestrator: fresh run_id already has terminal state: %s", runID)
		}
	}
	return runSelection{
		runContext: runCtx,
		fresh:      true,
	}, nil
}

func runIDPR(runID contracts.RunID) (int, error) {
	raw := string(runID)
	start := strings.Index(raw, "-PR")
	if start < 0 {
		return 0, fmt.Errorf("orchestrator: invalid run_id PR segment: %s", runID)
	}
	rest := raw[start+3:]
	end := strings.Index(rest, "-")
	if end < 0 {
		return 0, fmt.Errorf("orchestrator: invalid run_id suffix: %s", runID)
	}
	pr, err := strconv.Atoi(rest[:end])
	if err != nil {
		return 0, fmt.Errorf("orchestrator: parse run_id PR %q: %w", runID, err)
	}
	return pr, nil
}

func loadRunContext(runID contracts.RunID, runsBase, worktreeBase string) (internalio.RunContext, error) {
	runCtx, err := internalio.NewRunContext(runID, runsBase, worktreeBase)
	if err != nil {
		return internalio.RunContext{}, err
	}
	effectiveWorktreeBase := worktreeBase
	if cfg, err := loadRunConfigSnapshot(runCtx); err == nil {
		snapshotWorktreeBase, err := cfg.WorktreeBase()
		if err != nil {
			return internalio.RunContext{}, err
		}
		effectiveWorktreeBase = snapshotWorktreeBase
		runCtx, err = internalio.NewRunContext(runID, runsBase, snapshotWorktreeBase)
		if err != nil {
			return internalio.RunContext{}, err
		}
	} else if !os.IsNotExist(err) {
		return internalio.RunContext{}, err
	}
	if err := internalio.EnsureNoSymlinkPathComponents(runCtx.RunDir()); err != nil {
		return internalio.RunContext{}, err
	}
	taskPackagePath := runCtx.TaskPackagePath()
	if !fileExists(taskPackagePath) {
		if err := validatePersistedRunScopedArtifacts(runCtx); err != nil {
			return internalio.RunContext{}, err
		}
		return runCtx, nil
	}
	pkg, err := internalio.ReadJSON[contracts.TaskPackage](taskPackagePath)
	if err != nil {
		return internalio.RunContext{}, err
	}
	if pkg.RunID != runID {
		return internalio.RunContext{}, fmt.Errorf("orchestrator: task package run_id mismatch: selected=%s package=%s", runID, pkg.RunID)
	}
	runCtx, err = internalio.RunContextFromTaskPackage(pkg, runsBase, effectiveWorktreeBase)
	if err != nil {
		return internalio.RunContext{}, err
	}
	if err := validatePersistedRunScopedArtifacts(runCtx); err != nil {
		return internalio.RunContext{}, err
	}
	return runCtx, nil
}

func loadRunConfigSnapshot(runCtx internalio.RunContext) (config.Config, error) {
	return config.Load(filepath.Join(runCtx.RunDir(), "config.snapshot.yaml"))
}

func fromScratchCleanupRepoRoot(runCtx internalio.RunContext, liveCfg *config.Config) (string, error) {
	if snapshot, err := loadRunConfigSnapshot(runCtx); err == nil {
		return snapshot.RepoRoot()
	} else if !os.IsNotExist(err) {
		return "", err
	}
	return liveCfg.RepoRoot()
}

type runSelection struct {
	runContext  internalio.RunContext
	fresh       bool
	fromScratch *fromScratchReplacement
}

type fromScratchReplacement struct {
	oldRunContext internalio.RunContext
	oldPackage    *contracts.TaskPackage
	repoRoot      string
	skippedEntry  contracts.StateEntry
}
