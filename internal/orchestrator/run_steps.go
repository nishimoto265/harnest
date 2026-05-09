package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/contracts/stepio"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/state"
	"github.com/nishimoto265/auto-improve/internal/steps/agentrunner"
	"github.com/nishimoto265/auto-improve/internal/steps/step20_implement"
	"github.com/nishimoto265/auto-improve/internal/steps/step50_implement"
	"gopkg.in/yaml.v3"
)

func (o *Orchestrator) ensureRunScaffold(run *StepRunContext) error {
	for _, path := range []string{
		run.IO.RunDir(),
		filepath.Join(run.IO.RunDir(), "20-pass1"),
		filepath.Join(run.IO.RunDir(), "30"),
		filepath.Join(run.IO.RunDir(), "40"),
		filepath.Join(run.IO.RunDir(), "50-pass2"),
		filepath.Join(run.IO.RunDir(), "60"),
		filepath.Join(run.IO.RunDir(), "70"),
		filepath.Join(run.IO.RunDir(), "processed-details"),
	} {
		if err := internalio.EnsureNoSymlinkPathComponents(path); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(run.IO.RunDir(), 0o755); err != nil {
		return err
	}
	return writeConfigSnapshot(filepath.Join(run.IO.RunDir(), "config.snapshot.yaml"), run.Config)
}

func (o *Orchestrator) loadPersistedArtifacts(run *StepRunContext) error {
	if run.TaskPackage == nil && fileExists(run.IO.TaskPackagePath()) {
		pkg, err := internalio.ReadJSON[contracts.TaskPackage](run.IO.TaskPackagePath())
		if err != nil {
			return err
		}
		if pkg.RunID != run.IO.RunID {
			return fmt.Errorf("orchestrator: task package run_id mismatch: expected=%s got=%s", run.IO.RunID, pkg.RunID)
		}
		run.TaskPackage = &pkg
		ctx, err := internalio.RunContextFromTaskPackage(pkg, run.IO.RunsBase, run.IO.WorktreeBase)
		if err != nil {
			return err
		}
		run.IO = ctx
		o.runContext = ctx
		o.stateWriter = state.NewWriter(ctx)
	}

	candidatesPath, err := run.IO.ResolveRunRelative("40/candidates.json")
	if err != nil {
		return err
	}
	if run.Candidates == nil && fileExists(candidatesPath) {
		candidates, err := readCandidatesForRun(candidatesPath, run.IO.RunID)
		if err != nil {
			return err
		}
		run.Candidates = &candidates
	}

	decisionPath, err := run.IO.ResolveRunRelative("70/decision.json")
	if err != nil {
		return err
	}
	if run.Decision == nil && fileExists(decisionPath) {
		decision, err := readDecisionForRun(decisionPath, run.IO.RunID)
		if err != nil {
			return err
		}
		run.Decision = &decision
	}

	if run.Intention == nil {
		intention, err := run.IntentionFile.Load()
		if err != nil {
			return err
		}
		run.Intention = intention
		if intention != nil && intention.Stage == contracts.IntentionStageNeedsManualRecovery {
			suppress, err := shouldSuppressNeedsRecoveryReconstruction(run.IO.RunsBase, run.IO.RunID)
			if err != nil {
				return err
			}
			if suppress {
				return nil
			}
			if _, exists, err := existingNeedsRecoverySentinelPath(run.IO.RunsBase, run.IO.RunID); err != nil {
				return err
			} else if exists {
				return nil
			}
			if err := ensureNeedsRecoverySentinel(run.IO, run.PR, run.IO.RunID, intention.RecoveryReason, intention.FailedStep); err != nil {
				return err
			}
		}
	}

	return nil
}

func (o *Orchestrator) resolveStartStep(run *StepRunContext) (contracts.FailedStep, error) {
	hasDecision, err := hasRunRelative(run.IO, "70/decision.json")
	if err != nil {
		return "", err
	}
	if hasDecision {
		return contracts.FailedStep70, nil
	}

	intention, err := run.IntentionFile.Load()
	if err != nil {
		return "", err
	}
	run.Intention = intention
	if intention != nil {
		return contracts.FailedStep70, nil
	}

	if ok, err := hasRunRelative(run.IO, "60/done.marker"); err != nil {
		return "", err
	} else if ok {
		timedOut, err := allFinalizedManifestsTimedOut(run, 2)
		if err != nil {
			return "", err
		}
		if timedOut {
			return "", errAllPass2TimedOutResume
		}
		return contracts.FailedStep60, nil
	}
	if done, err := taskPackageHasAllManifests(run.IO, 2, run.TaskPackage); err != nil {
		return "", err
	} else if done {
		timedOut, err := allFinalizedManifestsTimedOut(run, 2)
		if err != nil {
			return "", err
		}
		if timedOut {
			return "", errAllPass2TimedOutResume
		}
		return contracts.FailedStep60, nil
	}
	if ok, err := hasRunRelative(run.IO, "40/candidates.json"); err != nil {
		return "", err
	} else if ok {
		return contracts.FailedStep50, nil
	}
	if ok, err := hasRunRelative(run.IO, "30/done.marker"); err != nil {
		return "", err
	} else if ok {
		timedOut, err := allFinalizedManifestsTimedOut(run, 1)
		if err != nil {
			return "", err
		}
		if timedOut {
			return "", errAllPass1TimedOutResume
		}
		scorableAgents, err := scorableAgentsForPass(run.IO, run.TaskPackage, 1)
		if err != nil {
			return "", err
		}
		if len(scorableAgents) == 0 {
			return "", errNoScorableAgentsResume
		}
		return contracts.FailedStep30, nil
	}
	if done, err := taskPackageHasAllManifests(run.IO, 1, run.TaskPackage); err != nil {
		return "", err
	} else if done {
		timedOut, err := allFinalizedManifestsTimedOut(run, 1)
		if err != nil {
			return "", err
		}
		if timedOut {
			return "", errAllPass1TimedOutResume
		}
		scorableAgents, err := scorableAgentsForPass(run.IO, run.TaskPackage, 1)
		if err != nil {
			return "", err
		}
		if len(scorableAgents) == 0 {
			return "", errNoScorableAgentsResume
		}
		return contracts.FailedStep30, nil
	}
	if run.TaskPackage != nil {
		return contracts.FailedStep20, nil
	}
	return contracts.FailedStep10, nil
}

func pipelineFrom(start contracts.FailedStep) []contracts.FailedStep {
	all := []contracts.FailedStep{
		contracts.FailedStep10,
		contracts.FailedStep20,
		contracts.FailedStep30,
		contracts.FailedStep40,
		contracts.FailedStep50,
		contracts.FailedStep60,
		contracts.FailedStep70,
	}
	index := 0
	for i, step := range all {
		if step == start {
			index = i
			break
		}
	}
	return all[index:]
}

func (o *Orchestrator) runStep10(ctx context.Context, run *StepRunContext) error {
	if err := o.runSingle(ctx, run, contracts.FailedStep10, o.steps.Step10); err != nil {
		return err
	}
	if err := o.loadPersistedArtifacts(run); err != nil {
		return err
	}
	return o.appendState(stepDoneEntry(run.PR, run.IO.RunID, contracts.FailedStep10, time.Now().UTC()))
}

func (o *Orchestrator) runSingle(ctx context.Context, run *StepRunContext, step contracts.FailedStep, runner Step) error {
	if runner == nil {
		return fmt.Errorf("orchestrator: missing runner for step %s", step)
	}
	stepRun := *run
	stepRun.Step = step
	stepRun.Agent = ""
	stepRun.Pass = 0
	return runner.Run(ctx, &stepRun)
}

func (o *Orchestrator) runParallel(ctx context.Context, run *StepRunContext, pass int, step contracts.FailedStep, runners map[contracts.AgentID]Step) error {
	agents := passAgents(run.TaskPackage, pass)
	if len(agents) == 0 {
		return fmt.Errorf("orchestrator: no agents configured for pass %d", pass)
	}

	type parallelResult struct {
		agent contracts.AgentID
		err   error
	}
	errCh := make(chan parallelResult, len(agents))
	var wg sync.WaitGroup
	var blockedErr error
	for _, agent := range agents {
		done, err := hasFinalizedManifest(run.IO, pass, agent)
		if err != nil {
			return err
		}
		if done {
			o.emitProgress(ctx, ProgressEvent{
				Event:   ProgressAgentDone,
				RunID:   run.IO.RunID,
				PR:      run.PR,
				Step:    step,
				Pass:    pass,
				Agent:   agent,
				RunDir:  run.IO.RunDir(),
				Message: "already finalized",
			})
			continue
		}
		runner, ok := runners[agent]
		if !ok || runner == nil {
			return fmt.Errorf("orchestrator: missing runner for step %s agent %s", step, agent)
		}
		if err := o.ensureNoGlobalSentinel(run.IO); err != nil {
			blockedErr = err
			break
		}
		agent := agent
		wg.Add(1)
		go func() {
			defer wg.Done()
			o.emitProgress(ctx, ProgressEvent{
				Event:  ProgressAgentStart,
				RunID:  run.IO.RunID,
				PR:     run.PR,
				Step:   step,
				Pass:   pass,
				Agent:  agent,
				RunDir: run.IO.RunDir(),
			})
			stepRun := *run
			stepRun.Step = step
			stepRun.Pass = pass
			stepRun.Agent = agent
			err := runner.Run(ctx, &stepRun)
			event := ProgressEvent{
				Event:  ProgressAgentDone,
				RunID:  run.IO.RunID,
				PR:     run.PR,
				Step:   step,
				Pass:   pass,
				Agent:  agent,
				RunDir: run.IO.RunDir(),
			}
			if err != nil {
				event.Error = err.Error()
			}
			o.emitProgress(ctx, event)
			errCh <- parallelResult{agent: agent, err: err}
		}()
	}
	wg.Wait()
	close(errCh)
	if blockedErr != nil {
		return blockedErr
	}

	rescueExhausted := make([]stepio.RescueExhausted, 0, len(agents))
	var interruptedDetail string
	var manualRecoveryErr *agentrunner.ManualRecoveryRequiredError
	var manualRecoveryAgent contracts.AgentID
	for result := range errCh {
		if result.err == nil {
			continue
		}
		var exhausted20 *step20_implement.RescueExhaustedError
		if errors.As(result.err, &exhausted20) {
			rescueExhausted = append(rescueExhausted, exhausted20.Result())
			continue
		}
		var exhausted50 *step50_implement.RescueExhaustedError
		if errors.As(result.err, &exhausted50) {
			rescueExhausted = append(rescueExhausted, exhausted50.Result())
			continue
		}
		var manualRecovery *agentrunner.ManualRecoveryRequiredError
		if errors.As(result.err, &manualRecovery) {
			if manualRecoveryErr == nil {
				manualRecoveryErr = manualRecovery
				manualRecoveryAgent = result.agent
			}
			continue
		}
		switch {
		case errors.Is(result.err, step20_implement.ErrAgentLeaseContended),
			errors.Is(result.err, step20_implement.ErrRescueAbortedLeaseActive),
			errors.Is(result.err, step50_implement.ErrAgentLeaseContended),
			errors.Is(result.err, step50_implement.ErrRescueAbortedLeaseActive):
			if interruptedDetail == "" {
				interruptedDetail = fmt.Sprintf("agent=%s: %v", result.agent, result.err)
			}
			continue
		}
		return fmt.Errorf("orchestrator: step %s agent %s: %w", step, result.agent, result.err)
	}
	if manualRecoveryErr != nil {
		if err := o.handleManualRecovery(run, step, manualRecoveryErr.Reason, manualRecoveryAgent, manualRecoveryErr.Detail); err != nil {
			return err
		}
		return errStopPipeline
	}
	if len(rescueExhausted) > 0 {
		if err := o.handleRescueExhausted(run, step, rescueExhausted); err != nil {
			return err
		}
		return errStopPipeline
	}
	if interruptedDetail != "" {
		if err := o.appendInterrupted(run.PR, run.IO.RunID, step, contracts.InterruptedReasonUnknown, interruptedDetail); err != nil {
			return err
		}
		return errStopPipeline
	}
	if err := o.validateImplementationBoundary(run, pass, agents); err != nil {
		return err
	}
	return nil
}

func (o *Orchestrator) appendTerminalDecision(run *StepRunContext) error {
	if run.Decision == nil {
		decisionPath, err := run.IO.ResolveRunRelative("70/decision.json")
		if err != nil {
			return err
		}
		if !fileExists(decisionPath) {
			return nil
		}
		decision, err := readDecisionForRun(decisionPath, run.IO.RunID)
		if err != nil {
			return err
		}
		run.Decision = &decision
	}

	entries, err := state.ScanEventsForRun(run.IO, run.IO.RunID)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Kind.IsTerminal() {
			return nil
		}
	}

	now := time.Now().UTC()
	switch decision := run.Decision.Value.(type) {
	case contracts.DecisionAdopt:
		return o.appendState(promotedEntry(run.PR, run.IO.RunID, now))
	case *contracts.DecisionAdopt:
		if decision == nil {
			return nil
		}
		return o.appendState(promotedEntry(run.PR, run.IO.RunID, now))
	case contracts.DecisionRollback:
		return o.appendState(rollbackEntry(run.PR, run.IO.RunID, decision.RollbackReason, decision.FailedStep, now))
	case *contracts.DecisionRollback:
		if decision == nil {
			return nil
		}
		return o.appendState(rollbackEntry(run.PR, run.IO.RunID, decision.RollbackReason, decision.FailedStep, now))
	default:
		return o.appendState(completedEntry(run.PR, run.IO.RunID, contracts.FailedStep70, now))
	}
}

func writeConfigSnapshot(path string, cfg *config.Config) error {
	if fileExists(path) {
		return nil
	}
	snapshot := *cfg
	repoRoot, err := cfg.RepoRoot()
	if err != nil {
		return err
	}
	runsBase, err := cfg.RunsBase()
	if err != nil {
		return err
	}
	worktreeBase, err := cfg.WorktreeBase()
	if err != nil {
		return err
	}
	agentConfigPath, err := cfg.AgentConfigSnapshotPath()
	if err != nil {
		return err
	}
	snapshot.Repo.Root = repoRoot
	snapshot.Paths.Runs = ""
	snapshot.RunsBasePath = runsBase
	snapshot.Worktree.Base = ""
	snapshot.WorktreeBasePath = worktreeBase
	snapshot.AgentConfigPath = agentConfigPath
	snapshot.AgentFileSnapshot = cfg.AgentFile()
	data, err := yaml.Marshal(snapshot)
	if err != nil {
		return err
	}
	return internalio.WriteAtomic(path, data)
}

func passAgents(pkg *contracts.TaskPackage, pass int) []contracts.AgentID {
	if pkg == nil {
		return append([]contracts.AgentID(nil), defaultAgents...)
	}
	seen := make(map[contracts.AgentID]struct{}, len(pkg.Worktrees))
	var agents []contracts.AgentID
	for _, worktree := range pkg.Worktrees {
		if worktree.Pass != pass {
			continue
		}
		if _, ok := seen[worktree.Agent]; ok {
			continue
		}
		seen[worktree.Agent] = struct{}{}
		agents = append(agents, worktree.Agent)
	}
	return agents
}

func taskPackageHasAllManifests(runCtx internalio.RunContext, pass int, pkg *contracts.TaskPackage) (bool, error) {
	if pkg == nil {
		return false, nil
	}
	for _, agent := range passAgents(pkg, pass) {
		done, err := hasFinalizedManifest(runCtx, pass, agent)
		if err != nil {
			return false, err
		}
		if !done {
			return false, nil
		}
	}
	return true, nil
}

func hasRunRelative(runCtx internalio.RunContext, rel string) (bool, error) {
	path, err := runCtx.ResolveRunRelative(rel)
	if err != nil {
		return false, err
	}
	return fileExists(path), nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
