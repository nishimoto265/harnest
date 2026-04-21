// Package step50_implement は step50(pass2 implement) の agent 実行境界を担当する。
// 候補ルールを読み込んで prompt を生成し、Claude CLI を worktree 上で起動し、
// 成功時のみ diff/session/checklist を確定した上で manifest.json を atomic write する。
package step50_implement

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/interruption"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	ilog "github.com/nishimoto265/auto-improve/internal/logger"
	"github.com/nishimoto265/auto-improve/internal/orchestrator"
)

const (
	passNumber                  = 2
	step50PromptVersion         = "step50-implement-pass2@v1"
	defaultStep50TimeoutSeconds = 1800
	maxStderrTailBytes          = 64 << 10
	stderrDetailRuneLimit       = 300
	checklistFilename           = "checklist-result.json"
)

// Step runs step50 pass2 implementation for one agent.
type Step struct{}

// Run executes the pass2 implement agent, then writes the terminal manifest.
func (Step) Run(ctx context.Context, run *orchestrator.StepRunContext) error {
	if run == nil {
		return errors.New("step50_implement: step run context is required")
	}
	if run.Config == nil {
		return errors.New("step50_implement: config is required")
	}
	if run.TaskPackage == nil {
		return errors.New("step50_implement: task package is required")
	}

	logger := run.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	logger = logger.With(
		slog.String(ilog.FieldRunID, string(run.IO.RunID)),
		slog.String(ilog.FieldAgent, string(run.Agent)),
		slog.Int(ilog.FieldPass, passNumber),
	)

	worktree, err := resolveWorktree(run.TaskPackage, passNumber, run.Agent)
	if err != nil {
		return err
	}

	candidatesPath, err := run.IO.ResolveRunRelative(filepath.Join("40", "candidates.json"))
	if err != nil {
		return fmt.Errorf("resolve candidates path: %w", err)
	}
	rulePayloads, err := LoadRulePayloads(nil, run.IO.RunsBase, candidatesPath)
	if err != nil {
		return fmt.Errorf("load rule payloads: %w", err)
	}
	candidateRuleIDs := rulePayloadIDs(rulePayloads)

	renderedPrompt, err := RenderPrompt(PromptData{
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

	agentPrefix := filepath.Join("50-pass2", string(run.Agent))
	agentDir, err := run.IO.ResolveRunRelative(agentPrefix)
	if err != nil {
		return fmt.Errorf("resolve agent dir: %w", err)
	}
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		return fmt.Errorf("create agent dir: %w", err)
	}

	manifestPath, err := run.IO.ManifestPath(passNumber, run.Agent)
	if err != nil {
		return fmt.Errorf("resolve manifest path: %w", err)
	}

	stepTimeoutSeconds := stepTimeout(run.Config)
	startedAt := time.Now().UTC()
	timeoutCtx, cancel := context.WithTimeout(ctx, time.Duration(stepTimeoutSeconds)*time.Second)
	defer cancel()

	var stdout bytes.Buffer
	stderrTail := newTailBuffer(maxStderrTailBytes)

	logger.Info("starting step50 implement command")

	cmd := exec.CommandContext(timeoutCtx, run.Config.ClaudeBinary(), "-p", "--output-format=stream-json")
	cmd.Dir = worktree.Path
	cmd.Stdin = strings.NewReader(renderedPrompt)
	cmd.Stdout = &stdout
	cmd.Stderr = stderrTail

	err = cmd.Run()
	finishedAt := time.Now().UTC()

	if errors.Is(timeoutCtx.Err(), context.DeadlineExceeded) {
		logger.Warn("step50 implement timed out", slog.Int("timeout_seconds", stepTimeoutSeconds))
		return writeManifest(manifestPath, contracts.Manifest{
			Kind: contracts.ManifestKindTimeout,
			Value: contracts.ManifestTimeout{
				Kind:           contracts.ManifestKindTimeout,
				SchemaVersion:  "1",
				RunID:          run.IO.RunID,
				Pass:           passNumber,
				Agent:          run.Agent,
				TimeoutSeconds: stepTimeoutSeconds,
				StartedAt:      startedAt,
				FinishedAt:     finishedAt,
			},
		})
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		exitCode := exitCodeFromError(err)
		kind := interruption.Classify(exitCode, stdout.Bytes(), stderrTail.Bytes())
		logger.Warn("step50 implement exited with non-zero status",
			slog.Int("exit_code", exitCode),
			slog.String("reason", string(kind)),
		)
		return writeManifest(manifestPath, contracts.Manifest{
			Kind: contracts.ManifestKindError,
			Value: contracts.ManifestError{
				Kind:          contracts.ManifestKindError,
				SchemaVersion: "1",
				RunID:         run.IO.RunID,
				Pass:          passNumber,
				Agent:         run.Agent,
				ExitCode:      exitCode,
				Reason:        string(kind),
				Detail:        truncateRunes(strings.TrimSpace(stderrTail.String()), stderrDetailRuneLimit),
				StartedAt:     startedAt,
				FinishedAt:    finishedAt,
			},
		})
	}

	sessionPath, err := run.IO.ResolveRunRelative(filepath.Join(agentPrefix, "session.jsonl"))
	if err != nil {
		return fmt.Errorf("resolve session path: %w", err)
	}
	if err := internalio.WriteAtomic(sessionPath, stdout.Bytes()); err != nil {
		return fmt.Errorf("write session: %w", err)
	}

	diffBytes, err := gitOutput(ctx, worktree.Path, "diff", worktree.BaseSHA+"..HEAD", "--binary")
	if err != nil {
		return fmt.Errorf("capture diff: %w", err)
	}
	diffPath, err := run.IO.ResolveRunRelative(filepath.Join(agentPrefix, "diff.patch"))
	if err != nil {
		return fmt.Errorf("resolve diff path: %w", err)
	}
	if err := internalio.WriteAtomic(diffPath, diffBytes); err != nil {
		return fmt.Errorf("write diff: %w", err)
	}

	headSHABytes, err := gitOutput(ctx, worktree.Path, "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("resolve head sha: %w", err)
	}
	headSHA := strings.TrimSpace(string(headSHABytes))

	checklistData, err := readChecklistArtifact(worktree.Path)
	if err != nil {
		return err
	}
	checklistPath, err := run.IO.ResolveRunRelative(filepath.Join(agentPrefix, checklistFilename))
	if err != nil {
		return fmt.Errorf("resolve checklist path: %w", err)
	}
	if err := internalio.WriteAtomic(checklistPath, checklistData); err != nil {
		return fmt.Errorf("write checklist: %w", err)
	}

	logger.Info("step50 implement completed successfully", slog.String("head_sha", headSHA))

	return writeManifest(manifestPath, contracts.Manifest{
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
			DiffPath:      filepath.Join(agentPrefix, "diff.patch"),
			SessionPath:   filepath.Join(agentPrefix, "session.jsonl"),
			ChecklistPath: filepath.Join(agentPrefix, checklistFilename),
			PromptVersion: step50PromptVersion,
			StartedAt:     startedAt,
			FinishedAt:    finishedAt,
		},
	})
}

func resolveWorktree(pkg *contracts.TaskPackage, pass int, agent contracts.AgentID) (contracts.WorktreeAllocation, error) {
	for _, worktree := range pkg.Worktrees {
		if worktree.Pass == pass && worktree.Agent == agent {
			return worktree, nil
		}
	}
	return contracts.WorktreeAllocation{}, fmt.Errorf("step50_implement: missing worktree allocation: pass=%d agent=%s", pass, agent)
}

func stepTimeout(cfg *config.Config) int {
	if cfg == nil || cfg.StepTimeouts == nil {
		return defaultStep50TimeoutSeconds
	}
	timeoutSeconds := cfg.StepTimeouts["step50"]
	if timeoutSeconds <= 0 {
		return defaultStep50TimeoutSeconds
	}
	return timeoutSeconds
}

func writeManifest(path string, manifest contracts.Manifest) error {
	return internalio.WriteJSONAtomic(path, manifest)
}

func rulePayloadIDs(payloads []RulePayload) []string {
	if len(payloads) == 0 {
		return nil
	}
	ids := make([]string, 0, len(payloads))
	for _, payload := range payloads {
		ids = append(ids, payload.ID)
	}
	return ids
}

func readChecklistArtifact(worktreePath string) ([]byte, error) {
	path := filepath.Join(worktreePath, checklistFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []byte("{}\n"), nil
		}
		return nil, fmt.Errorf("read checklist artifact: %w", err)
	}
	return data, nil
}

func gitOutput(ctx context.Context, worktreePath string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", worktreePath}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return output, nil
}

func exitCodeFromError(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return 1
}

func truncateRunes(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes])
}

type tailBuffer struct {
	limit int
	data  []byte
}

func newTailBuffer(limit int) *tailBuffer {
	return &tailBuffer{limit: limit}
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	if b.limit <= 0 {
		return len(p), nil
	}
	if len(p) >= b.limit {
		b.data = append(b.data[:0], p[len(p)-b.limit:]...)
		return len(p), nil
	}
	if len(b.data)+len(p) > b.limit {
		drop := len(b.data) + len(p) - b.limit
		b.data = append(b.data[drop:], p...)
		return len(p), nil
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

func (b *tailBuffer) Bytes() []byte {
	return append([]byte(nil), b.data...)
}

func (b *tailBuffer) String() string {
	return string(b.data)
}
