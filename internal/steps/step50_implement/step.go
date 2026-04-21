package step50_implement

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/prompt"
	"github.com/nishimoto265/auto-improve/internal/steps/agentrunner"
)

const (
	passNumber = 2

	defaultHeartbeatInterval = 60 * time.Second
	defaultStaleAfter        = 5 * time.Minute
	defaultRescueMaxRetries  = 3

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
	if step.now == nil {
		impl := newStep(step.cfg, stepOptions{})
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
	promptText, err := RenderPrompt(PromptData{
		TaskPackage:      *run.TaskPackage,
		Agent:            run.Agent,
		CandidateRuleIDs: rulePayloadIDs(rulePayloads),
		RulePayloads:     rulePayloads,
		WorktreePath:     allocation.Path,
		Pass:             passNumber,
	})
	if err != nil {
		return fmt.Errorf("step50: render prompt: %w", err)
	}

	state := resumeState{
		ExpectedBaseSHA: allocation.BaseSHA,
		StartedAt:       stepStartedAt,
		Pid:             os.Getpid(),
		RetryCount:      retryCount,
		LastHeartbeat:   stepStartedAt,
	}
	if err := touchHeartbeat(agentDir, state.LastHeartbeat); err != nil {
		return err
	}
	if err := saveResumeState(agentDir, state); err != nil {
		return err
	}

	heartbeat, err := startHeartbeat(ctx, heartbeatConfig{
		agentDir:  agentDir,
		interval:  s.heartbeatInterval,
		now:       s.now,
		baseState: state,
	})
	if err != nil {
		return err
	}
	defer heartbeat.Stop()

	sessionPath, err := artifactPath(run.IO, run.Pass, run.Agent, sessionFileName)
	if err != nil {
		return err
	}

	remaining := deadline.Sub(s.now().UTC())
	if remaining <= 0 {
		return s.writeTimeoutManifest(ctx, run, timeout, stepStartedAt, s.now().UTC())
	}

	runResult, err := s.runner.Run(ctx, runnerRequest{
		Binary:      run.Config.ClaudeBinary(),
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
	})
	if err != nil {
		return err
	}

	if runResult.TimedOut {
		return s.writeTimeoutManifest(ctx, run, timeout, runResult.StartedAt.UTC(), runResult.FinishedAt.UTC())
	}
	if runResult.ExitCode != 0 {
		return s.writeErrorManifest(ctx, run, runResult)
	}
	return s.writeSuccessArtifacts(ctx, run, allocation, runResult)
}

func (s *Step) writeSuccessArtifacts(ctx context.Context, run RunContext, allocation contracts.WorktreeAllocation, runResult runnerResult) error {
	headSHA, err := gitOutputContext(ctx, stringsTrimSpace, allocation.Path, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	diffBytes, err := successDiffBytes(ctx, allocation.Path, allocation.BaseSHA)
	if err != nil {
		return err
	}

	diffPath, err := artifactPath(run.IO, run.Pass, run.Agent, diffFileName)
	if err != nil {
		return err
	}
	if err := writeAtomic(diffPath, diffBytes); err != nil {
		return err
	}

	checklistPath, err := artifactPath(run.IO, run.Pass, run.Agent, checklistFileName)
	if err != nil {
		return err
	}
	checklist, err := loadChecklistArtifact(allocation.Path, run.TaskPackage.RunID, run.Pass, run.Agent)
	if err != nil {
		return err
	}
	if err := writeJSONAtomic(checklistPath, checklist); err != nil {
		return err
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
	reason := interruptionReason(runResult.ExitCode, runResult.StdoutSnippet, runResult.StderrSnippet)
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
			Detail:        truncateDetail(runResult.StderrSnippet, runResult.StdoutSnippet),
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
	return writeJSONAtomic(path, manifest)
}

func artifactPath(runIO internalio.RunContext, pass int, agent contracts.AgentID, name string) (string, error) {
	rel := filepath.Join(manifestPrefix(pass, agent), name)
	return runIO.ResolveRunRelative(rel)
}

func ensureDir(path string) error {
	return os.MkdirAll(path, 0o755)
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

func loadChecklistArtifact(worktreePath string, runID contracts.RunID, pass int, agent contracts.AgentID) (contracts.ChecklistResult, error) {
	_ = runID
	_ = pass
	_ = agent
	return agentrunner.LoadChecklistArtifact(worktreePath, checklistFileName, "step50")
}

func successDiffBytes(ctx context.Context, worktreePath, baseSHA string) ([]byte, error) {
	return agentrunner.SuccessDiffBytes(ctx, worktreePath, baseSHA, "step50")
}

func shouldWriteTimeoutManifest(err error, execCtx context.Context) bool {
	return err != nil && errors.Is(execCtx.Err(), context.DeadlineExceeded)
}
