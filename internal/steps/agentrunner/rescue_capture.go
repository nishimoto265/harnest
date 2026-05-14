package agentrunner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	internalio "github.com/nishimoto265/auto-improve/internal/io"
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

	cmdCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmd, err := processenv.TrustedCommandContext(cmdCtx, "git", append([]string{"-C", worktreePath}, args...)...)
	if err != nil {
		return 0, fmt.Errorf("%s: resolve git: %w", errPrefix, err)
	}
	cmd.Env = processenv.GitLocalEnv()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return 0, err
	}

	var output bytes.Buffer
	written, copyErr := copyWithLimit(&output, stdout, limit)
	if copyErr != nil {
		cancel()
		_ = cmd.Wait()
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, copyErr
	}
	if written > limit {
		cancel()
	}
	waitErr := cmd.Wait()
	if written > limit {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, fmt.Errorf("%w: git %s bytes=%d limit=%d", ErrRescueDiffOverLimit, strings.Join(args, " "), written, limit)
	}
	if waitErr != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, fmt.Errorf("%s: git %s: %w: %s", errPrefix, strings.Join(args, " "), waitErr, strings.TrimSpace(stderr.String()))
	}
	if err := internalio.WriteAtomic(destPath, output.Bytes()); err != nil {
		return 0, err
	}
	return int64(output.Len()), nil
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
