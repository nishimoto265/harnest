package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/contracts/stepio"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	ilog "github.com/nishimoto265/auto-improve/internal/logger"
	"github.com/nishimoto265/auto-improve/internal/state"
	"github.com/nishimoto265/auto-improve/internal/steps/step20_implement"
	"github.com/nishimoto265/auto-improve/internal/steps/step50_implement"
	"github.com/nishimoto265/auto-improve/internal/steps/step70_decide"
	"gopkg.in/yaml.v3"
)

var defaultAgents = []contracts.AgentID{"a1", "a2", "a3"}

type RunOptions struct {
	RunID contracts.RunID
}

type ContractDecoders struct {
	Step10 func([]byte, any) (any, error)
	Step20 func([]byte, any) (any, error)
	Step30 func([]byte, any) (any, error)
	Step40 func([]byte, any) (any, error)
	Step50 func([]byte, any) (any, error)
	Step60 func([]byte, any) (any, error)
	Step70 func([]byte, any) (any, error)
}

type Step interface {
	Run(ctx context.Context, run *StepRunContext) error
}

type Steps struct {
	Step10  Step
	Step20  map[contracts.AgentID]Step
	Step30  Step
	Step40  Step
	Step50  map[contracts.AgentID]Step
	Step60  Step
	Step70  Step
	Archive Step
}

type Orchestrator struct {
	cfg         *config.Config
	logger      *slog.Logger
	runContext  internalio.RunContext
	stateWriter state.Writer
	decoders    ContractDecoders
	steps       Steps
}

type StepRunContext struct {
	Config        *config.Config
	Logger        *slog.Logger
	PR            int
	Step          contracts.FailedStep
	Pass          int
	Agent         contracts.AgentID
	IO            internalio.RunContext
	TaskPackage   *contracts.TaskPackage
	Candidates    *contracts.Candidates
	Decision      *contracts.Decision
	Intention     *contracts.IntentionRecord
	IntentionFile *IntentionStore
}

var errStopPipeline = errors.New("orchestrator: stop pipeline")
var errNoScorableAgentsResume = errors.New("orchestrator: resume selected step30 but pass1 has no scorable agents")

type GlobalNeedsRecoveryError struct {
	Sentinel contracts.NeedsRecoverySentinel
}

func (e *GlobalNeedsRecoveryError) Error() string {
	return fmt.Sprintf(
		"orchestrator: global needs_manual_recovery block: run_id=%s pr=%d restart_from=%s",
		e.Sentinel.RunID,
		e.Sentinel.PR,
		contracts.FailedStep10,
	)
}

func (e *GlobalNeedsRecoveryError) RestartStep() contracts.FailedStep {
	return contracts.FailedStep10
}

func NewOrchestrator(cfg *config.Config) (*Orchestrator, error) {
	if cfg == nil {
		return nil, errors.New("orchestrator: config is required")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	decoders := ContractDecoders{
		Step10: func(data []byte, req any) (any, error) {
			request, ok := req.(stepio.Step10Request)
			if !ok {
				return nil, fmt.Errorf("orchestrator: step10 decoder expects Step10Request, got %T", req)
			}
			return stepio.DecodeAndValidateStep10Response(data, request)
		},
		Step20: func(data []byte, req any) (any, error) {
			request, ok := req.(stepio.Step20Request)
			if !ok {
				return nil, fmt.Errorf("orchestrator: step20 decoder expects Step20Request, got %T", req)
			}
			return stepio.DecodeAndValidateStep20Response(data, request)
		},
		Step30: func(data []byte, req any) (any, error) {
			request, ok := req.(stepio.Step30Request)
			if !ok {
				return nil, fmt.Errorf("orchestrator: step30 decoder expects Step30Request, got %T", req)
			}
			return stepio.DecodeAndValidateStep30Response(data, request)
		},
		Step40: func(data []byte, req any) (any, error) {
			request, ok := req.(stepio.Step40Request)
			if !ok {
				return nil, fmt.Errorf("orchestrator: step40 decoder expects Step40Request, got %T", req)
			}
			return stepio.DecodeAndValidateStep40Response(data, request)
		},
		Step50: func(data []byte, req any) (any, error) {
			request, ok := req.(stepio.Step50Request)
			if !ok {
				return nil, fmt.Errorf("orchestrator: step50 decoder expects Step50Request, got %T", req)
			}
			return stepio.DecodeAndValidateStep50Response(data, request)
		},
		Step60: func(data []byte, req any) (any, error) {
			request, ok := req.(stepio.Step60Request)
			if !ok {
				return nil, fmt.Errorf("orchestrator: step60 decoder expects Step60Request, got %T", req)
			}
			return stepio.DecodeAndValidateStep60Response(data, request)
		},
		Step70: func(data []byte, req any) (any, error) {
			request, ok := req.(stepio.Step70Request)
			if !ok {
				return nil, fmt.Errorf("orchestrator: step70 decoder expects Step70Request, got %T", req)
			}
			return stepio.DecodeAndValidateStep70Response(data, request)
		},
	}
	return &Orchestrator{
		cfg:      cfg,
		logger:   ilog.New(slog.LevelInfo),
		decoders: decoders,
		steps:    defaultSteps(cfg, decoders),
	}, nil
}

func (o *Orchestrator) Run(ctx context.Context, pr int, opts RunOptions) error {
	if pr <= 0 {
		return fmt.Errorf("orchestrator: pr must be > 0: pr=%d", pr)
	}
	runsBase, err := o.cfg.RunsBase()
	if err != nil {
		return err
	}
	sentinel, blocked, err := firstNeedsRecoverySentinel(runsBase)
	if err != nil {
		return err
	}
	if blocked {
		return &GlobalNeedsRecoveryError{Sentinel: sentinel}
	}

	selection, err := o.selectRun(pr, opts)
	if err != nil {
		return err
	}

	o.runContext = selection.runContext
	o.stateWriter = state.NewWriter(selection.runContext)

	run := &StepRunContext{
		Config:        o.cfg,
		Logger:        o.logger.With(slog.String(ilog.FieldRunID, string(selection.runContext.RunID))),
		PR:            pr,
		IO:            selection.runContext,
		IntentionFile: NewIntentionStore(selection.runContext),
	}

	if err := o.ensureRunScaffold(run); err != nil {
		return err
	}
	if selection.fresh {
		if err := o.ensureNoGlobalSentinel(run.IO); err != nil {
			return err
		}
		if err := o.appendState(startedEntry(pr, selection.runContext.RunID, time.Now().UTC())); err != nil {
			return err
		}
	}

	if err := o.loadPersistedArtifacts(run); err != nil {
		return err
	}

	start, err := o.resolveStartStep(run)
	if err != nil {
		if errors.Is(err, errNoScorableAgentsResume) {
			if err := o.appendState(failedEntry(pr, run.IO.RunID, contracts.FailedStep30, "no_scorable_agents", "step30 resume selected without any scorable pass1 manifests", time.Now().UTC())); err != nil {
				return err
			}
			return nil
		}
		return err
	}

	preserveWorktrees := true
	defer func() {
		if preserveWorktrees {
			return
		}
		_ = cleanupWorktrees(run.IO, run.TaskPackage)
	}()

	for _, step := range pipelineFrom(start) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := o.ensureNoGlobalSentinel(run.IO); err != nil {
			return err
		}
		switch step {
		case contracts.FailedStep10:
			if err := o.runStep10(ctx, run); err != nil {
				return err
			}
		case contracts.FailedStep20:
			if err := o.runParallel(ctx, run, 1, contracts.FailedStep20, o.steps.Step20); err != nil {
				if errors.Is(err, errStopPipeline) {
					return nil
				}
				return err
			}
			if err := o.appendState(stepDoneEntry(pr, run.IO.RunID, contracts.FailedStep20, time.Now().UTC())); err != nil {
				return err
			}
			scorableAgents, err := scorableAgentsForPass(run.IO, run.TaskPackage, 1)
			if err != nil {
				return err
			}
			if len(scorableAgents) == 0 {
				if err := o.appendState(failedEntry(pr, run.IO.RunID, contracts.FailedStep20, "no_scorable_agents", "step20 completed without any scorable pass1 manifests", time.Now().UTC())); err != nil {
					return err
				}
				return nil
			}
		case contracts.FailedStep30:
			if err := o.runSingle(ctx, run, contracts.FailedStep30, o.steps.Step30); err != nil {
				return err
			}
			if err := o.appendState(stepDoneEntry(pr, run.IO.RunID, contracts.FailedStep30, time.Now().UTC())); err != nil {
				return err
			}
		case contracts.FailedStep40:
			if err := o.runSingle(ctx, run, contracts.FailedStep40, o.steps.Step40); err != nil {
				return err
			}
			if err := o.appendState(stepDoneEntry(pr, run.IO.RunID, contracts.FailedStep40, time.Now().UTC())); err != nil {
				return err
			}
		case contracts.FailedStep50:
			if noActionableCandidates(run.Candidates) {
				continue
			}
			if err := o.runParallel(ctx, run, 2, contracts.FailedStep50, o.steps.Step50); err != nil {
				if errors.Is(err, errStopPipeline) {
					return nil
				}
				return err
			}
			if err := o.appendState(stepDoneEntry(pr, run.IO.RunID, contracts.FailedStep50, time.Now().UTC())); err != nil {
				return err
			}
		case contracts.FailedStep60:
			if noActionableCandidates(run.Candidates) {
				continue
			}
			if err := o.runSingle(ctx, run, contracts.FailedStep60, o.steps.Step60); err != nil {
				return err
			}
			if err := o.appendState(stepDoneEntry(pr, run.IO.RunID, contracts.FailedStep60, time.Now().UTC())); err != nil {
				return err
			}
		case contracts.FailedStep70:
			if err := o.runSingle(ctx, run, contracts.FailedStep70, o.steps.Step70); err != nil {
				switch {
				case errors.Is(err, step70_decide.ErrBlockedBySentinel):
					if appendErr := o.appendInterrupted(run.PR, run.IO.RunID, contracts.FailedStep70, contracts.InterruptedReasonUnknown, "step70 blocked by needs-recovery sentinel"); appendErr != nil {
						return appendErr
					}
					return nil
				case errors.Is(err, step70_decide.ErrNeedsManualRecovery):
					if appendErr := o.ensureStep70NeedsManualRecoveryState(run); appendErr != nil {
						return appendErr
					}
					return nil
				}
				return err
			}
			terminal, err := hasTerminalEvent(run.IO, run.IO.RunID)
			if err != nil {
				return err
			}
			if !terminal {
				if err := o.appendState(stepDoneEntry(pr, run.IO.RunID, contracts.FailedStep70, time.Now().UTC())); err != nil {
					return err
				}
			}
			if err := o.appendTerminalDecision(run); err != nil {
				return err
			}
			if err := o.runSingle(ctx, run, contracts.FailedStep70, o.steps.Archive); err != nil {
				return err
			}
			preserveWorktrees = false
		}
		if err := o.loadPersistedArtifacts(run); err != nil {
			return err
		}
	}

	return nil
}

func (o *Orchestrator) appendState(entry contracts.StateEntry) error {
	if o.stateWriter == (state.Writer{}) {
		return errors.New("orchestrator: state writer is not initialized")
	}
	return o.stateWriter.Append(entry)
}

func (o *Orchestrator) selectRun(pr int, opts RunOptions) (runSelection, error) {
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

	action := latest.Action
	switch action {
	case state.NextActionResume:
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

func newFreshSelection(pr int, opts RunOptions, runsBase, worktreeBase string) (runSelection, error) {
	runID := opts.RunID
	if runID == "" {
		runID = internalio.NewRunID(pr)
	}
	runCtx, err := internalio.NewRunContext(runID, runsBase, worktreeBase)
	if err != nil {
		return runSelection{}, err
	}
	return runSelection{
		runContext: runCtx,
		fresh:      true,
	}, nil
}

func loadRunContext(runID contracts.RunID, runsBase, worktreeBase string) (internalio.RunContext, error) {
	runCtx, err := internalio.NewRunContext(runID, runsBase, worktreeBase)
	if err != nil {
		return internalio.RunContext{}, err
	}
	taskPackagePath := runCtx.TaskPackagePath()
	if !fileExists(taskPackagePath) {
		return runCtx, nil
	}
	pkg, err := internalio.ReadJSON[contracts.TaskPackage](taskPackagePath)
	if err != nil {
		return internalio.RunContext{}, err
	}
	if pkg.RunID != runID {
		return internalio.RunContext{}, fmt.Errorf("orchestrator: task package run_id mismatch: selected=%s package=%s", runID, pkg.RunID)
	}
	return internalio.RunContextFromTaskPackage(pkg, runsBase, worktreeBase)
}

func (o *Orchestrator) ensureRunScaffold(run *StepRunContext) error {
	if err := os.MkdirAll(run.IO.RunDir(), 0o755); err != nil {
		return err
	}
	return writeConfigSnapshot(filepath.Join(run.IO.RunDir(), "config.snapshot.yaml"), o.cfg)
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
		candidates, err := internalio.ReadJSON[contracts.Candidates](candidatesPath)
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
		decision, err := internalio.ReadJSON[contracts.Decision](decisionPath)
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
		return contracts.FailedStep60, nil
	}
	if done, err := taskPackageHasAllManifests(run.IO, 2, run.TaskPackage); err != nil {
		return "", err
	} else if done {
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
			stepRun := *run
			stepRun.Step = step
			stepRun.Pass = pass
			stepRun.Agent = agent
			errCh <- parallelResult{agent: agent, err: runner.Run(ctx, &stepRun)}
		}()
	}
	wg.Wait()
	close(errCh)
	if blockedErr != nil {
		return blockedErr
	}

	rescueExhausted := make([]stepio.RescueExhausted, 0, len(agents))
	var interruptedDetail string
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
		return result.err
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
		decision, err := internalio.ReadJSON[contracts.Decision](decisionPath)
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
	data, err := yaml.Marshal(cfg)
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

type runSelection struct {
	runContext internalio.RunContext
	fresh      bool
}

func stateRunID(entry contracts.StateEntry) (contracts.RunID, bool) {
	switch value := entry.Value.(type) {
	case contracts.StateEntryStarted:
		return value.RunID, true
	case *contracts.StateEntryStarted:
		if value != nil {
			return value.RunID, true
		}
	case contracts.StateEntryStepDone:
		return value.RunID, true
	case *contracts.StateEntryStepDone:
		if value != nil {
			return value.RunID, true
		}
	case contracts.StateEntryInterrupted:
		return value.RunID, true
	case *contracts.StateEntryInterrupted:
		if value != nil {
			return value.RunID, true
		}
	case contracts.StateEntryPromoting:
		return value.RunID, true
	case *contracts.StateEntryPromoting:
		if value != nil {
			return value.RunID, true
		}
	case contracts.StateEntryWarning:
		if value.RunID != nil {
			return *value.RunID, true
		}
	case *contracts.StateEntryWarning:
		if value != nil && value.RunID != nil {
			return *value.RunID, true
		}
	case contracts.StateEntryCompleted:
		return value.RunID, true
	case *contracts.StateEntryCompleted:
		if value != nil {
			return value.RunID, true
		}
	case contracts.StateEntryFailed:
		return value.RunID, true
	case *contracts.StateEntryFailed:
		if value != nil {
			return value.RunID, true
		}
	case contracts.StateEntryPromoted:
		return value.RunID, true
	case *contracts.StateEntryPromoted:
		if value != nil {
			return value.RunID, true
		}
	case contracts.StateEntryRollback:
		return value.RunID, true
	case *contracts.StateEntryRollback:
		if value != nil {
			return value.RunID, true
		}
	case contracts.StateEntrySkipped:
		return value.RunID, true
	case *contracts.StateEntrySkipped:
		if value != nil {
			return value.RunID, true
		}
	case contracts.StateEntryTimeout:
		return value.RunID, true
	case *contracts.StateEntryTimeout:
		if value != nil {
			return value.RunID, true
		}
	case contracts.StateEntryNeedsManualRecovery:
		return value.RunID, true
	case *contracts.StateEntryNeedsManualRecovery:
		if value != nil {
			return value.RunID, true
		}
	}
	return "", false
}

func startedEntry(pr int, runID contracts.RunID, at time.Time) contracts.StateEntry {
	value := contracts.StateEntryStarted{
		Kind:  contracts.StateKindStarted,
		PR:    pr,
		RunID: runID,
		Step:  contracts.FailedStep10,
		At:    at,
	}
	return contracts.StateEntry{Kind: contracts.StateKindStarted, Value: value}
}

func stepDoneEntry(pr int, runID contracts.RunID, step contracts.FailedStep, at time.Time) contracts.StateEntry {
	value := contracts.StateEntryStepDone{
		Kind:  contracts.StateKindStepDone,
		PR:    pr,
		RunID: runID,
		Step:  step,
		At:    at,
	}
	return contracts.StateEntry{Kind: contracts.StateKindStepDone, Value: value}
}

func completedEntry(pr int, runID contracts.RunID, step contracts.FailedStep, at time.Time) contracts.StateEntry {
	value := contracts.StateEntryCompleted{
		Kind:  contracts.StateKindCompleted,
		PR:    pr,
		RunID: runID,
		Step:  step,
		At:    at,
	}
	return contracts.StateEntry{Kind: contracts.StateKindCompleted, Value: value}
}

func failedEntry(pr int, runID contracts.RunID, step contracts.FailedStep, reason, detail string, at time.Time) contracts.StateEntry {
	value := contracts.StateEntryFailed{
		Kind:   contracts.StateKindFailed,
		PR:     pr,
		RunID:  runID,
		Step:   step,
		Reason: reason,
		Detail: detail,
		At:     at,
	}
	return contracts.StateEntry{Kind: value.Kind, Value: value}
}

func promotedEntry(pr int, runID contracts.RunID, at time.Time) contracts.StateEntry {
	value := contracts.StateEntryPromoted{
		Kind:  contracts.StateKindPromoted,
		PR:    pr,
		RunID: runID,
		Step:  contracts.FailedStep70,
		At:    at,
	}
	return contracts.StateEntry{Kind: contracts.StateKindPromoted, Value: value}
}

func rollbackEntry(pr int, runID contracts.RunID, reason contracts.RollbackReason, failedStep contracts.FailedStep, at time.Time) contracts.StateEntry {
	value := contracts.StateEntryRollback{
		Kind:           contracts.StateKindRollback,
		PR:             pr,
		RunID:          runID,
		Step:           contracts.FailedStep70,
		RollbackReason: reason,
		FailedStep:     failedStep,
		At:             at,
	}
	return contracts.StateEntry{Kind: contracts.StateKindRollback, Value: value}
}

func noActionableCandidates(candidates *contracts.Candidates) bool {
	return len(actionableCandidateIDs(candidates)) == 0
}

func hasFinalizedManifest(runIO internalio.RunContext, pass int, agent contracts.AgentID) (bool, error) {
	manifest, err := internalio.LoadFinalizedManifest(runIO, pass, agent)
	if err == nil && manifest != nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func scorableAgentsForPass(runIO internalio.RunContext, pkg *contracts.TaskPackage, pass int) ([]contracts.AgentID, error) {
	if pkg == nil {
		return nil, errors.New("orchestrator: task package is required")
	}
	agents := make([]contracts.AgentID, 0, len(pkg.Worktrees))
	seen := make(map[contracts.AgentID]struct{}, len(pkg.Worktrees))
	for _, wt := range pkg.Worktrees {
		if wt.Pass != pass {
			continue
		}
		if _, ok := seen[wt.Agent]; ok {
			continue
		}
		manifest, err := internalio.LoadScorableManifest(runIO, pass, wt.Agent)
		if err != nil {
			if shouldSkipScorableManifest(err) || os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		if manifest == nil {
			continue
		}
		seen[wt.Agent] = struct{}{}
		agents = append(agents, wt.Agent)
	}
	return agents, nil
}

func shouldSkipScorableManifest(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, internalio.ErrNotScorable) ||
		errors.Is(err, contracts.ErrDuplicateJSONKey) ||
		errors.Is(err, contracts.ErrTrailingJSON) ||
		errors.Is(err, contracts.ErrUnknownManifestKind) {
		return true
	}
	return strings.Contains(err.Error(), "Field validation")
}

func (o *Orchestrator) validateImplementationBoundary(run *StepRunContext, pass int, agents []contracts.AgentID) error {
	if run == nil || run.TaskPackage == nil {
		return errors.New("orchestrator: task package is required")
	}
	if run.Config == nil {
		return errors.New("orchestrator: config is required")
	}
	results := make([]stepio.Step20AgentResult, 0, len(agents))
	for _, agent := range agents {
		manifest, err := internalio.LoadFinalizedManifest(run.IO, pass, agent)
		if err != nil {
			return fmt.Errorf("orchestrator: load finalized manifest pass=%d agent=%s: %w", pass, agent, err)
		}
		results = append(results, stepio.Step20AgentResult{
			Agent:    agent,
			Manifest: *manifest,
		})
	}

	switch pass {
	case 1:
		if o.decoders.Step20 == nil {
			return nil
		}
		timeout := run.Config.StepTimeouts["step20"]
		req := stepio.Step20Request{
			TaskPackage:    *run.TaskPackage,
			Agents:         append([]contracts.AgentID(nil), agents...),
			TimeoutSeconds: timeout,
		}
		resp, err := stepio.NewStep20Response(results, nil, req)
		if err != nil {
			return err
		}
		payload, err := resp.MarshalJSON()
		if err != nil {
			return err
		}
		_, err = o.decoders.Step20(payload, req)
		return err
	case 2:
		if o.decoders.Step50 == nil {
			return nil
		}
		timeout := run.Config.StepTimeouts["step50"]
		req := stepio.Step50Request{
			TaskPackage:      *run.TaskPackage,
			Agents:           append([]contracts.AgentID(nil), agents...),
			TimeoutSeconds:   timeout,
			CandidateRuleIDs: candidateRuleIDs(run.Candidates),
		}
		resp, err := stepio.NewStep50Response(results, nil, req)
		if err != nil {
			return err
		}
		payload, err := resp.MarshalJSON()
		if err != nil {
			return err
		}
		_, err = o.decoders.Step50(payload, req)
		return err
	default:
		return fmt.Errorf("orchestrator: unsupported implementation pass=%d", pass)
	}
}

func candidateRuleIDs(candidates *contracts.Candidates) []string {
	return actionableCandidateIDs(candidates)
}

func actionableCandidateIDs(candidates *contracts.Candidates) []string {
	if candidates == nil || len(candidates.Candidates) == 0 {
		return nil
	}
	ids := make([]string, 0, len(candidates.Candidates))
	for _, candidate := range candidates.Candidates {
		if candidate.Kind == contracts.CandidateKindDuplicate {
			continue
		}
		ids = append(ids, candidate.CandidateID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func (o *Orchestrator) appendInterrupted(pr int, runID contracts.RunID, step contracts.FailedStep, reason contracts.InterruptedReason, detail string) error {
	value := contracts.StateEntryInterrupted{
		Kind:   contracts.StateKindInterrupted,
		PR:     pr,
		RunID:  runID,
		Step:   step,
		Reason: reason,
		Detail: detail,
		At:     time.Now().UTC(),
	}
	return o.appendState(contracts.StateEntry{Kind: value.Kind, Value: value})
}

func (o *Orchestrator) handleRescueExhausted(run *StepRunContext, step contracts.FailedStep, exhausted []stepio.RescueExhausted) error {
	now := time.Now().UTC()
	manual := contracts.StateEntryNeedsManualRecovery{
		Kind:       contracts.StateKindNeedsManualRecovery,
		PR:         run.PR,
		RunID:      run.IO.RunID,
		Step:       step,
		Reason:     contracts.RollbackReasonWorktreeRescueLoop,
		FailedStep: step,
		At:         now,
	}
	if err := o.appendState(contracts.StateEntry{Kind: manual.Kind, Value: manual}); err != nil {
		return err
	}
	for _, item := range exhausted {
		pr := run.PR
		runID := run.IO.RunID
		failedStep := step
		detail := fmt.Sprintf("agent=%s retry_count=%d", item.Agent, item.RetryCount)
		warning := contracts.StateEntryWarning{
			Kind:   contracts.StateKindWarningRescueRetry,
			PR:     &pr,
			RunID:  &runID,
			Step:   &failedStep,
			Detail: detail,
			At:     now,
		}
		if err := o.appendState(contracts.StateEntry{Kind: warning.Kind, Value: warning}); err != nil {
			return err
		}
	}
	return nil
}

func (o *Orchestrator) ensureNoGlobalSentinel(runCtx internalio.RunContext) error {
	sentinel, blocked, err := firstNeedsRecoverySentinel(runCtx.RunsBase)
	if err != nil {
		return err
	}
	if !blocked {
		return nil
	}
	return &GlobalNeedsRecoveryError{Sentinel: sentinel}
}

func (o *Orchestrator) ensureStep70NeedsManualRecoveryState(run *StepRunContext) error {
	reason := contracts.RollbackReasonTransactionalFailure
	failedStep := contracts.FailedStep70
	if run.Intention != nil {
		if run.Intention.RecoveryReason != "" {
			reason = run.Intention.RecoveryReason
		}
		if run.Intention.FailedStep != "" {
			failedStep = run.Intention.FailedStep
		}
	}
	if err := ensureNeedsRecoverySentinel(run.IO, run.PR, run.IO.RunID, reason, failedStep); err != nil {
		return err
	}
	entries, err := state.ScanEventsForRun(run.IO, run.IO.RunID)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Kind == contracts.StateKindNeedsManualRecovery {
			return nil
		}
	}
	value := contracts.StateEntryNeedsManualRecovery{
		Kind:       contracts.StateKindNeedsManualRecovery,
		PR:         run.PR,
		RunID:      run.IO.RunID,
		Step:       contracts.FailedStep70,
		Reason:     reason,
		FailedStep: failedStep,
		At:         time.Now().UTC(),
	}
	return o.appendState(contracts.StateEntry{Kind: value.Kind, Value: value})
}

func ensureNeedsRecoverySentinelFromState(runCtx internalio.RunContext, entry *contracts.StateEntry) error {
	if entry == nil {
		return nil
	}
	switch value := entry.Value.(type) {
	case contracts.StateEntryNeedsManualRecovery:
		if value.Step != contracts.FailedStep70 || value.Reason == contracts.RollbackReasonWorktreeRescueLoop {
			return nil
		}
		return ensureNeedsRecoverySentinel(runCtx, value.PR, value.RunID, value.Reason, value.FailedStep)
	case *contracts.StateEntryNeedsManualRecovery:
		if value == nil {
			return nil
		}
		if value.Step != contracts.FailedStep70 || value.Reason == contracts.RollbackReasonWorktreeRescueLoop {
			return nil
		}
		return ensureNeedsRecoverySentinel(runCtx, value.PR, value.RunID, value.Reason, value.FailedStep)
	default:
		return nil
	}
}

func ensureNeedsRecoverySentinel(runCtx internalio.RunContext, pr int, runID contracts.RunID, reason contracts.RollbackReason, failedStep contracts.FailedStep) error {
	path := filepath.Join(runCtx.RunsBase, "needs-recovery", string(runID)+".json")
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	sentinel := contracts.NeedsRecoverySentinel{
		RunID:      runID,
		PR:         pr,
		Reason:     reason,
		FailedStep: failedStep,
		CreatedAt:  time.Now().UTC(),
	}
	if err := sentinel.Validate(); err != nil {
		return err
	}
	return internalio.WriteJSONAtomic(path, sentinel)
}

func firstNeedsRecoverySentinel(runsBase string) (contracts.NeedsRecoverySentinel, bool, error) {
	processedPath := filepath.Join(runsBase, "processed.jsonl")
	manualRuns, err := state.NeedsManualRecoveryRunsPath(processedPath)
	if err != nil {
		return contracts.NeedsRecoverySentinel{}, false, err
	}
	for _, run := range manualRuns {
		sentinel, ok, err := ensureNeedsRecoverySentinelFromLatestRun(runsBase, run)
		if err != nil {
			return contracts.NeedsRecoverySentinel{}, false, err
		}
		if ok {
			return sentinel, true, nil
		}
	}

	dir := filepath.Join(runsBase, "needs-recovery")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return contracts.NeedsRecoverySentinel{}, false, nil
		}
		return contracts.NeedsRecoverySentinel{}, false, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".aborted.json") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		sentinel, err := internalio.ReadJSON[contracts.NeedsRecoverySentinel](filepath.Join(dir, name))
		if err != nil {
			return contracts.NeedsRecoverySentinel{RunID: sentinelRunIDFromFilename(name)}, true, nil
		}
		return sentinel, true, nil
	}
	return contracts.NeedsRecoverySentinel{}, false, nil
}

func ensureNeedsRecoverySentinelFromLatestRun(runsBase string, latest state.LatestRun) (contracts.NeedsRecoverySentinel, bool, error) {
	if latest.LastEvent == nil || latest.Action != state.NextActionNeedsManualRecovery {
		return contracts.NeedsRecoverySentinel{}, false, nil
	}
	var sentinel contracts.NeedsRecoverySentinel
	switch value := latest.LastEvent.Value.(type) {
	case contracts.StateEntryNeedsManualRecovery:
		if value.Step != contracts.FailedStep70 || value.Reason == contracts.RollbackReasonWorktreeRescueLoop {
			return contracts.NeedsRecoverySentinel{}, false, nil
		}
		sentinel = contracts.NeedsRecoverySentinel{
			RunID:      value.RunID,
			PR:         value.PR,
			Reason:     value.Reason,
			FailedStep: value.FailedStep,
			CreatedAt:  value.At,
		}
	case *contracts.StateEntryNeedsManualRecovery:
		if value == nil {
			return contracts.NeedsRecoverySentinel{}, false, nil
		}
		if value.Step != contracts.FailedStep70 || value.Reason == contracts.RollbackReasonWorktreeRescueLoop {
			return contracts.NeedsRecoverySentinel{}, false, nil
		}
		sentinel = contracts.NeedsRecoverySentinel{
			RunID:      value.RunID,
			PR:         value.PR,
			Reason:     value.Reason,
			FailedStep: value.FailedStep,
			CreatedAt:  value.At,
		}
	default:
		return contracts.NeedsRecoverySentinel{}, false, nil
	}
	if err := sentinel.Validate(); err != nil {
		return contracts.NeedsRecoverySentinel{}, false, err
	}
	path := filepath.Join(runsBase, "needs-recovery", string(sentinel.RunID)+".json")
	if _, err := os.Stat(path); err != nil {
		if !os.IsNotExist(err) {
			return contracts.NeedsRecoverySentinel{}, false, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return contracts.NeedsRecoverySentinel{}, false, err
		}
		if err := internalio.WriteJSONAtomic(path, sentinel); err != nil {
			return contracts.NeedsRecoverySentinel{}, false, err
		}
	}
	return sentinel, true, nil
}

func hasTerminalEvent(runCtx internalio.RunContext, runID contracts.RunID) (bool, error) {
	events, err := state.ScanEventsForRun(runCtx, runID)
	if err != nil {
		return false, err
	}
	for _, entry := range events {
		if entry.Kind.IsTerminal() {
			return true, nil
		}
	}
	return false, nil
}

func sentinelRunIDFromFilename(name string) contracts.RunID {
	name = strings.TrimSuffix(name, ".aborted.json")
	name = strings.TrimSuffix(name, ".json")
	return contracts.RunID(name)
}
