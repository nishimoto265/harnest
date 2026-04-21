// Package step50_implement runs the pass2 implementation agent, captures its
// artifacts, and atomically writes the per-agent manifest for step 50.
package step50_implement

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/interruption"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/orchestrator"
)

const (
	passNumber                  = 2
	promptVersion               = "step50-implement-pass2@v1"
	defaultStep50TimeoutSeconds = 1800
	maxStderrTailBytes          = 64 * 1024
	maxManifestDetailRunes      = 300
)

// Step executes step 50 for one pass2 agent.
type Step struct{}

// Run executes the Claude CLI in the agent worktree and finalizes the step50
// manifest according to the pass2 contract.
func (Step) Run(ctx context.Context, run *orchestrator.StepRunContext) error {
	if run == nil {
		return errors.New("step50_implement: run context is required")
	}
	if run.Config == nil {
		return errors.New("step50_implement: config is required")
	}
	if run.TaskPackage == nil {
		return errors.New("step50_implement: task package is required")
	}

	logger := run.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With(
		slog.String("run_id", string(run.IO.RunID)),
		slog.Int("pass", passNumber),
		slog.String("agent", string(run.Agent)),
	)

	worktree, err := pass2Worktree(run.TaskPackage, run.Agent)
	if err != nil {
		return err
	}

	rulePayloads, err := LoadCandidateRulePayloads(run.IO)
	if err != nil {
		return fmt.Errorf("load candidate rules: %w", err)
	}
	candidateRuleIDs := make([]string, 0, len(rulePayloads))
	for _, payload := range rulePayloads {
		candidateRuleIDs = append(candidateRuleIDs, payload.ID)
	}

	promptText, err := renderPrompt(run.Config, promptData{
		TaskPackage:      *run.TaskPackage,
		Agent:            run.Agent,
		CandidateRuleIDs: candidateRuleIDs,
		RulePayloads:     rulePayloads,
		WorktreePath:     worktree.Path,
		Pass:             passNumber,
	})
	if err != nil {
		return fmt.Errorf("render prompt: %w", err)
	}

	agentRelDir := filepath.Join("50-pass2", string(run.Agent))
	agentDir, err := run.IO.ResolveRunRelative(agentRelDir)
	if err != nil {
		return fmt.Errorf("resolve agent directory: %w", err)
	}
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		return fmt.Errorf("mkdir agent directory: %w", err)
	}

	timeoutSeconds := stepTimeoutSeconds(run.Config)
	execCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	startedAt := time.Now().UTC()
	var stdout bytes.Buffer
	stderrTail := newTailBuffer(maxStderrTailBytes)
	cmd := exec.CommandContext(execCtx, run.Config.ClaudeBinary())
	cmd.Dir = worktree.Path
	cmd.Stdin = strings.NewReader(promptText)
	cmd.Stdout = &stdout
	cmd.Stderr = stderrTail

	logger.Info("starting step50 agent run", slog.String("worktree_path", worktree.Path), slog.Int("timeout_seconds", timeoutSeconds))
	runErr := cmd.Run()
	finishedAt := time.Now().UTC()

	if errors.Is(execCtx.Err(), context.Canceled) && !errors.Is(execCtx.Err(), context.DeadlineExceeded) && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ctx.Err()
	}

	manifestPath, err := run.IO.ManifestPath(passNumber, run.Agent)
	if err != nil {
		return fmt.Errorf("resolve manifest path: %w", err)
	}

	if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
		timeoutManifest := contracts.Manifest{
			Kind: contracts.ManifestKindTimeout,
			Value: contracts.ManifestTimeout{
				Kind:           contracts.ManifestKindTimeout,
				SchemaVersion:  "1",
				RunID:          run.IO.RunID,
				Pass:           passNumber,
				Agent:          run.Agent,
				TimeoutSeconds: timeoutSeconds,
				StartedAt:      startedAt,
				FinishedAt:     finishedAt,
			},
		}
		if err := internalio.WriteJSONAtomic(manifestPath, timeoutManifest); err != nil {
			return fmt.Errorf("write timeout manifest: %w", err)
		}
		logger.Warn("step50 agent timed out", slog.Int("timeout_seconds", timeoutSeconds))
		return nil
	}

	if runErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) {
			return fmt.Errorf("run claude binary: %w", runErr)
		}

		exitCode := exitErr.ExitCode()
		reason := interruption.Classify(exitCode, stdout.Bytes(), stderrTail.Bytes())
		errorManifest := contracts.Manifest{
			Kind: contracts.ManifestKindError,
			Value: contracts.ManifestError{
				Kind:          contracts.ManifestKindError,
				SchemaVersion: "1",
				RunID:         run.IO.RunID,
				Pass:          passNumber,
				Agent:         run.Agent,
				ExitCode:      exitCode,
				Reason:        string(reason),
				Detail:        manifestDetail(stderrTail.Bytes(), stdout.Bytes()),
				StartedAt:     startedAt,
				FinishedAt:    finishedAt,
			},
		}
		if err := internalio.WriteJSONAtomic(manifestPath, errorManifest); err != nil {
			return fmt.Errorf("write error manifest: %w", err)
		}
		logger.Warn("step50 agent failed", slog.Int("exit_code", exitCode), slog.String("reason", string(reason)))
		return nil
	}

	sessionRelPath := filepath.Join(agentRelDir, "session.jsonl")
	sessionPath, err := run.IO.ResolveRunRelative(sessionRelPath)
	if err != nil {
		return fmt.Errorf("resolve session path: %w", err)
	}
	if err := internalio.WriteAtomic(sessionPath, stdout.Bytes()); err != nil {
		return fmt.Errorf("write session output: %w", err)
	}

	diffBytes, err := gitOutput(ctx, worktree.Path, "diff", worktree.BaseSHA+"..HEAD", "--binary")
	if err != nil {
		return fmt.Errorf("capture diff: %w", err)
	}
	diffRelPath := filepath.Join(agentRelDir, "diff.patch")
	diffPath, err := run.IO.ResolveRunRelative(diffRelPath)
	if err != nil {
		return fmt.Errorf("resolve diff path: %w", err)
	}
	if err := internalio.WriteAtomic(diffPath, diffBytes); err != nil {
		return fmt.Errorf("write diff patch: %w", err)
	}

	headSHABytes, err := gitOutput(ctx, worktree.Path, "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("resolve head sha: %w", err)
	}
	headSHA := strings.TrimSpace(string(headSHABytes))

	checklistBytes, err := checklistResultBytes(worktree.Path)
	if err != nil {
		return fmt.Errorf("read checklist result: %w", err)
	}
	checklistRelPath := filepath.Join(agentRelDir, "checklist-result.json")
	checklistPath, err := run.IO.ResolveRunRelative(checklistRelPath)
	if err != nil {
		return fmt.Errorf("resolve checklist path: %w", err)
	}
	if err := internalio.WriteAtomic(checklistPath, checklistBytes); err != nil {
		return fmt.Errorf("write checklist result: %w", err)
	}

	successManifest := contracts.Manifest{
		Kind: contracts.ManifestKindSuccess,
		Value: contracts.ManifestSuccess{
			Kind:          contracts.ManifestKindSuccess,
			SchemaVersion: "1",
			RunID:         run.IO.RunID,
			Pass:          passNumber,
			Agent:         run.Agent,
			BranchName:    worktree.Branch,
			HeadSHA:       headSHA,
			BaseSHA:       worktree.BaseSHA,
			DiffPath:      diffRelPath,
			SessionPath:   sessionRelPath,
			ChecklistPath: checklistRelPath,
			PromptVersion: promptVersion,
			StartedAt:     startedAt,
			FinishedAt:    finishedAt,
		},
	}
	if err := internalio.WriteJSONAtomic(manifestPath, successManifest); err != nil {
		return fmt.Errorf("write success manifest: %w", err)
	}

	logger.Info("step50 agent completed", slog.String("head_sha", headSHA))
	return nil
}

func pass2Worktree(pkg *contracts.TaskPackage, agent contracts.AgentID) (contracts.WorktreeAllocation, error) {
	for _, worktree := range pkg.Worktrees {
		if worktree.Pass == passNumber && worktree.Agent == agent {
			return worktree, nil
		}
	}
	return contracts.WorktreeAllocation{}, fmt.Errorf("step50_implement: missing pass2 worktree: agent=%s", agent)
}

func stepTimeoutSeconds(cfg *config.Config) int {
	if cfg == nil {
		return defaultStep50TimeoutSeconds
	}
	if timeout, ok := cfg.StepTimeouts["step50"]; ok && timeout > 0 {
		return timeout
	}
	return defaultStep50TimeoutSeconds
}

func gitOutput(ctx context.Context, worktreePath string, args ...string) ([]byte, error) {
	gitArgs := append([]string{"-C", worktreePath}, args...)
	cmd := exec.CommandContext(ctx, "git", gitArgs...)
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, err
	}
	return output, nil
}

func checklistResultBytes(worktreePath string) ([]byte, error) {
	path := filepath.Join(worktreePath, "checklist-result.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []byte("{}"), nil
		}
		return nil, err
	}
	return data, nil
}

func manifestDetail(stderr, stdout []byte) string {
	detail := strings.TrimSpace(string(stderr))
	if detail == "" {
		detail = strings.TrimSpace(string(stdout))
	}
	return truncateRunes(detail, maxManifestDetailRunes)
}

func truncateRunes(s string, limit int) string {
	if limit <= 0 || utf8.RuneCountInString(s) <= limit {
		return s
	}
	runes := []rune(s)
	return string(runes[:limit])
}

type tailBuffer struct {
	max int
	buf []byte
}

func newTailBuffer(max int) *tailBuffer {
	return &tailBuffer{max: max}
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	if b.max <= 0 {
		return len(p), nil
	}
	if len(p) >= b.max {
		b.buf = append(b.buf[:0], p[len(p)-b.max:]...)
		return len(p), nil
	}
	if overflow := len(b.buf) + len(p) - b.max; overflow > 0 {
		b.buf = append(b.buf[:0], b.buf[overflow:]...)
	}
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *tailBuffer) Bytes() []byte {
	out := make([]byte, len(b.buf))
	copy(out, b.buf)
	return out
}
