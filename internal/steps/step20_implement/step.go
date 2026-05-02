package step20_implement

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/nishimoto265/auto-improve/internal/agents"
	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/policyartifact"
	"github.com/nishimoto265/auto-improve/internal/policyrepo"
	"github.com/nishimoto265/auto-improve/internal/prompt"
	"github.com/nishimoto265/auto-improve/internal/steps/agentrunner"
	"github.com/nishimoto265/auto-improve/internal/steps/policyoverlay"
)

const (
	defaultHeartbeatInterval = 60 * time.Second
	defaultStaleAfter        = 5 * time.Minute
	defaultRescueMaxRetries  = 3
	successCollectTTL        = 10 * time.Second

	resumeStateFileName = ".resume-state.json"
	heartbeatFileName   = ".heartbeat"
	sessionFileName     = "session.jsonl"
	diffFileName        = "diff.patch"
	checklistFileName   = "checklist-result.json"
	rescuedDirName      = "rescued"
	rescueLockFileName  = ".rescue.lock"
	promptVersion       = string(prompt.TemplateStep20Implement)
)

var (
	ErrAgentLeaseContended      = errors.New("step20: agent lease contended")
	ErrRescueAbortedLeaseActive = errors.New("step20: rescue aborted because lease is active")
)

var collectSuccessDiffBytes = agentrunner.SuccessDiffBytes

var implementationCommitExcludedPathspecs = policyartifact.GitExcludePathspecs()

type RunContext struct {
	Config      *config.Config
	Logger      *slog.Logger
	PR          int
	Pass        int
	Agent       contracts.AgentID
	IO          internalio.RunContext
	TaskPackage *contracts.TaskPackage
}

type Step struct {
	cfg               *config.Config
	now               func() time.Time
	heartbeatInterval time.Duration
	staleAfter        time.Duration
	runner            runner
}

func NewStep(cfg *config.Config) *Step {
	return newStep(cfg, stepOptions{})
}

type stepOptions struct {
	now               func() time.Time
	heartbeatInterval time.Duration
	staleAfter        time.Duration
	runner            runner
}

func newStep(cfg *config.Config, opts stepOptions) *Step {
	if opts.now == nil {
		opts.now = time.Now
	}
	if opts.heartbeatInterval <= 0 {
		opts.heartbeatInterval = defaultHeartbeatInterval
	}
	if opts.staleAfter <= 0 {
		opts.staleAfter = defaultStaleAfter
	}
	if opts.runner == nil {
		opts.runner = commandRunner{now: opts.now}
	}
	return &Step{
		cfg:               cfg,
		now:               opts.now,
		heartbeatInterval: opts.heartbeatInterval,
		staleAfter:        opts.staleAfter,
		runner:            opts.runner,
	}
}

func (s Step) Run(ctx context.Context, run RunContext) error {
	step := s
	if step.now == nil || step.heartbeatInterval <= 0 || step.staleAfter <= 0 || step.runner == nil {
		impl := newStep(step.cfg, stepOptions{
			now:               step.now,
			heartbeatInterval: step.heartbeatInterval,
			staleAfter:        step.staleAfter,
			runner:            step.runner,
		})
		step = *impl
	}
	return step.run(ctx, run)
}

func (s *Step) run(ctx context.Context, run RunContext) error {
	if run.Pass != 1 {
		return fmt.Errorf("step20: unsupported pass: %d", run.Pass)
	}
	if run.TaskPackage == nil {
		return errors.New("step20: task package is required")
	}
	if run.TaskPackage.RunID != run.IO.RunID {
		return fmt.Errorf("step20: task package run_id mismatch: task_package=%s io=%s", run.TaskPackage.RunID, run.IO.RunID)
	}
	if run.Config == nil {
		run.Config = s.cfg
	}
	if run.Config == nil {
		return errors.New("step20: config is required")
	}

	allocation, err := worktreeFor(run.TaskPackage, run.Pass, run.Agent)
	if err != nil {
		return err
	}
	if err := run.IO.ValidateWorktreeAllocation(allocation); err != nil {
		return err
	}
	timeout, err := stepTimeout(run.Config, "step20")
	if err != nil {
		return err
	}

	agentDir, err := agentDir(run.IO, run.Pass, run.Agent)
	if err != nil {
		return err
	}
	if err := ensureDir(agentDir); err != nil {
		return err
	}

	leaseLock, acquired, err := tryAcquireRescueLock(filepath.Join(agentDir, rescueLockFileName))
	if err != nil {
		return err
	}
	if !acquired {
		return fmt.Errorf("%w: agent %s", ErrAgentLeaseContended, run.Agent)
	}
	defer leaseLock.Unlock()

	allocation, err = ensureAllocationWorktreeBeforeResume(ctx, run, allocation, agentDir)
	if err != nil {
		return err
	}

	stepStartedAt := s.now().UTC()
	retryCount, err := s.resumeIfNeeded(ctx, run, allocation, agentDir)
	if err != nil {
		return err
	}

	deadline := stepStartedAt.Add(timeout)
	activeRules, err := policyrepo.LoadActiveRulesForRun(run.IO)
	if err != nil {
		return fmt.Errorf("step20: load active policy rules: %w", err)
	}
	if err := policyoverlay.ApplyWithSnapshot(allocation.Path, filepath.Join(run.IO.RunDir(), "policy"), activeRules, nil); err != nil {
		return fmt.Errorf("step20: apply policy overlay: %w", err)
	}
	allocation, err = commitPolicyOverlayBase(collectCtxFromContext(ctx), allocation, run.IO.RunID)
	if err != nil {
		return err
	}
	if err := saveResumeState(agentDir, resumeState{
		ExpectedBaseSHA: allocation.BaseSHA,
		RetryCount:      retryCount,
	}); err != nil {
		return err
	}
	promptText, err := renderPrompt(run.Config, promptData{
		TaskPackage: run.TaskPackage,
		Agent:       run.Agent,
		OutputDir:   manifestPrefix(run.Pass, run.Agent),
		TaskPrompt:  run.TaskPackage.ReconstructedTaskPrompt,
		ActiveRules: activeRules,
	})
	if err != nil {
		return err
	}

	var heartbeat *heartbeatHandle
	defer func() {
		if heartbeat != nil {
			heartbeat.Stop()
		}
	}()
	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	sessionPath, err := artifactPath(run.IO, run.Pass, run.Agent, sessionFileName)
	if err != nil {
		return err
	}

	remaining := deadline.Sub(s.now().UTC())
	if remaining <= 0 {
		return s.writeTimeoutManifest(ctx, run, timeout, stepStartedAt, s.now().UTC())
	}
	implementer, err := run.Config.AgentProfile(agents.RoleImplementer)
	if err != nil {
		return err
	}
	binary, args, err := agentrunner.ImplementerCommand(implementer, allocation.Path)
	if err != nil {
		return err
	}

	runResult, err := s.runner.Run(runCtx, runnerRequest{
		Binary:      binary,
		Args:        args,
		Workdir:     allocation.Path,
		Prompt:      promptText,
		SessionPath: sessionPath,
		Timeout:     remaining,
		Env: append([]string{
			"AUTO_IMPROVE_STEP=20",
			"AUTO_IMPROVE_PASS=1",
			"AUTO_IMPROVE_AGENT=" + string(run.Agent),
			"AUTO_IMPROVE_RUN_ID=" + string(run.IO.RunID),
			"AUTO_IMPROVE_OUTPUT_DIR=" + manifestPrefix(run.Pass, run.Agent),
		}, agentrunner.ProfileEnv(implementer)...),
		OnStart: func(lease agentrunner.ProcessLease, startedAt time.Time) error {
			state := resumeState{
				ExpectedBaseSHA: allocation.BaseSHA,
				StartedAt:       startedAt.UTC(),
				Pid:             lease.PID,
				Pgid:            lease.PGID,
				LeaderStartTime: lease.StartTime,
				RetryCount:      retryCount,
				LastHeartbeat:   startedAt.UTC(),
			}
			if err := touchHeartbeat(agentDir, state.LastHeartbeat); err != nil {
				return err
			}
			if err := saveResumeState(agentDir, state); err != nil {
				return err
			}
			handle, err := startHeartbeat(runCtx, heartbeatConfig{
				agentDir:  agentDir,
				interval:  s.heartbeatInterval,
				now:       s.now,
				baseState: state,
				cancel:    cancel,
				prefix:    "step20",
			})
			if err != nil {
				return err
			}
			heartbeat = handle
			return nil
		},
	})
	if err != nil {
		if cause := context.Cause(runCtx); cause != nil && errors.Is(err, runCtx.Err()) {
			return cause
		}
		return err
	}
	if cause := context.Cause(runCtx); cause != nil && errors.Is(cause, errHeartbeatUpdateFailed) {
		return cause
	}

	finalizeCtx := context.Background()
	if heartbeat != nil {
		heartbeat.Stop()
		heartbeat = nil
	}
	if cause := context.Cause(runCtx); cause != nil && errors.Is(cause, errHeartbeatUpdateFailed) {
		return cause
	}
	if err := prepareTerminalLeaseFinalize(agentDir); err != nil {
		return err
	}

	var finalizeErr error
	if runResult.TimedOut {
		finalizeErr = s.writeTimeoutManifest(finalizeCtx, run, timeout, runResult.StartedAt.UTC(), runResult.FinishedAt.UTC())
	} else if runResult.ExitCode != 0 {
		finalizeErr = s.writeErrorManifest(finalizeCtx, run, runResult)
	} else if runResult.CleanupErr != nil {
		runResult.ExitCode = 1
		runResult.StderrSnippet = agentrunner.AppendCleanupDetail(runResult.StderrSnippet, runResult.CleanupErr)
		finalizeErr = s.writeErrorManifest(finalizeCtx, run, runResult)
	} else {
		finalizeErr = s.writeSuccessArtifacts(finalizeCtx, run, allocation, runResult)
	}
	if finalizeErr != nil {
		return finalizeErr
	}
	return clearActiveLease(agentDir)
}
