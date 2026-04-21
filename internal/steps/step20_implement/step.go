package step20_implement

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/interruption"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/prompt"
)

const (
	defaultRescueMaxRetries = 3
	promptVersion           = "step20-implement.tmpl"
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
	runner            ClaudeRunner
	now               func() time.Time
	heartbeatInterval time.Duration
	staleAfter        time.Duration
}

type Option func(*Step)

func WithRunner(runner ClaudeRunner) Option {
	return func(step *Step) {
		if runner != nil {
			step.runner = runner
		}
	}
}

func WithNow(now func() time.Time) Option {
	return func(step *Step) {
		if now != nil {
			step.now = now
		}
	}
}

func WithHeartbeatInterval(interval time.Duration) Option {
	return func(step *Step) {
		if interval > 0 {
			step.heartbeatInterval = interval
		}
	}
}

func WithStaleAfter(interval time.Duration) Option {
	return func(step *Step) {
		if interval > 0 {
			step.staleAfter = interval
		}
	}
}

func NewStep(cfg *config.Config, opts ...Option) *Step {
	step := &Step{
		cfg:               cfg,
		runner:            commandClaudeRunner{},
		now:               time.Now,
		heartbeatInterval: 60 * time.Second,
		staleAfter:        5 * time.Minute,
	}
	for _, opt := range opts {
		opt(step)
	}
	return step
}

func (s *Step) Run(ctx context.Context, run RunContext) error {
	if run.Pass != 1 {
		return fmt.Errorf("step20: pass must be 1: pass=%d", run.Pass)
	}
	if run.TaskPackage == nil {
		return errors.New("step20: task package is required")
	}

	cfg := s.cfg
	if run.Config != nil {
		cfg = run.Config
	}
	if cfg == nil {
		return errors.New("step20: config is required")
	}

	allocation, err := worktreeFor(run.TaskPackage, run.Pass, run.Agent)
	if err != nil {
		return err
	}
	paths, err := agentPathsFor(run.IO, run.Pass, run.Agent)
	if err != nil {
		return err
	}
	if err := ensureDir(paths.dir); err != nil {
		return err
	}

	retryCount, err := s.resumeAndRescueIfNeeded(ctx, cfg, run, allocation, paths)
	if err != nil {
		return err
	}

	promptText, err := s.renderPrompt(cfg, run)
	if err != nil {
		return err
	}

	startedAt := s.now().UTC()
	store := newResumeStateStore(paths.resumeStatePath, resumeState{
		ExpectedBaseSHA: allocation.BaseSHA,
		StartedAt:       startedAt,
		PID:             os.Getpid(),
		RetryCount:      retryCount,
		LastHeartbeat:   startedAt,
	})
	if err := store.Save(); err != nil {
		return err
	}

	stopHeartbeat, err := startHeartbeat(ctx, s.heartbeatInterval, paths.heartbeatPath, store, s.now)
	if err != nil {
		return err
	}
	defer stopHeartbeat()

	timeout, err := stepTimeout(cfg)
	if err != nil {
		return err
	}

	runnerResult, err := s.runner.Run(ctx, ClaudeRunRequest{
		Binary:      cfg.ClaudeBinary(),
		WorkDir:     allocation.Path,
		Prompt:      promptText,
		Timeout:     timeout,
		SessionPath: paths.sessionPath,
		Env: []string{
			"AUTO_IMPROVE_RUN_ID=" + string(run.IO.RunID),
			"AUTO_IMPROVE_AGENT=" + string(run.Agent),
			"AUTO_IMPROVE_PASS=1",
			"AUTO_IMPROVE_OUTPUT_DIR=" + paths.dir,
			"AUTO_IMPROVE_SESSION_PATH=" + paths.sessionPath,
			"AUTO_IMPROVE_CHECKLIST_PATH=" + paths.checklistPath,
			"AUTO_IMPROVE_DIFF_PATH=" + paths.diffPath,
		},
	})
	if err != nil {
		return err
	}

	finishedAt := s.now().UTC()
	switch {
	case runnerResult.TimedOut:
		manifest := buildTimeoutManifest(run, timeout, startedAt, finishedAt)
		return writeManifest(paths.manifestPath, manifest)
	case runnerResult.ExitCode != 0:
		manifest := buildErrorManifest(run, runnerResult.ExitCode, classifyReason(runnerResult.Stdout, runnerResult.Stderr, runnerResult.ExitCode), truncateDetail(detailFromOutput(runnerResult.Stdout, runnerResult.Stderr)), startedAt, finishedAt)
		return writeManifest(paths.manifestPath, manifest)
	default:
		manifest, err := s.buildSuccessManifest(run, allocation, paths, startedAt, finishedAt)
		if err != nil {
			return err
		}
		return writeManifest(paths.manifestPath, manifest)
	}
}

func (s *Step) buildSuccessManifest(run RunContext, allocation contracts.WorktreeAllocation, paths agentPaths, startedAt, finishedAt time.Time) (contracts.Manifest, error) {
	headSHA, err := gitOutput(allocation.Path, "rev-parse", "HEAD")
	if err != nil {
		return contracts.Manifest{}, err
	}
	diffPatch, err := gitOutputRaw(allocation.Path, "diff", allocation.BaseSHA+".."+headSHA, "--binary")
	if err != nil {
		return contracts.Manifest{}, err
	}
	if err := internalio.WriteAtomic(paths.diffPath, diffPatch); err != nil {
		return contracts.Manifest{}, err
	}
	if err := ensureFile(paths.sessionPath); err != nil {
		return contracts.Manifest{}, err
	}

	checklist, err := loadOrCreateChecklist(paths.checklistPath, run.IO.RunID, run.Pass, run.Agent)
	if err != nil {
		return contracts.Manifest{}, err
	}
	if err := internalio.WriteJSONAtomic(paths.checklistPath, checklist); err != nil {
		return contracts.Manifest{}, err
	}

	return buildSuccessManifest(run, allocation, headSHA, startedAt, finishedAt), nil
}

func (s *Step) renderPrompt(cfg *config.Config, run RunContext) (string, error) {
	repoRoot, err := cfg.RepoRoot()
	if err != nil {
		return "", err
	}
	path := filepath.Join(repoRoot, prompt.TemplateStep20Implement.RelativePath())
	tmpl, err := template.ParseFiles(path)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, struct {
		TaskPackage               *contracts.TaskPackage
		Agent                     contracts.AgentID
		ReconstructedTaskPrompt   string
		SanitizedTaskPackageTitle string
	}{
		TaskPackage:               run.TaskPackage,
		Agent:                     run.Agent,
		ReconstructedTaskPrompt:   internalio.SanitizeForPromptEmbedding(run.TaskPackage.ReconstructedTaskPrompt),
		SanitizedTaskPackageTitle: internalio.SanitizeForPromptEmbedding(run.TaskPackage.Title),
	}); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func stepTimeout(cfg *config.Config) (time.Duration, error) {
	if cfg == nil {
		return 0, errors.New("step20: config is required")
	}
	seconds, ok := cfg.StepTimeouts["step20"]
	if !ok {
		return 0, errors.New("step20: step timeout step20 is required")
	}
	if seconds <= 0 {
		return 0, fmt.Errorf("step20: step timeout must be positive: %d", seconds)
	}
	return time.Duration(seconds) * time.Second, nil
}

func rescueMaxRetries(cfg *config.Config) int {
	if cfg == nil || cfg.RescueMaxRetries == 0 {
		return defaultRescueMaxRetries
	}
	return cfg.RescueMaxRetries
}

func classifyReason(stdout, stderr []byte, exitCode int) string {
	switch interruption.Classify(exitCode, stdout, stderr) {
	case interruption.InterruptionKindRateLimit:
		return "rate_limit"
	case interruption.InterruptionKindBudget:
		return "budget"
	case interruption.InterruptionKindContext:
		return "context"
	case interruption.InterruptionKindSignal:
		return "signal"
	default:
		return "unknown"
	}
}

func detailFromOutput(stdout, stderr []byte) string {
	if detail := strings.TrimSpace(string(stderr)); detail != "" {
		return detail
	}
	return strings.TrimSpace(string(stdout))
}

func truncateDetail(detail string) string {
	const maxLen = 300
	detail = strings.TrimSpace(detail)
	if len(detail) <= maxLen {
		return detail
	}
	return detail[:maxLen]
}
