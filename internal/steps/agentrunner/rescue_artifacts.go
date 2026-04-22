package agentrunner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

const (
	RescueGitOutputMaxBytes int64 = 32 << 20
	RescueAggregateMaxBytes int64 = 256 << 20
	RescueAggregateMaxFiles       = 10_000
)

var (
	ErrRescueArtifactTooLarge    = errors.New("agentrunner: rescue artifact exceeds size limit")
	ErrRescueArtifactBudget      = errors.New("agentrunner: rescue artifact aggregate budget exceeded")
	ErrCleanupTimeout            = errors.New("agentrunner: cleanup timed out while processes remained")
	errRescueStreamStdoutMissing = errors.New("agentrunner: stdout pipe is required")
)

type RescueArtifactTooLargeError struct {
	Path  string
	Size  int64
	Limit int64
}

func (e *RescueArtifactTooLargeError) Error() string {
	if e == nil {
		return ErrRescueArtifactTooLarge.Error()
	}
	return fmt.Sprintf("%s: path=%s size=%d limit=%d", ErrRescueArtifactTooLarge, e.Path, e.Size, e.Limit)
}

func (e *RescueArtifactTooLargeError) Unwrap() error {
	return ErrRescueArtifactTooLarge
}

type RescueArtifactBudgetError struct {
	Path       string
	TotalBytes int64
	ByteLimit  int64
	FileCount  int
	FileLimit  int
}

func (e *RescueArtifactBudgetError) Error() string {
	if e == nil {
		return ErrRescueArtifactBudget.Error()
	}
	return fmt.Sprintf("%s: path=%s total_bytes=%d byte_limit=%d file_count=%d file_limit=%d", ErrRescueArtifactBudget, e.Path, e.TotalBytes, e.ByteLimit, e.FileCount, e.FileLimit)
}

func (e *RescueArtifactBudgetError) Unwrap() error {
	return ErrRescueArtifactBudget
}

type RescueArtifactBudget struct {
	byteLimit  int64
	fileLimit  int
	totalBytes int64
	fileCount  int
}

func NewRescueArtifactBudget(byteLimit int64, fileLimit int) *RescueArtifactBudget {
	if byteLimit <= 0 {
		byteLimit = RescueAggregateMaxBytes
	}
	if fileLimit <= 0 {
		fileLimit = RescueAggregateMaxFiles
	}
	return &RescueArtifactBudget{
		byteLimit: byteLimit,
		fileLimit: fileLimit,
	}
}

func (b *RescueArtifactBudget) AddFile(path string, size int64) error {
	if b == nil {
		return nil
	}
	nextCount := b.fileCount + 1
	if nextCount > b.fileLimit {
		return &RescueArtifactBudgetError{
			Path:       path,
			TotalBytes: b.totalBytes,
			ByteLimit:  b.byteLimit,
			FileCount:  nextCount,
			FileLimit:  b.fileLimit,
		}
	}
	nextBytes := b.totalBytes + size
	if nextBytes > b.byteLimit {
		return &RescueArtifactBudgetError{
			Path:       path,
			TotalBytes: nextBytes,
			ByteLimit:  b.byteLimit,
			FileCount:  nextCount,
			FileLimit:  b.fileLimit,
		}
	}
	b.totalBytes = nextBytes
	b.fileCount = nextCount
	return nil
}

func StreamCommandOutputToFile(ctx context.Context, cmd *exec.Cmd, destPath string, sizeLimit int64) (int64, []byte, error) {
	if cmd == nil {
		return 0, nil, errors.New("agentrunner: command is required")
	}
	if sizeLimit <= 0 {
		sizeLimit = RescueGitOutputMaxBytes
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return 0, nil, err
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}

	tempFile, err := os.CreateTemp(filepath.Dir(destPath), filepath.Base(destPath)+".tmp-*")
	if err != nil {
		return 0, nil, err
	}
	tempPath := tempFile.Name()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = tempFile.Close()
		_ = os.Remove(tempPath)
		return 0, nil, fmt.Errorf("%w: %v", errRescueStreamStdoutMissing, err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	cleanup := func(err error) (int64, []byte, error) {
		_ = tempFile.Close()
		_ = os.Remove(tempPath)
		return 0, stderr.Bytes(), err
	}

	if err := cmd.Start(); err != nil {
		return cleanup(err)
	}

	written, copyErr := io.CopyBuffer(tempFile, io.LimitReader(&contextReader{ctx: ctx, reader: stdout}, sizeLimit+1), make([]byte, 32<<10))
	if copyErr != nil {
		_ = killCommandProcessGroup(cmd)
		_ = cmd.Wait()
		return cleanup(copyErr)
	}
	if written > sizeLimit {
		_ = killCommandProcessGroup(cmd)
		_ = cmd.Wait()
		return cleanup(&RescueArtifactTooLargeError{Path: destPath, Size: written, Limit: sizeLimit})
	}
	waitErr := cmd.Wait()
	syncErr := tempFile.Sync()
	closeErr := tempFile.Close()
	renameErr := error(nil)
	if syncErr == nil && closeErr == nil {
		renameErr = os.Rename(tempPath, destPath)
	}

	switch {
	case ctx.Err() != nil:
		return cleanup(ctx.Err())
	case waitErr != nil:
		return cleanup(waitErr)
	case syncErr != nil:
		return cleanup(syncErr)
	case closeErr != nil:
		return cleanup(closeErr)
	case renameErr != nil:
		return cleanup(renameErr)
	default:
		return written, stderr.Bytes(), nil
	}
}

func killCommandProcessGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
