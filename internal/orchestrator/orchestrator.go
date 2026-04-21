package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	ilog "github.com/nishimoto265/auto-improve/internal/logger"
	"github.com/nishimoto265/auto-improve/internal/state"
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

func NewOrchestrator(cfg *config.Config) (*Orchestrator, error) {
	if cfg == nil {
		return nil, errors.New("orchestrator: config is required")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Orchestrator{
		cfg:      cfg,
		logger:   ilog.New(slog.LevelInfo),
		decoders: ContractDecoders{},
		steps:    defaultSteps(cfg),
	}, nil
}

func (o *Orchestrator) Run(ctx context.Context, pr int, opts RunOptions) error {
	if pr <= 0 {
		return fmt.Errorf("orchestrator: pr must be > 0: pr=%d", pr)
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
		if err := o.appendState(startedEntry(pr, selection.runContext.RunID, time.Now().UTC())); err != nil {
			return err
		}
	}

	if err := o.loadPersistedArtifacts(run); err != nil {
		return err
	}

	start, err := o.resolveStartStep(run)
	if err != nil {
		return err
	}

	preserveWorktrees := true
	defer func() {
		if preserveWorktrees {
			return
		}
		_ = cleanupWorktrees(run.TaskPackage)
	}()

	for _, step := range pipelineFrom(start) {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch step {
		case contracts.FailedStep10:
			if err := o.runStep10(ctx, run); err != nil {
				return err
			}
		case contracts.FailedStep20:
			if err := o.runParallel(ctx, run, 1, contracts.FailedStep20, o.steps.Step20); err != nil {
				return err
			}
			if err := o.appendState(stepDoneEntry(pr, run.IO.RunID, contracts.FailedStep20, time.Now().UTC())); err != nil {
				return err
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
			if err := o.runParallel(ctx, run, 2, contracts.FailedStep50, o.steps.Step50); err != nil {
				return err
			}
			if err := o.appendState(stepDoneEntry(pr, run.IO.RunID, contracts.FailedStep50, time.Now().UTC())); err != nil {
				return err
			}
		case contracts.FailedStep60:
			if err := o.runSingle(ctx, run, contracts.FailedStep60, o.steps.Step60); err != nil {
				return err
			}
			if err := o.appendState(stepDoneEntry(pr, run.IO.RunID, contracts.FailedStep60, time.Now().UTC())); err != nil {
				return err
			}
		case contracts.FailedStep70:
			if err := o.runSingle(ctx, run, contracts.FailedStep70, o.steps.Step70); err != nil {
				return err
			}
			if err := o.appendState(stepDoneEntry(pr, run.IO.RunID, contracts.FailedStep70, time.Now().UTC())); err != nil {
				return err
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

	action := state.NextActionForEntry(latest.LastEvent)
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
		return contracts.FailedStep70, nil
	}
	if taskPackageHasAllManifests(run.IO, 2, run.TaskPackage) {
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
		return contracts.FailedStep40, nil
	}
	if taskPackageHasAllManifests(run.IO, 1, run.TaskPackage) {
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

	errCh := make(chan error, len(agents))
	var wg sync.WaitGroup
	for _, agent := range agents {
		runner, ok := runners[agent]
		if !ok || runner == nil {
			return fmt.Errorf("orchestrator: missing runner for step %s agent %s", step, agent)
		}
		agent := agent
		wg.Add(1)
		go func() {
			defer wg.Done()
			stepRun := *run
			stepRun.Step = step
			stepRun.Pass = pass
			stepRun.Agent = agent
			errCh <- runner.Run(ctx, &stepRun)
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			return err
		}
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
	if n := len(entries); n > 0 && entries[n-1].Kind.IsTerminal() {
		return nil
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

func taskPackageHasAllManifests(runCtx internalio.RunContext, pass int, pkg *contracts.TaskPackage) bool {
	for _, agent := range passAgents(pkg, pass) {
		path, err := runCtx.ManifestPath(pass, agent)
		if err != nil || !fileExists(path) {
			return false
		}
	}
	return pkg != nil
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
