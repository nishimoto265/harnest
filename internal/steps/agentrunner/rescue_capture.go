package agentrunner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/nishimoto265/auto-improve/internal/processenv"
)

const (
	RescueDiffLimitBytes          int64 = 32 << 20
	RescueArtifactTotalLimitBytes int64 = 256 << 20
	RescueArtifactFileLimit             = 10000
)

var (
	ErrRescueDiffOverLimit    = errors.New("agentrunner: rescue diff exceeded 32MB limit")
	ErrRescueStorageOverLimit = errors.New("agentrunner: rescue artifacts exceeded aggregate storage budget")
)

type RescueArtifactBudget struct {
	MaxBytes int64
	MaxFiles int

	totalBytes int64
	fileCount  int
}

func NewRescueArtifactBudget() RescueArtifactBudget {
	return RescueArtifactBudget{
		MaxBytes: RescueArtifactTotalLimitBytes,
		MaxFiles: RescueArtifactFileLimit,
	}
}

func (b *RescueArtifactBudget) RecordFile(path string, size int64) error {
	if b == nil {
		return nil
	}
	if size < 0 {
		size = 0
	}
	if b.MaxBytes <= 0 {
		b.MaxBytes = RescueArtifactTotalLimitBytes
	}
	if b.MaxFiles <= 0 {
		b.MaxFiles = RescueArtifactFileLimit
	}
	nextBytes := b.totalBytes + size
	nextFiles := b.fileCount + 1
	if nextBytes > b.MaxBytes || nextFiles > b.MaxFiles {
		return fmt.Errorf(
			"%w: path=%s bytes=%d/%d files=%d/%d",
			ErrRescueStorageOverLimit,
			path,
			nextBytes,
			b.MaxBytes,
			nextFiles,
			b.MaxFiles,
		)
	}
	b.totalBytes = nextBytes
	b.fileCount = nextFiles
	return nil
}

func StreamGitOutputWithLimit(ctx context.Context, worktreePath, errPrefix, destPath string, limit int64, args ...string) (int64, error) {
	if limit <= 0 {
		limit = RescueDiffLimitBytes
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return 0, err
	}
	tempFile, err := os.CreateTemp(filepath.Dir(destPath), filepath.Base(destPath)+".tmp-*")
	if err != nil {
		return 0, err
	}
	tempPath := tempFile.Name()
	closeWithErr := func(err error) (int64, error) {
		_ = tempFile.Close()
		_ = os.Remove(tempPath)
		return 0, err
	}

	cmdCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmd, err := processenv.TrustedCommandContext(cmdCtx, "git", append([]string{"-C", worktreePath}, args...)...)
	if err != nil {
		return closeWithErr(fmt.Errorf("%s: resolve git: %w", errPrefix, err))
	}
	cmd.Env = processenv.Sanitize()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return closeWithErr(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return closeWithErr(err)
	}

	written, copyErr := copyWithLimit(tempFile, stdout, limit)
	if copyErr != nil {
		cancel()
		_ = cmd.Wait()
		if ctx.Err() != nil {
			return closeWithErr(ctx.Err())
		}
		return closeWithErr(copyErr)
	}
	if written > limit {
		cancel()
	}
	waitErr := cmd.Wait()
	if written > limit {
		if ctx.Err() != nil {
			return closeWithErr(ctx.Err())
		}
		return closeWithErr(fmt.Errorf("%w: git %s bytes=%d limit=%d", ErrRescueDiffOverLimit, strings.Join(args, " "), written, limit))
	}
	if waitErr != nil {
		if ctx.Err() != nil {
			return closeWithErr(ctx.Err())
		}
		return closeWithErr(fmt.Errorf("%s: git %s: %w: %s", errPrefix, strings.Join(args, " "), waitErr, strings.TrimSpace(stderr.String())))
	}
	if err := tempFile.Sync(); err != nil {
		return closeWithErr(err)
	}
	if err := tempFile.Close(); err != nil {
		return 0, err
	}
	if err := os.Rename(tempPath, destPath); err != nil {
		_ = os.Remove(tempPath)
		return 0, err
	}
	if err := syncRescueDir(filepath.Dir(destPath)); err != nil {
		return 0, err
	}
	if info, err := os.Stat(destPath); err == nil {
		written = info.Size()
	}
	return written, nil
}

func copyWithLimit(dst io.Writer, src io.Reader, limit int64) (int64, error) {
	buf := make([]byte, 32<<10)
	var written int64
	for {
		n, err := src.Read(buf)
		if n > 0 {
			toWrite := n
			remaining := limit + 1 - written
			if remaining <= 0 {
				written++
				return written, nil
			}
			if int64(toWrite) > remaining {
				toWrite = int(remaining)
			}
			if _, writeErr := dst.Write(buf[:toWrite]); writeErr != nil {
				return written, writeErr
			}
			written += int64(toWrite)
			if int64(n) > int64(toWrite) {
				return written + 1, nil
			}
		}
		if err == io.EOF {
			return written, nil
		}
		if err != nil {
			return written, err
		}
	}
}

func syncRescueDir(path string) error {
	dir, err := os.Open(filepath.Clean(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
