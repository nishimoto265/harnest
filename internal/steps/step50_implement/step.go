package step50_implement

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/nishimoto265/auto-improve/internal/agents"
	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/policyrepo"
	"github.com/nishimoto265/auto-improve/internal/prompt"
	"github.com/nishimoto265/auto-improve/internal/steps/agentrunner"
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

	if err := ensureAllocationWorktreeBeforeResume(ctx, run, allocation, agentDir); err != nil {
		return err
	}

	stepStartedAt := s.now().UTC()
	retryCount, err := s.resumeIfNeeded(ctx, run, allocation, agentDir)
	if err != nil {
		return err
	}

	deadline := stepStartedAt.Add(timeout)
	candidatesPath, err := run.IO.ResolveRunRelative(filepath.Join("40", "candidates.json"))
	if err != nil {
		return err
	}
	rulePayloads, err := LoadRulePayloads(candidatesPath)
	if err != nil {
		return fmt.Errorf("step50: load rule payloads: %w", err)
	}
	activeRules, err := policyrepo.LoadActiveRulesForRun(run.IO)
	if err != nil {
		return fmt.Errorf("step50: load active policy rules: %w", err)
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
		Env: []string{
			"AUTO_IMPROVE_STEP=50",
			"AUTO_IMPROVE_PASS=2",
			"AUTO_IMPROVE_AGENT=" + string(run.Agent),
			"AUTO_IMPROVE_RUN_ID=" + string(run.TaskPackage.RunID),
			"AUTO_IMPROVE_OUTPUT_DIR=" + manifestPrefix(run.Pass, run.Agent),
		},
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

func (s *Step) writeSuccessArtifacts(ctx context.Context, run RunContext, allocation contracts.WorktreeAllocation, runResult runnerResult) error {
	if err := run.IO.ValidateWorktreeAllocation(allocation); err != nil {
		return err
	}
	collectCtx, cancel := context.WithTimeout(ctx, successCollectTTL)
	defer cancel()

	headSHA, err := gitOutputContext(collectCtx, stringsTrimSpace, allocation.Path, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if err := agentrunner.ValidateSuccessHead(collectCtx, allocation, headSHA, "step50"); err != nil {
		return err
	}
	diffPath, err := artifactPath(run.IO, run.Pass, run.Agent, diffFileName)
	if err != nil {
		return err
	}
	diffBytes, err := successDiffBytes(collectCtx, allocation.Path, allocation.BaseSHA)
	if err != nil {
		return err
	}
	if len(diffBytes) == 0 {
		return s.writeNoChangeManifest(ctx, run, runResult)
	}
	syntheticCommit := false
	syntheticParent := ""
	if headSHA == allocation.BaseSHA {
		headSHA, syntheticParent, err = synthesizeSuccessCommit(collectCtx, allocation, run)
		if err != nil {
			return err
		}
		syntheticCommit = true
	}
	if err := internalio.WriteAtomic(diffPath, diffBytes); err != nil {
		return err
	}

	checklistPath, err := artifactPath(run.IO, run.Pass, run.Agent, checklistFileName)
	if err != nil {
		return err
	}
	checklist, err := loadChecklistArtifact(collectCtx, allocation.Path, run.TaskPackage.RunID, run.Pass, run.Agent)
	if err != nil {
		return err
	}
	if err := internalio.WriteJSONAtomic(checklistPath, checklist); err != nil {
		return err
	}
	if syntheticCommit {
		if err := finalizeSyntheticSuccessCommit(collectCtx, allocation, headSHA, syntheticParent, "step50"); err != nil {
			return err
		}
	}

	prefix := manifestPrefix(run.Pass, run.Agent)
	manifest := contracts.Manifest{
		Kind: contracts.ManifestKindSuccess,
		Value: contracts.ManifestSuccess{
			Kind:          contracts.ManifestKindSuccess,
			SchemaVersion: "1",
			RunID:         run.TaskPackage.RunID,
			Pass:          run.Pass,
			Agent:         run.Agent,
			BranchName:    allocation.Branch,
			HeadSHA:       headSHA,
			BaseSHA:       allocation.BaseSHA,
			DiffPath:      filepath.Join(prefix, diffFileName),
			SessionPath:   filepath.Join(prefix, sessionFileName),
			ChecklistPath: filepath.Join(prefix, checklistFileName),
			PromptVersion: promptVersion,
			StartedAt:     runResult.StartedAt.UTC(),
			FinishedAt:    runResult.FinishedAt.UTC(),
		},
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return writeManifest(run.IO, run.Pass, run.Agent, manifest)
}

func (s *Step) writeErrorManifest(ctx context.Context, run RunContext, runResult runnerResult) error {
	reason := agentrunner.InterruptionReason(runResult.ExitCode, runResult.StdoutSnippet, runResult.StderrSnippet)
	manifest := contracts.Manifest{
		Kind: contracts.ManifestKindError,
		Value: contracts.ManifestError{
			Kind:          contracts.ManifestKindError,
			SchemaVersion: "1",
			RunID:         run.TaskPackage.RunID,
			Pass:          run.Pass,
			Agent:         run.Agent,
			ExitCode:      runResult.ExitCode,
			Reason:        string(reason),
			Detail:        agentrunner.TruncateDetail(runResult.StderrSnippet, runResult.StdoutSnippet),
			StartedAt:     runResult.StartedAt.UTC(),
			FinishedAt:    runResult.FinishedAt.UTC(),
		},
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return writeManifest(run.IO, run.Pass, run.Agent, manifest)
}

func (s *Step) writeTimeoutManifest(ctx context.Context, run RunContext, timeout time.Duration, startedAt, finishedAt time.Time) error {
	manifest := contracts.Manifest{
		Kind: contracts.ManifestKindTimeout,
		Value: contracts.ManifestTimeout{
			Kind:           contracts.ManifestKindTimeout,
			SchemaVersion:  "1",
			RunID:          run.TaskPackage.RunID,
			Pass:           run.Pass,
			Agent:          run.Agent,
			TimeoutSeconds: int(timeout / time.Second),
			StartedAt:      startedAt,
			FinishedAt:     finishedAt,
		},
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return writeManifest(run.IO, run.Pass, run.Agent, manifest)
}

func writeManifest(runIO internalio.RunContext, pass int, agent contracts.AgentID, manifest contracts.Manifest) error {
	path, err := runIO.ManifestPath(pass, agent)
	if err != nil {
		return err
	}
	return internalio.WriteJSONAtomic(path, manifest)
}

func artifactPath(runIO internalio.RunContext, pass int, agent contracts.AgentID, name string) (string, error) {
	rel := filepath.Join(manifestPrefix(pass, agent), name)
	return runIO.ResolveRunRelative(rel)
}

func ensureDir(path string) error {
	return os.MkdirAll(path, 0o755)
}

func ensureAllocationWorktree(ctx context.Context, cfg *config.Config, allocation contracts.WorktreeAllocation) error {
	return ensureAllocationWorktreeAtRef(ctx, cfg, allocation, allocation.HeadSHA, true)
}

func ensureAllocationWorktreeBeforeResume(ctx context.Context, run RunContext, allocation contracts.WorktreeAllocation, agentDir string) error {
	state, ok, err := loadResumeState(agentDir)
	if err != nil {
		return err
	}
	if ok && state.Pid != 0 {
		return nil
	}
	return ensureAllocationWorktree(ctx, run.Config, allocation)
}

func ensureAllocationWorktreeForRescue(ctx context.Context, cfg *config.Config, allocation contracts.WorktreeAllocation) error {
	return ensureAllocationWorktreeAtRef(ctx, cfg, allocation, allocation.Branch, false)
}

func ensureAllocationWorktreeAtRef(ctx context.Context, cfg *config.Config, allocation contracts.WorktreeAllocation, ref string, resetBranch bool) error {
	// No-follow Lstat at use time (not just at step10 validation). A symlink
	// could have been swapped in between ValidateWorktreeAllocation and now;
	// os.Stat would follow it and accept an arbitrary target directory.
	if err := internalio.EnsureNoSymlinkPathComponents(allocation.Path); err != nil {
		return fmt.Errorf("step50: worktree path rejected: %w", err)
	}
	info, err := os.Lstat(allocation.Path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("step50: worktree path is a symlink: %s", allocation.Path)
		}
		if !info.IsDir() {
			return fmt.Errorf("step50: worktree path is not a directory: %s", allocation.Path)
		}
		// Existing worktree: HEAD may legitimately have advanced via a prior
		// successful attempt. Trust the on-disk state; BaseSHA / HeadSHA
		// verification happens downstream in rescue / diff flows.
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	if cfg == nil {
		return errors.New("step50: config is required to recreate missing worktree")
	}
	if ref == "" {
		if resetBranch {
			return errors.New("step50: cannot recreate worktree without immutable head_sha")
		}
		return errors.New("step50: cannot recreate rescue worktree without allocation branch")
	}
	repoRoot, err := cfg.RepoRoot()
	if err != nil {
		return err
	}
	parent := filepath.Dir(allocation.Path)
	if err := internalio.EnsureNoSymlinkPathComponents(parent); err != nil {
		return fmt.Errorf("step50: worktree parent rejected: %w", err)
	}
	if err := ensureDir(parent); err != nil {
		return err
	}
	if err := runGitCommand(ctx, repoRoot, "worktree", "prune"); err != nil {
		return err
	}
	if resetBranch {
		// Fresh runs pin the recreated worktree to the immutable HeadSHA
		// recorded in the task package rather than trusting the current tip of
		// allocation.Branch.
		if err := runGitCommand(ctx, repoRoot,
			"worktree", "add", "-B", allocation.Branch, allocation.Path, ref); err != nil {
			return err
		}
	} else {
		// Rescue runs must not move allocation.Branch before performRescue has
		// captured commits from the branch's current tip.
		if err := runGitCommand(ctx, repoRoot,
			"worktree", "add", allocation.Path, ref); err != nil {
			return err
		}
	}
	// Re-check symlink components after creation: refuse to continue if the
	// freshly created path or any ancestor was swapped to a symlink mid-setup.
	if err := internalio.EnsureNoSymlinkPathComponents(allocation.Path); err != nil {
		return fmt.Errorf("step50: worktree path swapped after create: %w", err)
	}
	if resetBranch {
		return verifyAllocationHead(ctx, allocation)
	}
	return nil
}

// verifyAllocationHead refuses to continue if the worktree's HEAD does not
// match the immutable allocation.HeadSHA recorded in the task package.
func verifyAllocationHead(ctx context.Context, allocation contracts.WorktreeAllocation) error {
	if allocation.HeadSHA == "" {
		return nil
	}
	head, err := gitOutputContext(ctx, stringsTrimSpace, allocation.Path, "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("step50: rev-parse HEAD for allocation %s: %w", allocation.Path, err)
	}
	if head != allocation.HeadSHA {
		return fmt.Errorf("step50: allocation HEAD mismatch: path=%s want=%s got=%s", allocation.Path, allocation.HeadSHA, head)
	}
	return nil
}

func restoreAllocationWorktree(ctx context.Context, allocation contracts.WorktreeAllocation, expectedBaseSHA string) error {
	targetRef := expectedBaseSHA
	currentHead, err := gitOutputContext(ctx, stringsTrimSpace, allocation.Path, "rev-parse", "HEAD")
	if err == nil && currentHead == expectedBaseSHA {
		targetRef = "HEAD"
	}
	if err := runGitCommand(ctx, allocation.Path, "checkout", "--force", "-B", allocation.Branch, targetRef); err != nil {
		return err
	}
	if err := runGitCommand(ctx, allocation.Path, "reset", "--hard", targetRef); err != nil {
		return err
	}
	if err := runGitCommand(ctx, allocation.Path, "clean", "-fd"); err != nil {
		return err
	}
	return nil
}

func manifestPrefix(pass int, agent contracts.AgentID) string {
	if pass == passNumber {
		return filepath.Join("50-pass2", string(agent))
	}
	return filepath.Join("20-pass1", string(agent))
}

func worktreeFor(pkg *contracts.TaskPackage, pass int, agent contracts.AgentID) (contracts.WorktreeAllocation, error) {
	if pkg == nil {
		return contracts.WorktreeAllocation{}, errors.New("step50: task package is required")
	}
	for _, worktree := range pkg.Worktrees {
		if worktree.Pass == pass && worktree.Agent == agent {
			return worktree, nil
		}
	}
	return contracts.WorktreeAllocation{}, fmt.Errorf("step50: missing worktree allocation: pass=%d agent=%s", pass, agent)
}

func agentDir(runIO internalio.RunContext, pass int, agent contracts.AgentID) (string, error) {
	return runIO.ResolveRunRelative(manifestPrefix(pass, agent))
}

func stepTimeout(cfg *config.Config, key string) (time.Duration, error) {
	if cfg == nil {
		return 0, errors.New("step50: config is required")
	}
	seconds, ok := cfg.StepTimeouts[key]
	if !ok || seconds <= 0 {
		return 0, fmt.Errorf("step50: missing step timeout: %s", key)
	}
	return time.Duration(seconds) * time.Second, nil
}

func loadChecklistArtifact(ctx context.Context, worktreePath string, runID contracts.RunID, pass int, agent contracts.AgentID) (contracts.ChecklistResult, error) {
	return agentrunner.LoadChecklistArtifactContext(ctx, worktreePath, checklistFileName, "step50", runID, pass, agent)
}

func successDiffBytes(ctx context.Context, worktreePath, baseSHA string) ([]byte, error) {
	return collectSuccessDiffBytes(ctx, worktreePath, baseSHA, "step50")
}

func shouldWriteTimeoutManifest(err error, execCtx context.Context) bool {
	return err != nil && errors.Is(execCtx.Err(), context.DeadlineExceeded)
}

func (s *Step) writeNoChangeManifest(ctx context.Context, run RunContext, runResult runnerResult) error {
	manifest := contracts.Manifest{
		Kind: contracts.ManifestKindError,
		Value: contracts.ManifestError{
			Kind:          contracts.ManifestKindError,
			SchemaVersion: "1",
			RunID:         run.TaskPackage.RunID,
			Pass:          run.Pass,
			Agent:         run.Agent,
			ExitCode:      0,
			Reason:        "unknown",
			Detail:        "agent produced no diff",
			StartedAt:     runResult.StartedAt.UTC(),
			FinishedAt:    runResult.FinishedAt.UTC(),
		},
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return writeManifest(run.IO, run.Pass, run.Agent, manifest)
}

func synthesizeSuccessCommit(ctx context.Context, allocation contracts.WorktreeAllocation, run RunContext) (string, string, error) {
	if _, err := gitOutputContext(ctx, identity, allocation.Path, "add", "-A", "--", ".", ":(exclude)"+checklistFileName); err != nil {
		return "", "", err
	}
	staged, err := gitOutputContext(ctx, stringsTrimSpace, allocation.Path, "diff", "--no-ext-diff", "--cached", "--name-only", "--", ".", ":(exclude)"+checklistFileName)
	if err != nil {
		return "", "", err
	}
	if staged == "" {
		return "", "", errors.New("step50: synthetic success commit found no staged changes")
	}
	parent, err := gitOutputContext(ctx, stringsTrimSpace, allocation.Path, "rev-parse", "HEAD")
	if err != nil {
		return "", "", err
	}
	tree, err := gitOutputContext(ctx, stringsTrimSpace, allocation.Path, "write-tree")
	if err != nil {
		return "", "", err
	}
	commitSHA, err := gitOutputContextWithEnv(
		ctx,
		stringsTrimSpace,
		allocation.Path,
		syntheticCommitEnv(),
		"commit-tree",
		tree,
		"-p",
		parent,
		"-m",
		fmt.Sprintf("auto-improve: synthesize step50 success for %s %s", run.IO.RunID, run.Agent),
	)
	if err != nil {
		return "", "", err
	}
	return commitSHA, parent, nil
}

func finalizeSyntheticSuccessCommit(ctx context.Context, allocation contracts.WorktreeAllocation, commitSHA, parent, errPrefix string) error {
	if _, err := gitOutputContext(ctx, identity, allocation.Path, "update-ref", "refs/heads/"+allocation.Branch, commitSHA, parent); err != nil {
		return err
	}
	if _, err := gitOutputContext(ctx, identity, allocation.Path, "reset", "--mixed", "HEAD"); err != nil {
		return err
	}
	return agentrunner.ValidateSuccessHead(ctx, allocation, commitSHA, errPrefix)
}
