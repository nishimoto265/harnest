package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nishimoto265/auto-improve/internal/archive"
	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/policyrepo"
	"github.com/nishimoto265/auto-improve/internal/state"
	"github.com/nishimoto265/auto-improve/internal/steps/step70_decide"
	"github.com/spf13/cobra"
)

func main() {
	root := newRootCmd()
	if err := root.Execute(); err != nil {
		var exitErr interface{ ExitCode() int }
		if errors.As(err, &exitErr) {
			_, _ = fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(exitErr.ExitCode())
		}
		_, _ = fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "auto-improve",
		Short:         "Self-improving harness pipeline for AI coding agents",
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	cmd.AddCommand(
		newPreflightCmd(),
		newDetectMergedCmd(),
		newRunCmd(),
		newSunsetCmd(),
		newRecoverCmd(),
	)
	return cmd
}

func newRecoverCmd() *cobra.Command {
	var inspect bool
	var rollback bool
	var adoptAnyway bool
	var markManualAbort bool
	var clearSentinel bool
	var runID string
	var clearDivergedSunset bool
	var finalizeCleanup bool
	var remoteHead string
	var registryHead string
	var policyHead string
	cmd := &cobra.Command{
		Use:           "recover",
		Short:         "Inspect or recover a stuck promotion run",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			selected := 0
			for _, enabled := range []bool{inspect, rollback, adoptAnyway, markManualAbort, clearSentinel, clearDivergedSunset, finalizeCleanup} {
				if enabled {
					selected++
				}
			}
			if selected > 1 {
				return commandExitError{code: 2, msg: "recover: flags are mutually exclusive"}
			}
			if clearDivergedSunset {
				if runID != "" {
					return commandExitError{code: 2, msg: "recover: --clear-diverged-sunset does not accept --run"}
				}
				return runRecoverClearDivergedSunset(cmd)
			}
			if finalizeCleanup {
				return runRecoverFinalizeCleanup(cmd, runID, remoteHead, registryHead, policyHead)
			}
			if inspect {
				return runRecoverInspect(cmd, runID)
			}
			if rollback {
				return runRecoverRollback(cmd, runID)
			}
			if adoptAnyway {
				return runRecoverAdoptAnyway(cmd, runID)
			}
			if markManualAbort {
				return runRecoverMarkManualAbort(cmd, runID)
			}
			if clearSentinel {
				return runRecoverClearSentinel(cmd, runID)
			}
			return commandExitError{code: 2, msg: "recover: not implemented"}
		},
	}
	cmd.Flags().BoolVar(&inspect, "inspect", false, "Inspect recovery state without making changes")
	cmd.Flags().BoolVar(&rollback, "rollback", false, "Rollback a recoverable stuck promotion run")
	cmd.Flags().BoolVar(&adoptAnyway, "adopt-anyway", false, "Finalize an already-applied adoption when the safe matrix allows it")
	cmd.Flags().BoolVar(&markManualAbort, "mark-manual-abort", false, "Convert a stuck promotion into a manual-abort-pending-cleanup state")
	cmd.Flags().BoolVar(&clearSentinel, "clear-sentinel", false, "Clear a durable needs-recovery sentinel as a last resort")
	cmd.Flags().StringVar(&runID, "run", "", "Run ID to inspect or recover")
	cmd.Flags().BoolVar(&clearDivergedSunset, "clear-diverged-sunset", false, "Clear the durable sunset divergence block after verifying sunset is not mid-transaction")
	cmd.Flags().BoolVar(&finalizeCleanup, "finalize-cleanup", false, "Clear an .aborted.json sentinel after verifying manual cleanup state")
	cmd.Flags().StringVar(&remoteHead, "remote-head", "", "Expected remote HEAD SHA for --finalize-cleanup")
	cmd.Flags().StringVar(&registryHead, "registry-head", "", "Expected registry head SHA for --finalize-cleanup")
	cmd.Flags().StringVar(&policyHead, "policy-head", "", "Expected policy_branch HEAD SHA for --finalize-cleanup when repo.policy_branch is configured")
	return cmd
}

type recoverInspectSnapshot struct {
	Event            string                       `json:"event"`
	RunsBase         string                       `json:"runs_base"`
	RunID            string                       `json:"run_id,omitempty"`
	RemoteHead       string                       `json:"remote_head,omitempty"`
	RegistryHead     string                       `json:"registry_head,omitempty"`
	PolicyBranch     string                       `json:"policy_branch,omitempty"`
	PolicySnapshot   *policyrepo.SnapshotMetadata `json:"policy_snapshot,omitempty"`
	PolicyRemoteHead string                       `json:"policy_remote_head,omitempty"`
	Intention        *contracts.IntentionRecord   `json:"intention,omitempty"`
	Decision         *contracts.Decision          `json:"decision,omitempty"`
	Sentinel         any                          `json:"sentinel,omitempty"`
	Processed        []contracts.StateEntry       `json:"processed,omitempty"`
	At               time.Time                    `json:"at"`
}

func runRecoverInspect(cmd *cobra.Command, runID string) error {
	runsBase, lock, err := recoverRunsBaseAndInspectLock()
	if err != nil {
		return err
	}
	if lock != nil {
		defer func() {
			_ = lock.Unlock()
		}()
	}
	if err := validateRegistryIntegrity(runsBase); err != nil {
		return err
	}
	snapshot, err := buildRecoverInspectSnapshot(runsBase, runID)
	if err != nil {
		return err
	}
	return json.NewEncoder(cmd.OutOrStdout()).Encode(snapshot)
}

func runRecoverClearDivergedSunset(cmd *cobra.Command) error {
	runsBase, lock, err := recoverRunsBaseAndLock()
	if err != nil {
		return err
	}
	defer func() {
		_ = lock.Unlock()
	}()

	markerPath := filepath.Join(runsBase, "sunset-running.marker")
	if _, err := os.Stat(markerPath); err == nil {
		return commandExitError{code: 2, msg: "recover: sunset-running.marker still exists; refusing to clear sunset divergence during an in-progress transaction"}
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := validateRegistryIntegrity(runsBase); err != nil {
		return err
	}
	if err := archive.ClearDivergedMarker(runsBase); err != nil {
		return err
	}

	return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{
		"event":     "diverged_sunset_cleared",
		"runs_base": runsBase,
		"at":        time.Now().UTC().Format(time.RFC3339Nano),
	})
}

type recoverFinalizeCleanupOutput struct {
	Event        string `json:"event"`
	RunsBase     string `json:"runs_base"`
	RunID        string `json:"run_id"`
	RemoteHead   string `json:"remote_head"`
	RegistryHead string `json:"registry_head"`
	PolicyHead   string `json:"policy_head,omitempty"`
}

type recoverActionOutput struct {
	Event    string `json:"event"`
	RunsBase string `json:"runs_base"`
	RunID    string `json:"run_id"`
}

var recoverInspectRemoteHeadTimeout = 10 * time.Second
var recoverMutationTimeout = 30 * time.Second

func recoverPaths() (string, string, error) {
	cfg, err := config.LoadDefault()
	if err != nil {
		return "", "", commandExitError{code: 2, msg: err.Error()}
	}
	runsBase, err := cfg.RunsBase()
	if err != nil {
		return "", "", commandExitError{code: 2, msg: err.Error()}
	}
	lockPath, err := cfg.PromotionLockPath()
	if err != nil {
		return "", "", commandExitError{code: 2, msg: err.Error()}
	}
	return runsBase, lockPath, nil
}

func recoverRunsBaseAndInspectLock() (string, *internalio.FileLock, error) {
	runsBase, lockPath, err := recoverPaths()
	if err != nil {
		return "", nil, err
	}
	lock, acquired, err := internalio.TryAcquireFileLock(lockPath)
	if err != nil {
		return "", nil, err
	}
	if !acquired {
		return "", nil, commandExitError{code: 2, msg: "recover: promotion.lock is held by another process"}
	}
	return runsBase, lock, nil
}

func recoverRunsBaseAndLock() (string, *internalio.FileLock, error) {
	runsBase, lockPath, err := recoverPaths()
	if err != nil {
		return "", nil, err
	}
	lock, acquired, err := internalio.TryAcquireFileLock(lockPath)
	if err != nil {
		return "", nil, err
	}
	if !acquired {
		return "", nil, commandExitError{code: 2, msg: "recover: promotion.lock is held by another process"}
	}
	return runsBase, lock, nil
}

func validateRegistryIntegrity(runsBase string) error {
	if _, err := internalio.RegistryLines(filepath.Join(runsBase, "rules-registry.jsonl")); err != nil {
		return commandExitError{code: 2, msg: fmt.Sprintf("recover: rules-registry.jsonl integrity check failed: %v", err)}
	}
	return nil
}

var recoverRemoteHead = func(ctx context.Context, repoRoot, branch string) (string, error) {
	return step70_decide.RealGitOps{RepoDir: repoRoot}.RemoteHead(ctx, branch)
}

var recoverGitOpsForRepo = func(repoRoot string) step70_decide.GitOps {
	return step70_decide.RealGitOps{RepoDir: repoRoot}
}

var recoverRegistryHead = func(runsBase string) (string, error) {
	lines, err := internalio.RegistryLines(filepath.Join(runsBase, "rules-registry.jsonl"))
	if err != nil {
		return "", err
	}
	if len(lines) == 0 {
		return "", nil
	}
	return lines[len(lines)-1].Sha256, nil
}

func runRecoverFinalizeCleanup(cmd *cobra.Command, runID, expectedRemoteHead, expectedRegistryHead, expectedPolicyHead string) error {
	if runID == "" {
		return commandExitError{code: 2, msg: "recover: --finalize-cleanup requires --run <id>"}
	}
	if !cmd.Flags().Changed("remote-head") || !cmd.Flags().Changed("registry-head") {
		return commandExitError{code: 2, msg: "recover: --finalize-cleanup requires both --remote-head and --registry-head"}
	}
	runsBase, lock, err := recoverRunsBaseAndLock()
	if err != nil {
		return err
	}
	defer func() {
		_ = lock.Unlock()
	}()
	if err := validateRegistryIntegrity(runsBase); err != nil {
		return err
	}
	runCtx, err := newRecoverRunContext(runsBase, runID)
	if err != nil {
		return err
	}
	if err := requireAbortedSentinel(runCtx); err != nil {
		return err
	}
	pkg, err := loadRecoverTaskPackage(runCtx)
	if err != nil {
		return err
	}
	cfg, err := recoverConfigForRun(runCtx)
	if err != nil {
		return commandExitError{code: 2, msg: err.Error()}
	}
	repoRoot, err := cfg.RepoRoot()
	if err != nil {
		return commandExitError{code: 2, msg: err.Error()}
	}
	remoteHeadCtx, cancel := context.WithTimeout(context.Background(), recoverMutationTimeout)
	defer cancel()
	actualRemoteHead, err := recoverRemoteHead(remoteHeadCtx, repoRoot, pkg.BestBranch)
	if err != nil {
		return commandExitError{code: 2, msg: fmt.Sprintf("recover: remote HEAD check failed: %v", err)}
	}
	if actualRemoteHead != expectedRemoteHead {
		return commandExitError{code: 2, msg: fmt.Sprintf("recover: remote HEAD mismatch: have=%s want=%s", actualRemoteHead, expectedRemoteHead)}
	}
	actualRegistryHead, err := recoverRegistryHead(runsBase)
	if err != nil {
		return commandExitError{code: 2, msg: fmt.Sprintf("recover: registry HEAD check failed: %v", err)}
	}
	if actualRegistryHead != expectedRegistryHead {
		return commandExitError{code: 2, msg: fmt.Sprintf("recover: registry HEAD mismatch: have=%s want=%s", actualRegistryHead, expectedRegistryHead)}
	}
	actualPolicyHead := ""
	policyBranch, _, err := recoverPolicyBranch(runCtx, cfg)
	if err != nil {
		return err
	}
	if strings.TrimSpace(policyBranch) != "" {
		if !cmd.Flags().Changed("policy-head") {
			return commandExitError{code: 2, msg: "recover: --finalize-cleanup requires --policy-head when repo.policy_branch is configured"}
		}
		actualPolicyHead, err = recoverRemoteHead(remoteHeadCtx, repoRoot, policyBranch)
		if err != nil {
			return commandExitError{code: 2, msg: fmt.Sprintf("recover: policy_branch HEAD check failed: %v", err)}
		}
		if actualPolicyHead != expectedPolicyHead {
			return commandExitError{code: 2, msg: fmt.Sprintf("recover: policy_branch HEAD mismatch: have=%s want=%s", actualPolicyHead, expectedPolicyHead)}
		}
	}
	if err := step70_decide.FinalizeCleanup(runCtx, recoverIntentionStore{runCtx: runCtx}); err != nil {
		return err
	}
	return json.NewEncoder(cmd.OutOrStdout()).Encode(recoverFinalizeCleanupOutput{
		Event:        "manual_cleanup_finalized",
		RunsBase:     runsBase,
		RunID:        string(runCtx.RunID),
		RemoteHead:   actualRemoteHead,
		RegistryHead: actualRegistryHead,
		PolicyHead:   actualPolicyHead,
	})
}

func runRecoverRollback(cmd *cobra.Command, runID string) error {
	_, runsBase, runCtx, pkg, _, store, lock, err := recoverRunPrereqs(runID, true)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Unlock() }()
	ctx, cancel := context.WithTimeout(context.Background(), recoverMutationTimeout)
	defer cancel()
	cfg, err := recoverConfigForRun(runCtx)
	if err != nil {
		return commandExitError{code: 2, msg: err.Error()}
	}
	repoRoot, err := cfg.RepoRoot()
	if err != nil {
		return commandExitError{code: 2, msg: err.Error()}
	}
	if err := step70_decide.RecoverRollback(ctx, pkg.PR, runCtx, &pkg, store, step70_decide.Deps{
		Git:          recoverGitOpsForRepo(repoRoot),
		Now:          func() time.Time { return time.Now().UTC() },
		RepoRoot:     repoRoot,
		PolicyBranch: recoverPolicyBranchString(runCtx, cfg),
	}); err != nil {
		var refused *step70_decide.RecoverRefusalError
		if errors.As(err, &refused) {
			return commandExitError{code: 2, msg: err.Error()}
		}
		return err
	}
	return json.NewEncoder(cmd.OutOrStdout()).Encode(recoverActionOutput{
		Event:    "recover_rollback",
		RunsBase: runsBase,
		RunID:    string(runCtx.RunID),
	})
}

func runRecoverAdoptAnyway(cmd *cobra.Command, runID string) error {
	_, runsBase, runCtx, pkg, candidates, store, lock, err := recoverRunPrereqs(runID, true)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Unlock() }()
	ctx, cancel := context.WithTimeout(context.Background(), recoverMutationTimeout)
	defer cancel()
	cfg, err := recoverConfigForRun(runCtx)
	if err != nil {
		return commandExitError{code: 2, msg: err.Error()}
	}
	repoRoot, err := cfg.RepoRoot()
	if err != nil {
		return commandExitError{code: 2, msg: err.Error()}
	}
	if err := step70_decide.RecoverAdoptAnyway(ctx, pkg.PR, runCtx, &pkg, &candidates, store, step70_decide.Deps{
		Git:          recoverGitOpsForRepo(repoRoot),
		Now:          func() time.Time { return time.Now().UTC() },
		RepoRoot:     repoRoot,
		PolicyBranch: recoverPolicyBranchString(runCtx, cfg),
	}); err != nil {
		var refused *step70_decide.RecoverRefusalError
		if errors.As(err, &refused) {
			return commandExitError{code: 2, msg: err.Error()}
		}
		return err
	}
	return json.NewEncoder(cmd.OutOrStdout()).Encode(recoverActionOutput{
		Event:    "recover_adopt_anyway",
		RunsBase: runsBase,
		RunID:    string(runCtx.RunID),
	})
}

func runRecoverMarkManualAbort(cmd *cobra.Command, runID string) error {
	_, runsBase, runCtx, pkg, _, store, lock, err := recoverRunPrereqs(runID, true)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Unlock() }()
	if err := step70_decide.RecoverMarkManualAbort(runCtx, pkg.PR, store, time.Now().UTC()); err != nil {
		return err
	}
	return json.NewEncoder(cmd.OutOrStdout()).Encode(recoverActionOutput{
		Event:    "recover_mark_manual_abort",
		RunsBase: runsBase,
		RunID:    string(runCtx.RunID),
	})
}

func runRecoverClearSentinel(cmd *cobra.Command, runID string) error {
	runsBase, runCtx, lock, err := recoverRunSentinelPrereqs(runID)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Unlock() }()
	events, err := state.ScanEventsForRun(runCtx, runCtx.RunID)
	if err != nil {
		return err
	}
	if !lastRunEventIsTerminal(events) {
		if pr, ok := recoverPRForClearSentinel(runCtx, events); ok {
			if err := state.NewWriter(runCtx).Append(contracts.StateEntry{
				Kind: contracts.StateKindCompleted,
				Value: contracts.StateEntryCompleted{
					Kind:   contracts.StateKindCompleted,
					PR:     pr,
					RunID:  runCtx.RunID,
					Step:   contracts.FailedStep70,
					Detail: "sentinel_manually_cleared",
					At:     time.Now().UTC(),
				},
			}); err != nil {
				return err
			}
		}
	}
	if err := step70_decide.RecoverClearSentinel(runCtx); err != nil {
		return err
	}
	return json.NewEncoder(cmd.OutOrStdout()).Encode(recoverActionOutput{
		Event:    "recover_clear_sentinel",
		RunsBase: runsBase,
		RunID:    string(runCtx.RunID),
	})
}

func buildRecoverInspectSnapshot(runsBase, runID string) (recoverInspectSnapshot, error) {
	snapshot := recoverInspectSnapshot{
		Event:    "recover_inspect",
		RunsBase: runsBase,
		At:       time.Now().UTC(),
	}
	registryLines, err := internalio.RegistryLines(filepath.Join(runsBase, "rules-registry.jsonl"))
	if err != nil {
		return recoverInspectSnapshot{}, err
	}
	if len(registryLines) > 0 {
		snapshot.RegistryHead = registryLines[len(registryLines)-1].Sha256
	}
	if runID == "" {
		return snapshot, nil
	}
	runCtx, err := newRecoverRunContext(runsBase, runID)
	if err != nil {
		return recoverInspectSnapshot{}, err
	}
	snapshot.RunID = string(runCtx.RunID)
	pkg, err := loadRecoverTaskPackage(runCtx)
	if err != nil {
		return recoverInspectSnapshot{}, err
	}
	cfg, err := recoverConfigForRun(runCtx)
	if err != nil {
		return recoverInspectSnapshot{}, err
	}
	repoRoot, err := cfg.RepoRoot()
	if err != nil {
		return recoverInspectSnapshot{}, err
	}
	remoteHeadCtx, cancel := context.WithTimeout(context.Background(), recoverInspectRemoteHeadTimeout)
	defer cancel()
	remoteHead, err := recoverRemoteHead(remoteHeadCtx, repoRoot, pkg.BestBranch)
	if err != nil {
		return recoverInspectSnapshot{}, err
	}
	snapshot.RemoteHead = remoteHead
	policyBranch, policySnapshot, err := recoverPolicyBranch(runCtx, cfg)
	if err != nil {
		return recoverInspectSnapshot{}, err
	}
	if policySnapshot != nil {
		snapshot.PolicySnapshot = policySnapshot
	}
	if strings.TrimSpace(policyBranch) != "" {
		snapshot.PolicyBranch = policyBranch
		policyRemoteHead, err := recoverRemoteHead(remoteHeadCtx, repoRoot, policyBranch)
		if err != nil {
			return recoverInspectSnapshot{}, err
		}
		snapshot.PolicyRemoteHead = policyRemoteHead
	}
	intentionPath, err := runCtx.ResolveRunRelative("70/intention.json")
	if err != nil {
		return recoverInspectSnapshot{}, err
	}
	if intention, err := readOptionalJSON[contracts.IntentionRecord](intentionPath); err == nil {
		snapshot.Intention = intention
	} else if !os.IsNotExist(err) {
		return recoverInspectSnapshot{}, err
	}
	if decision, err := readOptionalJSON[contracts.Decision](filepath.Join(runCtx.RunDir(), "70", "decision.json")); err == nil {
		snapshot.Decision = decision
	} else if !os.IsNotExist(err) {
		return recoverInspectSnapshot{}, err
	}
	if sentinel, err := readRecoverSentinel(runCtx); err != nil {
		return recoverInspectSnapshot{}, err
	} else {
		snapshot.Sentinel = sentinel
	}
	processed, err := state.ScanEventsForRun(runCtx, runCtx.RunID)
	if err != nil {
		return recoverInspectSnapshot{}, err
	}
	if len(processed) > 0 {
		snapshot.Processed = processed
	}
	return snapshot, nil
}

func recoverRunPrereqs(runID string, requireSentinel bool) (context.Context, string, internalio.RunContext, contracts.TaskPackage, contracts.Candidates, recoverIntentionStore, *internalio.FileLock, error) {
	if runID == "" {
		return nil, "", internalio.RunContext{}, contracts.TaskPackage{}, contracts.Candidates{}, recoverIntentionStore{}, nil, commandExitError{code: 2, msg: "recover: --run <id> is required"}
	}
	runsBase, lock, err := recoverRunsBaseAndLock()
	if err != nil {
		return nil, "", internalio.RunContext{}, contracts.TaskPackage{}, contracts.Candidates{}, recoverIntentionStore{}, nil, err
	}
	runCtx, err := newRecoverRunContext(runsBase, runID)
	if err != nil {
		_ = lock.Unlock()
		return nil, "", internalio.RunContext{}, contracts.TaskPackage{}, contracts.Candidates{}, recoverIntentionStore{}, nil, err
	}
	sentinelValue, _, err := existingRecoverSentinel(runCtx)
	if err != nil {
		_ = lock.Unlock()
		return nil, "", internalio.RunContext{}, contracts.TaskPackage{}, contracts.Candidates{}, recoverIntentionStore{}, nil, err
	}
	if err := validateRegistryIntegrity(runsBase); err != nil {
		_ = lock.Unlock()
		return nil, "", internalio.RunContext{}, contracts.TaskPackage{}, contracts.Candidates{}, recoverIntentionStore{}, nil, err
	}
	pkg, err := loadRecoverTaskPackage(runCtx)
	if err != nil {
		_ = lock.Unlock()
		return nil, "", internalio.RunContext{}, contracts.TaskPackage{}, contracts.Candidates{}, recoverIntentionStore{}, nil, err
	}
	candidates, err := loadRecoverCandidates(runCtx)
	if err != nil {
		_ = lock.Unlock()
		return nil, "", internalio.RunContext{}, contracts.TaskPackage{}, contracts.Candidates{}, recoverIntentionStore{}, nil, err
	}
	store := recoverIntentionStore{runCtx: runCtx}
	if requireSentinel && sentinelValue == nil {
		intention, loadErr := store.Load()
		if loadErr != nil {
			_ = lock.Unlock()
			return nil, "", internalio.RunContext{}, contracts.TaskPackage{}, contracts.Candidates{}, recoverIntentionStore{}, nil, loadErr
		}
		if intention == nil {
			_ = lock.Unlock()
			return nil, "", internalio.RunContext{}, contracts.TaskPackage{}, contracts.Candidates{}, recoverIntentionStore{}, nil, commandExitError{code: 2, msg: fmt.Sprintf("recover: no sentinel or persisted intention found for run_id=%s", runCtx.RunID)}
		}
	}
	return context.Background(), runsBase, runCtx, pkg, candidates, store, lock, nil
}

func recoverRunSentinelPrereqs(runID string) (string, internalio.RunContext, *internalio.FileLock, error) {
	if runID == "" {
		return "", internalio.RunContext{}, nil, commandExitError{code: 2, msg: "recover: --run <id> is required"}
	}
	runsBase, lock, err := recoverRunsBaseAndLock()
	if err != nil {
		return "", internalio.RunContext{}, nil, err
	}
	runCtx, err := newRecoverRunContext(runsBase, runID)
	if err != nil {
		_ = lock.Unlock()
		return "", internalio.RunContext{}, nil, err
	}
	sentinelValue, _, err := existingRecoverSentinel(runCtx)
	if err != nil {
		_ = lock.Unlock()
		return "", internalio.RunContext{}, nil, err
	}
	if sentinelValue == nil {
		_ = lock.Unlock()
		return "", internalio.RunContext{}, nil, commandExitError{code: 2, msg: fmt.Sprintf("recover: no sentinel found for run_id=%s", runCtx.RunID)}
	}
	return runsBase, runCtx, lock, nil
}

func newRecoverRunContext(runsBase, runID string) (internalio.RunContext, error) {
	cfg, err := recoverConfigFromSnapshotPath(runsBase, runID)
	if err == nil {
		worktreeBase, err := cfg.WorktreeBase()
		if err != nil {
			return internalio.RunContext{}, err
		}
		return internalio.NewRunContext(contracts.RunID(runID), runsBase, worktreeBase)
	} else if !os.IsNotExist(err) {
		return internalio.RunContext{}, err
	}
	cfg, err = config.LoadDefault()
	if err != nil {
		return internalio.RunContext{}, err
	}
	worktreeBase, err := cfg.WorktreeBase()
	if err != nil {
		return internalio.RunContext{}, err
	}
	return internalio.NewRunContext(contracts.RunID(runID), runsBase, worktreeBase)
}

func recoverConfigForRun(runCtx internalio.RunContext) (config.Config, error) {
	cfg, err := config.Load(filepath.Join(runCtx.RunDir(), "config.snapshot.yaml"))
	if err == nil {
		return cfg, nil
	}
	if os.IsNotExist(err) {
		return config.LoadDefault()
	}
	return config.Config{}, err
}

func recoverConfigFromSnapshotPath(runsBase, runID string) (config.Config, error) {
	return config.Load(filepath.Join(runsBase, runID, "config.snapshot.yaml"))
}

func recoverPolicyBranchString(runCtx internalio.RunContext, cfg config.Config) string {
	branch, _, err := recoverPolicyBranch(runCtx, cfg)
	if err != nil {
		return cfg.Repo.PolicyBranch
	}
	return branch
}

func recoverPolicyBranch(runCtx internalio.RunContext, cfg config.Config) (string, *policyrepo.SnapshotMetadata, error) {
	meta, ok, err := policyrepo.LoadSnapshotMetadata(runCtx)
	if err != nil {
		return "", nil, err
	}
	if ok {
		return meta.PolicyBranch, &meta, nil
	}
	return cfg.Repo.PolicyBranch, nil, nil
}

func loadRecoverTaskPackage(runCtx internalio.RunContext) (contracts.TaskPackage, error) {
	path, err := runCtx.ResolveRunRelative("task-package.json")
	if err != nil {
		return contracts.TaskPackage{}, err
	}
	pkg, err := internalio.ReadJSON[contracts.TaskPackage](path)
	if err != nil {
		return contracts.TaskPackage{}, err
	}
	if pkg.RunID != runCtx.RunID {
		return contracts.TaskPackage{}, fmt.Errorf("recover: task-package run_id mismatch: task_package=%s run=%s", pkg.RunID, runCtx.RunID)
	}
	return pkg, nil
}

func loadRecoverCandidates(runCtx internalio.RunContext) (contracts.Candidates, error) {
	path, err := runCtx.ResolveRunRelative(filepath.Join("40", "candidates.json"))
	if err != nil {
		return contracts.Candidates{}, err
	}
	candidates, err := internalio.ReadJSON[contracts.Candidates](path)
	if err != nil {
		return contracts.Candidates{}, err
	}
	if candidates.RunID != runCtx.RunID {
		return contracts.Candidates{}, fmt.Errorf("recover: candidates run_id mismatch: candidates=%s run=%s", candidates.RunID, runCtx.RunID)
	}
	return candidates, nil
}

func recoverPRForClearSentinel(runCtx internalio.RunContext, events []contracts.StateEntry) (int, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		if pr, ok := state.EntryPR(events[i]); ok && pr > 0 {
			return pr, true
		}
	}
	pkg, err := loadRecoverTaskPackage(runCtx)
	if err != nil {
		return 0, false
	}
	if pkg.PR <= 0 {
		return 0, false
	}
	return pkg.PR, true
}

func readOptionalJSON[T any](path string) (*T, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	value, err := internalio.ReadJSON[T](path)
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func readRecoverSentinel(runCtx internalio.RunContext) (any, error) {
	value, _, err := existingRecoverSentinel(runCtx)
	return value, err
}

func existingRecoverSentinel(runCtx internalio.RunContext) (any, string, error) {
	for _, name := range []string{
		contracts.NeedsRecoverySentinelAbortedFilename(runCtx.RunID),
		contracts.NeedsRecoverySentinelFilename(runCtx.RunID),
	} {
		path := filepath.Join(runCtx.RunsBase, "needs-recovery", name)
		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, "", err
		}
		value, err := readOptionalJSON[contracts.NeedsRecoverySentinel](path)
		if err != nil {
			return nil, "", err
		}
		return value, name, nil
	}
	return nil, "", nil
}

func requireAbortedSentinel(runCtx internalio.RunContext) error {
	path := filepath.Join(runCtx.RunsBase, "needs-recovery", contracts.NeedsRecoverySentinelAbortedFilename(runCtx.RunID))
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return commandExitError{code: 2, msg: fmt.Sprintf("recover: %s is required for --finalize-cleanup", contracts.NeedsRecoverySentinelAbortedFilename(runCtx.RunID))}
		}
		return err
	}
	return nil
}

func requireAnySentinel(runCtx internalio.RunContext) error {
	value, _, err := existingRecoverSentinel(runCtx)
	if err != nil {
		return err
	}
	if value == nil {
		return commandExitError{code: 2, msg: fmt.Sprintf("recover: no sentinel found for run_id=%s", runCtx.RunID)}
	}
	return nil
}

func lastRunEventIsTerminal(events []contracts.StateEntry) bool {
	if len(events) == 0 {
		return false
	}
	return events[len(events)-1].Kind.IsTerminal()
}

type recoverIntentionStore struct {
	runCtx internalio.RunContext
}

func (s recoverIntentionStore) path() (string, error) {
	return s.runCtx.ResolveRunRelative("70/intention.json")
}

func (s recoverIntentionStore) Load() (*contracts.IntentionRecord, error) {
	path, err := s.path()
	if err != nil {
		return nil, err
	}
	return readOptionalJSON[contracts.IntentionRecord](path)
}

func (s recoverIntentionStore) Save(record contracts.IntentionRecord) error {
	path, err := s.path()
	if err != nil {
		return err
	}
	return internalio.WriteJSONAtomic(path, record)
}

func (s recoverIntentionStore) Delete() error {
	path, err := s.path()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func notImplementedCommand(use, short string) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			return commandExitError{
				code: 2,
				msg:  fmt.Sprintf("%s: not implemented", cmd.Name()),
			}
		},
	}
}

type commandExitError struct {
	code int
	msg  string
}

func (e commandExitError) Error() string {
	return e.msg
}

func (e commandExitError) ExitCode() int {
	return e.code
}
