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
	"runtime"
	"strings"
	"syscall"
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
func (Step) Run(ctx context.Context, run *orchestrator.StepRunContext) (retErr error) {
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
	rulePayloads, err := LoadRulePayloads(run.IO.RunDir(), candidatesPath)
	if err != nil {
		return fmt.Errorf("load rule payloads: %w", err)
	}

	renderedPrompt, err := RenderPrompt(PromptData{
		TaskPackage:  *run.TaskPackage,
		Agent:        run.Agent,
		RulePayloads: rulePayloads,
		WorktreePath: worktree.Path,
		Pass:         passNumber,
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
	defer func() {
		if retErr == nil {
			return
		}
		if cleanupErr := cleanupAgentArtifacts(agentDir); cleanupErr != nil {
			retErr = errors.Join(retErr, cleanupErr)
		}
	}()

	manifestPath, err := run.IO.ManifestPath(passNumber, run.Agent)
	if err != nil {
		return fmt.Errorf("resolve manifest path: %w", err)
	}

	stepTimeoutSeconds := stepTimeout(run.Config)
	startedAt := time.Now().UTC()
	execCtx, cancel := context.WithTimeout(ctx, time.Duration(stepTimeoutSeconds)*time.Second)
	defer cancel()

	var stdout bytes.Buffer
	stderrTail := newTailBuffer(maxStderrTailBytes)

	logger.Info("starting step50 implement command")

	cmd := exec.Command(run.Config.ClaudeBinary(), "-p", "--output-format=stream-json")
	cmd.Dir = worktree.Path
	cmd.Stdin = strings.NewReader(renderedPrompt)
	cmd.Stdout = &stdout
	cmd.Stderr = stderrTail
	configureCommandProcessGroup(cmd)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start step50 implement command: %w", err)
	}

	waitDone := make(chan struct{})
	defer close(waitDone)
	go func() {
		select {
		case <-execCtx.Done():
			_ = killCommandProcessGroup(cmd)
		case <-waitDone:
		}
	}()

	err = cmd.Wait()
	finishedAt := clampFinishedAt(startedAt, time.Now().UTC())

	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if isLocalTimeout(err, execCtx.Err()) {
		logger.Warn("step50 implement timed out", slog.Int("timeout_seconds", stepTimeoutSeconds))
		if cleanupErr := cleanupAgentArtifacts(agentDir); cleanupErr != nil {
			return cleanupErr
		}
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
		exitCode := exitCodeFromError(err)
		kind := interruption.Classify(exitCode, stdout.Bytes(), stderrTail.Bytes())
		logger.Warn("step50 implement exited with non-zero status",
			slog.Int("exit_code", exitCode),
			slog.String("reason", string(kind)),
		)
		if cleanupErr := cleanupAgentArtifacts(agentDir); cleanupErr != nil {
			return cleanupErr
		}
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
	if ctx.Err() != nil {
		return ctx.Err()
	}

	checklistData, err := readChecklistArtifact(worktree.Path, run.IO.RunID, run.Agent)
	if err != nil {
		return err
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

func readChecklistArtifact(worktreePath string, runID contracts.RunID, agent contracts.AgentID) ([]byte, error) {
	path := filepath.Join(worktreePath, checklistFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read checklist artifact: %w", err)
	}

	var checklist contracts.ChecklistResult
	if err := contracts.DecodeStrictJSON(data, &checklist); err != nil {
		return nil, fmt.Errorf("decode checklist artifact: %w", err)
	}
	if checklist.RunID != runID {
		return nil, fmt.Errorf("checklist artifact run_id mismatch: got=%s want=%s", checklist.RunID, runID)
	}
	if checklist.Pass != passNumber {
		return nil, fmt.Errorf("checklist artifact pass mismatch: got=%d want=%d", checklist.Pass, passNumber)
	}
	if checklist.Agent != agent {
		return nil, fmt.Errorf("checklist artifact agent mismatch: got=%s want=%s", checklist.Agent, agent)
	}

	encoded, err := contracts.MarshalStrict(checklist)
	if err != nil {
		return nil, fmt.Errorf("marshal checklist artifact: %w", err)
	}
	return encoded, nil
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
		if exitErr.ProcessState != nil {
			if status, ok := exitErr.ProcessState.Sys().(syscall.WaitStatus); ok && !exitErr.ProcessState.Exited() && status.Signaled() {
				return 128 + int(status.Signal())
			}
		}
		return exitErr.ExitCode()
	}
	return 1
}

func cleanupAgentArtifacts(agentDir string) error {
	var cleanupErrs []error
	for _, name := range []string{"diff.patch", "session.jsonl", checklistFilename} {
		path := filepath.Join(agentDir, name)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("remove %s: %w", path, err))
		}
	}
	return errors.Join(cleanupErrs...)
}

func isLocalTimeout(err, execErr error) bool {
	return err != nil && errors.Is(execErr, context.DeadlineExceeded)
}

func clampFinishedAt(startedAt, finishedAt time.Time) time.Time {
	if finishedAt.Before(startedAt) {
		return startedAt
	}
	return finishedAt
}

func configureCommandProcessGroup(cmd *exec.Cmd) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

func killCommandProcessGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		err := cmd.Process.Kill()
		if errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		return err
	}

	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
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
