package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/contracts/stepio"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	ilog "github.com/nishimoto265/auto-improve/internal/logger"
	"github.com/nishimoto265/auto-improve/internal/state"
	"github.com/nishimoto265/auto-improve/internal/steps/agentrunner"
	"github.com/nishimoto265/auto-improve/internal/steps/step20_implement"
	"github.com/nishimoto265/auto-improve/internal/steps/step50_implement"
	"gopkg.in/yaml.v3"
)

var defaultAgents = []contracts.AgentID{"a1", "a2", "a3"}

type RunOptions struct {
	RunID       contracts.RunID
	FromScratch bool
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
	runMu       sync.Mutex
	running     bool
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
var errConcurrentRun = errors.New("orchestrator: concurrent Run() on the same instance is not allowed")
var errConcurrentPRRun = errors.New("orchestrator: another process is already running this PR")

var beforeFreshRunGateHook = func(*StepRunContext) error { return nil }
var beforeRunScaffoldHook = func(*StepRunContext) error { return nil }
var beforeStartedAppendHook = func(*StepRunContext) error { return nil }

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

type GlobalSunsetSentinelError struct {
	Path string
}

func (e *GlobalSunsetSentinelError) Error() string {
	return fmt.Sprintf("orchestrator: global sunset block: sentinel=%s", e.Path)
}

func CheckGlobalRecoveryGate(runsBase string) error {
	sentinel, blocked, err := firstNeedsRecoverySentinel(runsBase)
	if err != nil {
		return err
	}
	if blocked {
		return &GlobalNeedsRecoveryError{Sentinel: sentinel}
	}
	path, blocked, err := firstSunsetSentinel(runsBase)
	if err != nil {
		return err
	}
	if blocked {
		return &GlobalSunsetSentinelError{Path: path}
	}
	return nil
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
	if err := o.enterRun(); err != nil {
		return err
	}
	defer o.leaveRun()

	return o.runCycle(ctx, pr, opts)
}

func (o *Orchestrator) appendState(entry contracts.StateEntry) error {
	if o.stateWriter == (state.Writer{}) {
		return errors.New("orchestrator: state writer is not initialized")
	}
	return o.stateWriter.Append(entry)
}

func (o *Orchestrator) enterRun() error {
	o.runMu.Lock()
	defer o.runMu.Unlock()
	if o.running {
		return errConcurrentRun
	}
	o.running = true
	return nil
}

func (o *Orchestrator) leaveRun() {
	o.runMu.Lock()
	defer o.runMu.Unlock()
	o.running = false
}

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
		if err := o.supersedeNonTerminalRun(ctx, pr, latest, runsBase, worktreeBase); err != nil {
			return runSelection{}, err
		}
		freshOpts := opts
		freshOpts.RunID = ""
		return newFreshSelection(pr, freshOpts, runsBase, worktreeBase)
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

func (o *Orchestrator) supersedeNonTerminalRun(ctx context.Context, pr int, latest state.LatestRun, runsBase, worktreeBase string) error {
	if latest.LastEvent == nil || latest.LastEvent.Kind.IsTerminal() {
		return nil
	}
	runID, ok := stateRunID(*latest.LastEvent)
	if !ok {
		return errors.New("orchestrator: latest non-terminal event is missing run_id")
	}
	runCtx, err := loadRunContext(runID, runsBase, worktreeBase)
	if err != nil {
		return err
	}
	var pkg *contracts.TaskPackage
	if fileExists(runCtx.TaskPackagePath()) {
		loaded, err := internalio.ReadJSON[contracts.TaskPackage](runCtx.TaskPackagePath())
		if err != nil {
			return err
		}
		pkg = &loaded
	}
	step := latest.Step
	if step == "" {
		step = contracts.FailedStep10
	}
	if step == contracts.FailedStep70 {
		return fmt.Errorf("orchestrator: --from-scratch refused for run_id=%s with unfinished step70; resume or recover first", runID)
	}
	if has, err := hasPersistedIntention(runCtx); err != nil {
		return err
	} else if has {
		return fmt.Errorf("orchestrator: --from-scratch refused for run_id=%s with persisted step70 intention; resume or recover first", runID)
	}
	repoRoot, err := o.cfg.RepoRoot()
	if err != nil {
		return err
	}
	value := contracts.StateEntrySkipped{
		Kind:   contracts.StateKindSkipped,
		PR:     pr,
		RunID:  runID,
		Step:   step,
		Detail: "superseded_by_from_scratch",
		At:     time.Now().UTC(),
	}
	if err := state.NewWriter(runCtx).Append(contracts.StateEntry{Kind: value.Kind, Value: value}); err != nil {
		return err
	}
	return cleanupWorktreesWithGit(ctx, runCtx, pkg, repoRoot)
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
		return result.err
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

func interruptedReasonFromContext(ctx context.Context, err error) contracts.InterruptedReason {
	if cause := context.Cause(ctx); cause != nil && strings.HasPrefix(cause.Error(), "signal:") {
		return contracts.InterruptedReasonSignal
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return contracts.InterruptedReasonContext
	case errors.Is(err, context.Canceled):
		return contracts.InterruptedReasonContext
	default:
		return contracts.InterruptedReasonUnknown
	}
}

func (o *Orchestrator) handleContextCancellation(ctx context.Context, run *StepRunContext, step contracts.FailedStep, err error) (bool, error) {
	if ctx.Err() == nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return false, nil
	}
	if appendErr := o.appendInterruptedIfContextDone(ctx, run, step, err); appendErr != nil {
		return true, appendErr
	}
	return true, nil
}

func (o *Orchestrator) appendInterruptedIfContextDone(ctx context.Context, run *StepRunContext, step contracts.FailedStep, err error) error {
	terminal, terminalErr := hasTerminalEvent(run.IO, run.IO.RunID)
	if terminalErr != nil {
		return terminalErr
	}
	if terminal {
		return nil
	}
	detail := err.Error()
	if cause := context.Cause(ctx); cause != nil {
		detail = cause.Error()
	}
	return o.appendInterrupted(run.PR, run.IO.RunID, step, interruptedReasonFromContext(ctx, err), detail)
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

func providerInterruptionFromNonScorableManifests(run *StepRunContext, pass int) (contracts.InterruptedReason, string, bool, error) {
	if run == nil || run.TaskPackage == nil {
		return "", "", false, errors.New("orchestrator: task package is required")
	}
	agents := passAgents(run.TaskPackage, pass)
	if len(agents) == 0 {
		return "", "", false, nil
	}
	var reason contracts.InterruptedReason
	details := make([]string, 0, len(agents))
	for _, agent := range agents {
		manifest, err := internalio.LoadFinalizedManifest(run.IO, pass, agent)
		if err != nil {
			return "", "", false, err
		}
		switch value := manifest.Value.(type) {
		case contracts.ManifestError:
			agentReason, ok := providerManifestReason(value.Reason)
			if !ok {
				return "", "", false, nil
			}
			if reason == "" {
				reason = agentReason
			} else if reason != agentReason {
				reason = contracts.InterruptedReasonUnknown
			}
			details = append(details, fmt.Sprintf("agent=%s reason=%s", agent, value.Reason))
		case *contracts.ManifestError:
			if value == nil {
				return "", "", false, nil
			}
			agentReason, ok := providerManifestReason(value.Reason)
			if !ok {
				return "", "", false, nil
			}
			if reason == "" {
				reason = agentReason
			} else if reason != agentReason {
				reason = contracts.InterruptedReasonUnknown
			}
			details = append(details, fmt.Sprintf("agent=%s reason=%s", agent, value.Reason))
		default:
			return "", "", false, nil
		}
	}
	if reason == "" {
		return "", "", false, nil
	}
	return reason, strings.Join(details, "; "), true, nil
}

func providerManifestReason(reason string) (contracts.InterruptedReason, bool) {
	switch reason {
	case string(contracts.InterruptedReasonRateLimit):
		return contracts.InterruptedReasonRateLimit, true
	case string(contracts.InterruptedReasonBudget):
		return contracts.InterruptedReasonBudget, true
	case string(contracts.InterruptedReasonContext):
		return contracts.InterruptedReasonContext, true
	case string(contracts.InterruptedReasonSignal):
		return contracts.InterruptedReasonSignal, true
	default:
		return "", false
	}
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
	if err := ensureNeedsRecoverySentinel(run.IO, run.PR, run.IO.RunID, manual.Reason, manual.FailedStep); err != nil {
		return err
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

func (o *Orchestrator) handleManualRecovery(
	run *StepRunContext,
	step contracts.FailedStep,
	reason contracts.RollbackReason,
	agent contracts.AgentID,
	detail string,
) error {
	entries, err := state.ScanEventsForRun(run.IO, run.IO.RunID)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Kind == contracts.StateKindNeedsManualRecovery {
			return ensureNeedsRecoverySentinelFromState(run.IO, &entry)
		}
	}
	if detail != "" {
		run.Logger.Warn("orchestrator: implementation rescue requires manual recovery", slog.String("agent", string(agent)), slog.String("detail", detail))
	}
	if step != contracts.FailedStep70 && reason != contracts.RollbackReasonWorktreeRescueLoop {
		reason = contracts.RollbackReasonWorktreeRescueLoop
	}
	value := contracts.StateEntryNeedsManualRecovery{
		Kind:       contracts.StateKindNeedsManualRecovery,
		PR:         run.PR,
		RunID:      run.IO.RunID,
		Step:       step,
		Reason:     reason,
		FailedStep: step,
		At:         time.Now().UTC(),
	}
	if err := ensureNeedsRecoverySentinel(run.IO, run.PR, run.IO.RunID, value.Reason, value.FailedStep); err != nil {
		return err
	}
	if err := o.appendState(contracts.StateEntry{Kind: value.Kind, Value: value}); err != nil {
		return err
	}
	return nil
}

func (o *Orchestrator) ensureNoGlobalSentinel(runCtx internalio.RunContext) error {
	return CheckGlobalRecoveryGate(runCtx.RunsBase)
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
	suppress, err := shouldSuppressNeedsRecoveryReconstruction(runCtx.RunsBase, runCtx.RunID)
	if err != nil {
		return err
	}
	if suppress {
		return nil
	}
	if _, exists, err := existingNeedsRecoverySentinelPath(runCtx.RunsBase, runCtx.RunID); err != nil {
		return err
	} else if exists {
		return nil
	}
	switch value := entry.Value.(type) {
	case contracts.StateEntryNeedsManualRecovery:
		return ensureNeedsRecoverySentinel(runCtx, value.PR, value.RunID, value.Reason, value.FailedStep)
	case *contracts.StateEntryNeedsManualRecovery:
		if value == nil {
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
		if contracts.IsNeedsRecoverySentinelFilename(name) {
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
	suppress, err := shouldSuppressNeedsRecoveryReconstruction(runsBase, sentinel.RunID)
	if err != nil {
		return contracts.NeedsRecoverySentinel{}, false, err
	}
	if suppress {
		return contracts.NeedsRecoverySentinel{}, false, nil
	}
	if err := sentinel.Validate(); err != nil {
		return contracts.NeedsRecoverySentinel{}, false, err
	}
	if _, exists, err := existingNeedsRecoverySentinelPath(runsBase, sentinel.RunID); err != nil {
		return contracts.NeedsRecoverySentinel{}, false, err
	} else if !exists {
		path := needsRecoverySentinelPath(runsBase, sentinel.RunID)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return contracts.NeedsRecoverySentinel{}, false, err
		}
		if err := internalio.WriteJSONAtomic(path, sentinel); err != nil {
			return contracts.NeedsRecoverySentinel{}, false, err
		}
	}
	return sentinel, true, nil
}

func firstSunsetSentinel(runsBase string) (string, bool, error) {
	for _, name := range []string{"sunset-running.marker.diverged", "sunset-running.marker"} {
		path := filepath.Join(runsBase, name)
		if _, err := os.Stat(path); err == nil {
			return name, true, nil
		} else if err != nil && !os.IsNotExist(err) {
			return "", false, err
		}
	}
	return "", false, nil
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
	return contracts.SentinelRunIDFromFilename(name)
}

func validatePersistedRunScopedArtifacts(runCtx internalio.RunContext) error {
	candidatesPath, err := runCtx.ResolveRunRelative("40/candidates.json")
	if err != nil {
		return err
	}
	if fileExists(candidatesPath) {
		if _, err := readCandidatesForRun(candidatesPath, runCtx.RunID); err != nil {
			return err
		}
	}
	decisionPath, err := runCtx.ResolveRunRelative("70/decision.json")
	if err != nil {
		return err
	}
	if fileExists(decisionPath) {
		if _, err := readDecisionForRun(decisionPath, runCtx.RunID); err != nil {
			return err
		}
	}
	intentionPath, err := runCtx.ResolveRunRelative("70/intention.json")
	if err != nil {
		return err
	}
	if fileExists(intentionPath) {
		if _, err := readIntentionRecordForRun(intentionPath, runCtx.RunID); err != nil {
			return err
		}
	}
	return nil
}

func readCandidatesForRun(path string, runID contracts.RunID) (contracts.Candidates, error) {
	candidates, err := internalio.ReadJSON[contracts.Candidates](path)
	if err != nil {
		return contracts.Candidates{}, err
	}
	if candidates.RunID != runID {
		return contracts.Candidates{}, fmt.Errorf("orchestrator: candidates run_id mismatch: expected=%s got=%s", runID, candidates.RunID)
	}
	return candidates, nil
}

func readDecisionForRun(path string, runID contracts.RunID) (contracts.Decision, error) {
	decision, err := internalio.ReadJSON[contracts.Decision](path)
	if err != nil {
		return contracts.Decision{}, err
	}
	if decisionRunID, ok := decisionRunID(decision); ok && decisionRunID != runID {
		return contracts.Decision{}, fmt.Errorf("orchestrator: decision run_id mismatch: expected=%s got=%s", runID, decisionRunID)
	}
	return decision, nil
}

func readIntentionRecordForRun(path string, runID contracts.RunID) (contracts.IntentionRecord, error) {
	record, err := internalio.ReadJSON[contracts.IntentionRecord](path)
	if err != nil {
		return contracts.IntentionRecord{}, err
	}
	if record.RunID != runID {
		return contracts.IntentionRecord{}, fmt.Errorf("orchestrator: intention run_id mismatch: expected=%s got=%s", runID, record.RunID)
	}
	return record, nil
}

func decisionRunID(decision contracts.Decision) (contracts.RunID, bool) {
	switch value := decision.Value.(type) {
	case contracts.DecisionAdopt:
		return value.RunID, true
	case *contracts.DecisionAdopt:
		if value != nil {
			return value.RunID, true
		}
	case contracts.DecisionRollback:
		return value.RunID, true
	case *contracts.DecisionRollback:
		if value != nil {
			return value.RunID, true
		}
	case contracts.DecisionNoop:
		return value.RunID, true
	case *contracts.DecisionNoop:
		if value != nil {
			return value.RunID, true
		}
	case contracts.DecisionReject:
		return value.RunID, true
	case *contracts.DecisionReject:
		if value != nil {
			return value.RunID, true
		}
	}
	return "", false
}

func needsRecoverySentinelPath(runsBase string, runID contracts.RunID) string {
	return filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelFilename(runID))
}

func needsRecoverySentinelAbortedPath(runsBase string, runID contracts.RunID) string {
	return filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelAbortedFilename(runID))
}

func needsRecoverySentinelClearedPath(runsBase string, runID contracts.RunID) string {
	return filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelClearedFilename(runID))
}

func existingNeedsRecoverySentinelPath(runsBase string, runID contracts.RunID) (string, bool, error) {
	for _, path := range []string{
		needsRecoverySentinelPath(runsBase, runID),
		needsRecoverySentinelAbortedPath(runsBase, runID),
	} {
		if _, err := os.Stat(path); err == nil {
			return path, true, nil
		} else if err != nil && !os.IsNotExist(err) {
			return "", false, err
		}
	}
	return "", false, nil
}

func shouldSuppressNeedsRecoveryReconstruction(runsBase string, runID contracts.RunID) (bool, error) {
	_, exists, err := existingNeedsRecoveryClearedMarker(runsBase, runID)
	return exists, err
}

func existingNeedsRecoveryClearedMarker(runsBase string, runID contracts.RunID) (string, bool, error) {
	path := needsRecoverySentinelClearedPath(runsBase, runID)
	if _, err := os.Stat(path); err == nil {
		return path, true, nil
	} else if err != nil && !os.IsNotExist(err) {
		return "", false, err
	}
	return "", false, nil
}
