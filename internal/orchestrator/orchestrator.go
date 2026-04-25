package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	ilog "github.com/nishimoto265/auto-improve/internal/logger"
	"github.com/nishimoto265/auto-improve/internal/state"
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
var (
	errNoScorableAgentsResume = errors.New("orchestrator: resume selected step30 but pass1 has no scorable agents")
	errAllPass1TimedOutResume = errors.New("orchestrator: resume selected step30 but all pass1 agents timed out")
	errAllPass2TimedOutResume = errors.New("orchestrator: resume selected step60 but all pass2 agents timed out")
)
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
	decoders := defaultContractDecoders()
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
