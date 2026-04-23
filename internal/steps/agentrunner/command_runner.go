package agentrunner

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
)

type CommandRequest struct {
	Binary                 string
	Args                   []string
	Workdir                string
	Prompt                 string
	SessionPath            string
	Timeout                time.Duration
	Env                    []string
	OnStart                func(ProcessLease, time.Time) error
	ErrPrefix              string
	Now                    func() time.Time
	StartDescendantTracker func(int, time.Duration) *DescendantTracker
	CleanupProcessTree     func(ProcessLease, int, *DescendantTracker) error
}

type CommandResult struct {
	ExitCode      int
	TimedOut      bool
	StdoutSnippet []byte
	StderrSnippet []byte
	StartedAt     time.Time
	FinishedAt    time.Time
	Lease         ProcessLease
	CleanupErr    error
}

func RunCommand(ctx context.Context, req CommandRequest) (CommandResult, error) {
	if req.Binary == "" {
		return CommandResult{}, fmt.Errorf("%s: command binary is required", req.ErrPrefix)
	}
	if req.StartDescendantTracker == nil {
		req.StartDescendantTracker = StartDescendantTracker
	}
	if req.CleanupProcessTree == nil {
		req.CleanupProcessTree = CleanupProcessTree
	}
	if err := os.MkdirAll(filepath.Dir(req.SessionPath), 0o755); err != nil {
		return CommandResult{}, err
	}
	sessionFile, err := os.OpenFile(req.SessionPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return CommandResult{}, err
	}
	defer sessionFile.Close()

	timeoutCtx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()

	cmd := exec.CommandContext(timeoutCtx, req.Binary, req.Args...)
	if resolved, err := exec.LookPath(req.Binary); err == nil {
		cmd.Path = resolved
	}
	cmd.Dir = req.Workdir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdin = strings.NewReader(req.Prompt)
	cmd.Env = withBinaryDirOnPATH(processenv.Sanitize(req.Env...), cmd.Path)

	stdoutTail := newTailBuffer(8 << 10)
	stderrTail := newTailBuffer(8 << 10)
	cmd.Stdout = io.MultiWriter(sessionFile, stdoutTail)
	cmd.Stderr = stderrTail

	now := req.Now
	if now == nil {
		now = time.Now
	}
	result := CommandResult{StartedAt: now().UTC()}
	if err := cmd.Start(); err != nil {
		return CommandResult{}, err
	}
	lease, err := ResolveProcessLease(cmd.Process.Pid)
	if err != nil {
		_ = KillProcessGroup(cmd.Process.Pid)
		_ = cmd.Wait()
		return CommandResult{}, err
	}
	tracker := req.StartDescendantTracker(lease.PID, 25*time.Millisecond)
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
			cleanupErr := req.CleanupProcessTree(lease, lease.PID, tracker)
			_ = cmd.Wait()
			return CommandResult{}, errors.Join(err, cleanupErr)
		}
	}

	groupKillDone := make(chan struct{})
	go func(pgid int) {
		select {
		case <-timeoutCtx.Done():
			_ = KillProcessGroup(pgid)
		case <-groupKillDone:
		}
	}(lease.PGID)

	waitErr := cmd.Wait()
	close(groupKillDone)
	if tracker != nil {
		tracker.CaptureBurst(25 * time.Millisecond)
		tracker.Stop()
	}
	result.CleanupErr = req.CleanupProcessTree(lease, lease.PID, tracker)
	result.FinishedAt = now().UTC()
	result.StdoutSnippet = stdoutTail.Bytes()
	result.StderrSnippet = stderrTail.Bytes()

	switch {
	case waitErr == nil:
		return result, nil
	case timeoutCtx.Err() == context.DeadlineExceeded:
		result.TimedOut = true
		return result, nil
	case ctx.Err() != nil:
		return CommandResult{}, ctx.Err()
	}

	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		result.ExitCode = exitCode(exitErr)
		return result, nil
	}
	return CommandResult{}, waitErr
}

func withBinaryDirOnPATH(env []string, binaryPath string) []string {
	if binaryPath == "" {
		return env
	}
	dir := filepath.Dir(binaryPath)
	if dir == "." || dir == "" {
		return env
	}
	out := append([]string(nil), env...)
	for i, item := range out {
		if !strings.HasPrefix(item, "PATH=") {
			continue
		}
		current := strings.TrimPrefix(item, "PATH=")
		if current == "" {
			out[i] = "PATH=" + dir
			return out
		}
		out[i] = "PATH=" + dir + string(os.PathListSeparator) + current
		return out
	}
	return append(out, "PATH="+dir)
}

func InterruptionReason(exitCode int, stdout, stderr []byte) contracts.InterruptedReason {
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

func TruncateDetail(stderrSnippet, stdoutSnippet []byte) string {
	detail := strings.TrimSpace(string(stderrSnippet))
	if detail == "" {
		detail = strings.TrimSpace(string(stdoutSnippet))
	}
	if len(detail) <= 300 {
		return detail
	}
	return strings.TrimSpace(detail[:300])
}

func AppendCleanupDetail(stderrSnippet []byte, cleanupErr error) []byte {
	detail := strings.TrimSpace(string(stderrSnippet))
	if cleanupErr == nil {
		return stderrSnippet
	}
	if detail == "" {
		return []byte(cleanupErr.Error())
	}
	return []byte(detail + "\ncleanup: " + cleanupErr.Error())
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
