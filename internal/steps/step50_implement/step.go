package step50_implement

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nishimoto265/auto-improve/internal/agents"
	"github.com/nishimoto265/auto-improve/internal/candidaterules"
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
	passNumber = 2

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
	promptVersion       = string(prompt.TemplateStep50Implement)
)

var (
	ErrAgentLeaseContended      = errors.New("step50: agent lease contended")
	ErrRescueAbortedLeaseActive = errors.New("step50: rescue aborted because lease is active")
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

type stepOptions struct {
	now               func() time.Time
	heartbeatInterval time.Duration
	staleAfter        time.Duration
	runner            runner
}

func NewStep(cfg *config.Config) *Step {
	return newStep(cfg, stepOptions{})
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
	if run.Pass != passNumber {
		return fmt.Errorf("step50: unsupported pass: %d", run.Pass)
	}
	if run.TaskPackage == nil {
		return errors.New("step50: task package is required")
	}
	if run.TaskPackage.RunID != run.IO.RunID {
		return fmt.Errorf("step50: task package run_id mismatch: task_package=%s io=%s", run.TaskPackage.RunID, run.IO.RunID)
	}
	taskPackage := cloneTaskPackage(run.TaskPackage)
	run.TaskPackage = &taskPackage
	if run.Config == nil {
		run.Config = s.cfg
	}
	if run.Config == nil {
		return errors.New("step50: config is required")
	}

	allocation, err := worktreeFor(run.TaskPackage, run.Pass, run.Agent)
	if err != nil {
		return err
	}
	if err := run.IO.ValidateWorktreeAllocation(allocation); err != nil {
		return err
	}
	timeout, err := stepTimeout(run.Config, "step50")
	if err != nil {
		return err
	}
	candidatesPath, err := run.IO.ResolveRunRelative(filepath.Join("40", "candidates.json"))
	if err != nil {
		return err
	}
	rulePayloads, err := candidaterules.LoadRulePayloads(candidatesPath)
	if err != nil {
		return fmt.Errorf("step50: load rule payloads: %w", err)
	}
	activeRules, err := policyrepo.LoadActiveRulesForRun(run.IO)
	if err != nil {
		return fmt.Errorf("step50: load active policy rules: %w", err)
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

	allocation, err = s.ensurePreparedPass2Allocation(ctx, run, allocation, activeRules, rulePayloads)
	if err != nil {
		return fmt.Errorf("step50: prepare pass2 allocation: %w", err)
	}
	allocation, err = ensureAllocationWorktreeBeforeResume(ctx, run, allocation, agentDir)
	if err != nil {
		return fmt.Errorf("step50: ensure allocation before resume: %w", err)
	}

	stepStartedAt := s.now().UTC()
	retryCount, err := s.resumeIfNeeded(ctx, run, allocation, agentDir)
	if err != nil {
		return fmt.Errorf("step50: resume: %w", err)
	}

	deadline := stepStartedAt.Add(timeout)
	if err := policyoverlay.ApplyWithSnapshot(allocation.Path, filepath.Join(run.IO.RunDir(), "policy"), activeRules, policyoverlay.ExperimentsFromRulePayloads(rulePayloads)); err != nil {
		return fmt.Errorf("step50: apply policy overlay: %w", err)
	}
	allocation, err = commitPolicyOverlayBase(ctx, allocation, run.TaskPackage.RunID)
	if err != nil {
		return err
	}
	if err := saveResumeState(agentDir, resumeState{
		ExpectedBaseSHA: allocation.BaseSHA,
		RetryCount:      retryCount,
	}); err != nil {
		return err
	}
	promptText, err := RenderPrompt(PromptData{
		TaskPackage:      *run.TaskPackage,
		Agent:            run.Agent,
		CandidateRuleIDs: rulePayloadIDs(rulePayloads),
		RulePayloads:     rulePayloads,
		ActiveRules:      activeRules,
		WorktreePath:     allocation.Path,
		Pass:             passNumber,
	})
	if err != nil {
		return fmt.Errorf("step50: render prompt: %w", err)
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
		Provider:    implementer.Provider,
		Env: append(append([]string{
			"AUTO_IMPROVE_STEP=50",
			"AUTO_IMPROVE_PASS=2",
			"AUTO_IMPROVE_AGENT=" + string(run.Agent),
			"AUTO_IMPROVE_RUN_ID=" + string(run.TaskPackage.RunID),
			"AUTO_IMPROVE_OUTPUT_DIR=" + manifestPrefix(run.Pass, run.Agent),
		}, agentrunner.CurrentExecutableEnv()...), agentrunner.ProfileEnv(implementer)...),
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
				prefix:    "step50",
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
		return fmt.Errorf("step50: prepare terminal lease finalize: %w", err)
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
		if errors.Is(finalizeErr, agentrunner.ErrMissingChecklistArtifact) {
			runResult.ExitCode = 1
			runResult.StderrSnippet = []byte(finalizeErr.Error())
			finalizeErr = s.writeErrorManifest(finalizeCtx, run, runResult)
		}
	}
	if finalizeErr != nil {
		return fmt.Errorf("step50: finalize agent result: %w", finalizeErr)
	}
	if err := clearActiveLease(agentDir); err != nil {
		return fmt.Errorf("step50: clear active lease: %w", err)
	}
	return nil
}

func cloneTaskPackage(pkg *contracts.TaskPackage) contracts.TaskPackage {
	if pkg == nil {
		return contracts.TaskPackage{}
	}
	clone := *pkg
	clone.PassBases = append([]contracts.PassBaseAllocation(nil), pkg.PassBases...)
	clone.Worktrees = append([]contracts.WorktreeAllocation(nil), pkg.Worktrees...)
	return clone
}

func (s *Step) ensurePreparedPass2Allocation(ctx context.Context, run RunContext, allocation contracts.WorktreeAllocation, activeRules []policyrepo.ActiveRule, rulePayloads []candidaterules.RulePayload) (contracts.WorktreeAllocation, error) {
	if len(run.TaskPackage.PassBases) == 0 {
		return allocation, nil
	}
	passBase, err := passBaseFor(run.TaskPackage, passNumber)
	if err != nil {
		return allocation, fmt.Errorf("select pass base: %w", err)
	}
	if err := run.IO.ValidatePassBaseAllocation(passBase); err != nil {
		return allocation, fmt.Errorf("validate pass base allocation: %w", err)
	}
	passDir, err := run.IO.ResolveRunRelative("50-pass2")
	if err != nil {
		return allocation, fmt.Errorf("resolve pass2 dir: %w", err)
	}
	if err := ensureDir(passDir); err != nil {
		return allocation, fmt.Errorf("ensure pass2 dir: %w", err)
	}
	lock, err := internalio.AcquireFileLockContext(ctx, filepath.Join(passDir, ".pass-base.lock"))
	if err != nil {
		return allocation, fmt.Errorf("acquire pass base lock: %w", err)
	}
	defer lock.Unlock()

	if latest, err := internalio.ReadJSON[contracts.TaskPackage](run.IO.TaskPackagePath()); err == nil && latest.RunID == run.TaskPackage.RunID {
		run.TaskPackage.PassBases = latest.PassBases
		run.TaskPackage.Worktrees = latest.Worktrees
		passBase, err = passBaseFor(run.TaskPackage, passNumber)
		if err != nil {
			return allocation, fmt.Errorf("select refreshed pass base: %w", err)
		}
		if err := run.IO.ValidatePassBaseAllocation(passBase); err != nil {
			return allocation, fmt.Errorf("validate refreshed pass base allocation: %w", err)
		}
	}
	repoRoot, err := run.Config.RepoRoot()
	if err != nil {
		return allocation, fmt.Errorf("resolve repo root: %w", err)
	}
	if err := ensurePassBaseWorktree(ctx, repoRoot, passBase); err != nil {
		return allocation, fmt.Errorf("ensure pass base worktree: %w", err)
	}
	if err := policyoverlay.ApplyWithSnapshot(passBase.Path, filepath.Join(run.IO.RunDir(), "policy"), activeRules, policyoverlay.ExperimentsFromRulePayloads(rulePayloads)); err != nil {
		return allocation, fmt.Errorf("step50: apply pass base policy overlay: %w", err)
	}
	passBase, err = commitPolicyOverlayPassBase(ctx, passBase, run.TaskPackage.RunID)
	if err != nil {
		return allocation, fmt.Errorf("commit pass base policy overlay: %w", err)
	}
	if err := persistPreparedPass2Base(run, passBase); err != nil {
		return allocation, fmt.Errorf("persist prepared pass2 base: %w", err)
	}
	if err := ensureMissingPass2Worktrees(ctx, run.Config, run.TaskPackage, passBase.HeadSHA); err != nil {
		return allocation, fmt.Errorf("ensure missing pass2 worktrees: %w", err)
	}
	allocation.BaseSHA = passBase.HeadSHA
	allocation.HeadSHA = passBase.HeadSHA
	return allocation, nil
}

func ensurePassBaseWorktree(ctx context.Context, repoRoot string, allocation contracts.PassBaseAllocation) error {
	if err := internalio.EnsureNoSymlinkPathComponents(allocation.Path); err != nil {
		return fmt.Errorf("step50: pass base path rejected: %w", err)
	}
	if info, err := os.Lstat(allocation.Path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("step50: pass base path is a symlink: %s", allocation.Path)
		}
		if !info.IsDir() {
			return fmt.Errorf("step50: pass base path is not a directory: %s", allocation.Path)
		}
		currentBranch, err := gitOutputContext(ctx, strings.TrimSpace, allocation.Path, "branch", "--show-current")
		if err != nil {
			return err
		}
		if currentBranch != allocation.Branch {
			return fmt.Errorf("step50: pass base branch mismatch: path=%s want=%s got=%s", allocation.Path, allocation.Branch, currentBranch)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := ensureDir(filepath.Dir(allocation.Path)); err != nil {
		return err
	}
	if err := runGitCommand(ctx, repoRoot, "worktree", "prune"); err != nil {
		return err
	}
	ref := allocation.BaseSHA
	if allocation.HeadSHA != "" && allocation.HeadSHA != allocation.BaseSHA {
		ref = allocation.HeadSHA
	}
	err := runGitCommand(ctx, repoRoot, "worktree", "add", "-B", allocation.Branch, allocation.Path, ref)
	if err == nil || ref == allocation.BaseSHA {
		return err
	}
	return runGitCommand(ctx, repoRoot, "worktree", "add", "-B", allocation.Branch, allocation.Path, allocation.BaseSHA)
}

func persistPreparedPass2Base(run RunContext, passBase contracts.PassBaseAllocation) error {
	if run.TaskPackage == nil || passBase.Pass != passNumber || passBase.HeadSHA == "" {
		return nil
	}
	pkg := *run.TaskPackage
	pkg.PassBases = append([]contracts.PassBaseAllocation(nil), run.TaskPackage.PassBases...)
	pkg.Worktrees = append([]contracts.WorktreeAllocation(nil), run.TaskPackage.Worktrees...)
	changed := false
	for i := range pkg.PassBases {
		if pkg.PassBases[i].Pass != passNumber {
			continue
		}
		if pkg.PassBases[i].HeadSHA != passBase.HeadSHA {
			pkg.PassBases[i].HeadSHA = passBase.HeadSHA
			changed = true
		}
	}
	for i := range pkg.Worktrees {
		if pkg.Worktrees[i].Pass != passNumber {
			continue
		}
		if pkg.Worktrees[i].BaseSHA != passBase.HeadSHA || pkg.Worktrees[i].HeadSHA != passBase.HeadSHA {
			pkg.Worktrees[i].BaseSHA = passBase.HeadSHA
			pkg.Worktrees[i].HeadSHA = passBase.HeadSHA
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if err := internalio.WriteJSONAtomic(run.IO.TaskPackagePath(), pkg); err != nil {
		return err
	}
	*run.TaskPackage = pkg
	return nil
}
