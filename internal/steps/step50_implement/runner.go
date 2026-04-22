package step50_implement

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/interruption"
	"github.com/nishimoto265/auto-improve/internal/processenv"
	"github.com/nishimoto265/auto-improve/internal/steps/agentrunner"
)

type runner interface {
	Run(ctx context.Context, req runnerRequest) (runnerResult, error)
}

type runnerRequest struct {
	Binary      string
	Workdir     string
	Prompt      string
	SessionPath string
	Timeout     time.Duration
	Env         []string
	OnStart     func(agentrunner.ProcessLease, time.Time) error
}

type runnerResult struct {
	ExitCode      int
	TimedOut      bool
	StdoutSnippet []byte
	StderrSnippet []byte
	StartedAt     time.Time
	FinishedAt    time.Time
	Lease         agentrunner.ProcessLease
}

type commandRunner struct {
	now func() time.Time
}

var (
	startDescendantTracker = agentrunner.StartDescendantTracker
	cleanupProcessTree     = agentrunner.CleanupProcessTree
	// cleanupProcessTreeFailClosed: see step20 runner.go equivalent.
	cleanupProcessTreeFailClosed = true
)

func (r commandRunner) Run(ctx context.Context, req runnerRequest) (runnerResult, error) {
	if req.Binary == "" {
		return runnerResult{}, errors.New("step50: claude binary is required")
	}
	if err := ensureDir(filepath.Dir(req.SessionPath)); err != nil {
		return runnerResult{}, err
	}
	sessionFile, err := os.OpenFile(req.SessionPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return runnerResult{}, err
	}
	defer sessionFile.Close()

	timeoutCtx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()

	cmd := exec.CommandContext(timeoutCtx, req.Binary)
	cmd.Dir = req.Workdir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdin = strings.NewReader(req.Prompt)
	cmd.Env = processenv.Sanitize(req.Env...)

	stdoutTail := newTailBuffer(8 << 10)
	stderrTail := newTailBuffer(8 << 10)
	cmd.Stdout = io.MultiWriter(sessionFile, stdoutTail)
	cmd.Stderr = stderrTail

	result := runnerResult{StartedAt: r.now().UTC()}
	if err := cmd.Start(); err != nil {
		return runnerResult{}, err
	}
	lease, err := agentrunner.ResolveProcessLease(cmd.Process.Pid)
	if err != nil {
		_ = agentrunner.KillProcessGroup(cmd.Process.Pid)
		_ = cmd.Wait()
		return runnerResult{}, err
	}
	tracker := startDescendantTracker(lease.PID, 25*time.Millisecond)
	if tracker != nil {
		tracker.CaptureBurst(250 * time.Millisecond)
	}
	result.Lease = lease
	if req.OnStart != nil {
		if err := req.OnStart(lease, result.StartedAt); err != nil {
			if tracker != nil {
				tracker.Stop()
				defer func() { tracker = nil }()
			}
			_ = cleanupProcessTree(lease, lease.PID, tracker)
			_ = cmd.Wait()
			return runnerResult{}, err
		}
	}

	groupKillDone := make(chan struct{})
	go func(pgid int) {
		select {
		case <-timeoutCtx.Done():
			_ = killProcessGroup(pgid)
		case <-groupKillDone:
		}
	}(lease.PGID)

	waitErr := cmd.Wait()
	close(groupKillDone)
	if tracker != nil {
		tracker.CaptureBurst(25 * time.Millisecond)
		tracker.Stop()
	}
	cleanupErr := cleanupProcessTree(lease, lease.PID, tracker)
	result.FinishedAt = r.now().UTC()
	result.StdoutSnippet = stdoutTail.Bytes()
	result.StderrSnippet = stderrTail.Bytes()

	switch {
	case waitErr == nil:
		// M5: fail closed on success path (see step20 runner).
		if cleanupErr != nil && cleanupProcessTreeFailClosed {
			return runnerResult{}, fmt.Errorf("step50: cleanup process tree after success: %w", cleanupErr)
		}
		return result, nil
	case timeoutCtx.Err() == context.DeadlineExceeded:
		result.TimedOut = true
		return result, nil
	case ctx.Err() != nil:
		return runnerResult{}, ctx.Err()
	}

	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		result.ExitCode = exitCode(exitErr)
		return result, nil
	}
	return runnerResult{}, waitErr
}

func killProcessGroup(pgid int) error {
	return agentrunner.KillProcessGroup(pgid)
}

func exitCode(exitErr *exec.ExitError) int {
	if exitErr == nil {
		return 0
	}
	if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
		if status.Signaled() {
			return 128 + int(status.Signal())
		}
		return status.ExitStatus()
	}
	return exitErr.ExitCode()
}

type tailBuffer struct {
	max int
	buf []byte
}

func newTailBuffer(max int) *tailBuffer {
	return &tailBuffer{max: max}
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	if len(p) >= b.max {
		b.buf = append(b.buf[:0], p[len(p)-b.max:]...)
		return len(p), nil
	}
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.max {
		b.buf = append([]byte(nil), b.buf[len(b.buf)-b.max:]...)
	}
	return len(p), nil
}

func (b *tailBuffer) Bytes() []byte {
	return append([]byte(nil), b.buf...)
}

func interruptionReason(exitCode int, stdout, stderr []byte) contracts.InterruptedReason {
	switch interruption.Classify(exitCode, stdout, stderr) {
	case interruption.InterruptionKindRateLimit:
		return contracts.InterruptedReasonRateLimit
	case interruption.InterruptionKindBudget:
		return contracts.InterruptedReasonBudget
	case interruption.InterruptionKindContext:
		return contracts.InterruptedReasonContext
	case interruption.InterruptionKindSignal:
		return contracts.InterruptedReasonSignal
	default:
		return contracts.InterruptedReasonUnknown
	}
}

func truncateDetail(stderrSnippet, stdoutSnippet []byte) string {
	detail := strings.TrimSpace(string(stderrSnippet))
	if detail == "" {
		detail = strings.TrimSpace(string(stdoutSnippet))
	}
	if len(detail) <= 300 {
		return detail
	}
	return strings.TrimSpace(detail[:300])
}
