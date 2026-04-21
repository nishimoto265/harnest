package step20_implement

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type ClaudeRunRequest struct {
	Binary      string
	WorkDir     string
	Prompt      string
	Timeout     time.Duration
	SessionPath string
	Env         []string
}

type ClaudeRunResult struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
	TimedOut bool
}

type ClaudeRunner interface {
	Run(ctx context.Context, req ClaudeRunRequest) (ClaudeRunResult, error)
}

type commandClaudeRunner struct{}

func (commandClaudeRunner) Run(ctx context.Context, req ClaudeRunRequest) (ClaudeRunResult, error) {
	if err := ensureDir(filepath.Dir(req.SessionPath)); err != nil {
		return ClaudeRunResult{}, err
	}
	sessionFile, err := os.OpenFile(req.SessionPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return ClaudeRunResult{}, err
	}
	defer sessionFile.Close()

	runCtx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, req.Binary)
	cmd.Dir = req.WorkDir
	cmd.Env = append(os.Environ(), req.Env...)
	cmd.Stdin = bytes.NewReader([]byte(req.Prompt))

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return ClaudeRunResult{}, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return ClaudeRunResult{}, err
	}

	if err := cmd.Start(); err != nil {
		return ClaudeRunResult{}, err
	}

	var stdoutBuf bytes.Buffer
	var stderrBuf bytes.Buffer
	var wg sync.WaitGroup
	var stdoutErr error
	var stderrErr error

	wg.Add(2)
	go func() {
		defer wg.Done()
		_, stdoutErr = io.Copy(io.MultiWriter(&stdoutBuf, sessionFile), stdoutPipe)
	}()
	go func() {
		defer wg.Done()
		_, stderrErr = io.Copy(&stderrBuf, stderrPipe)
	}()

	waitErr := cmd.Wait()
	wg.Wait()
	if syncErr := sessionFile.Sync(); syncErr != nil {
		return ClaudeRunResult{}, syncErr
	}
	stdoutErr = normalizePipeCopyError(stdoutErr)
	stderrErr = normalizePipeCopyError(stderrErr)
	if stdoutErr != nil {
		return ClaudeRunResult{}, stdoutErr
	}
	if stderrErr != nil {
		return ClaudeRunResult{}, stderrErr
	}

	result := ClaudeRunResult{
		Stdout: stdoutBuf.Bytes(),
		Stderr: stderrBuf.Bytes(),
	}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		result.TimedOut = true
		return result, nil
	}
	if waitErr == nil {
		return result, nil
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result, nil
	}
	return ClaudeRunResult{}, waitErr
}

func normalizePipeCopyError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, os.ErrClosed) {
		return nil
	}
	if strings.Contains(err.Error(), "file already closed") {
		return nil
	}
	return err
}
