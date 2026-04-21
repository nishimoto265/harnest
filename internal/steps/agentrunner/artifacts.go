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
	"strings"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/processenv"
)

const maxSuccessDiffBytes = 16 << 20

var truncatedDiffWarning = []byte("\n# auto-improve: diff truncated at 16777216 bytes\n")

func SuccessDiffBytes(ctx context.Context, worktreePath, baseSHA, errPrefix string) ([]byte, error) {
	tempDir, err := os.MkdirTemp("", "auto-improve-diff-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tempDir)
	diffPath := filepath.Join(tempDir, "diff.patch")
	if err := WriteSuccessDiff(ctx, worktreePath, baseSHA, errPrefix, diffPath); err != nil {
		return nil, err
	}
	return os.ReadFile(diffPath)
}

func WriteSuccessDiff(ctx context.Context, worktreePath, baseSHA, errPrefix, destPath string) error {
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}
	tempFile, err := os.CreateTemp(filepath.Dir(destPath), filepath.Base(destPath)+".tmp-*")
	if err != nil {
		return err
	}
	tempPath := tempFile.Name()
	writer := newCappedDiffWriter(tempFile, maxSuccessDiffBytes-int64(len(truncatedDiffWarning)))

	closeWithErr := func(err error) error {
		_ = tempFile.Close()
		_ = os.Remove(tempPath)
		return err
	}

	if err := streamGitCommandContext(
		ctx,
		worktreePath,
		errPrefix,
		writer,
		false,
		"diff",
		baseSHA,
		"--binary",
		"--",
		".",
		":(exclude)checklist-result.json",
	); err != nil {
		return closeWithErr(err)
	}
	if writer.truncated {
		if _, err := tempFile.Write(truncatedDiffWarning); err != nil {
			return closeWithErr(err)
		}
		if err := tempFile.Close(); err != nil {
			return closeWithErr(err)
		}
		return os.Rename(tempPath, destPath)
	}

	untrackedList, err := gitOutputBytesContext(ctx, worktreePath, errPrefix, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return closeWithErr(err)
	}

	for _, entry := range strings.Split(string(untrackedList), "\x00") {
		if entry == "" {
			continue
		}
		if entry == "checklist-result.json" {
			continue
		}
		if err := contracts.EnsureCleanRelativePath(entry); err != nil {
			return closeWithErr(err)
		}
		if err := streamGitNoIndexDiffContext(ctx, worktreePath, entry, errPrefix, writer); err != nil {
			return closeWithErr(err)
		}
		if writer.truncated {
			break
		}
	}
	if writer.truncated {
		if _, err := tempFile.Write(truncatedDiffWarning); err != nil {
			return closeWithErr(err)
		}
	}
	if err := tempFile.Close(); err != nil {
		return closeWithErr(err)
	}
	return os.Rename(tempPath, destPath)
}

func LoadChecklistArtifact(worktreePath, filename, errPrefix string, runID contracts.RunID, pass int, agent contracts.AgentID) (contracts.ChecklistResult, error) {
	sourcePath := filepath.Join(worktreePath, filename)
	if _, err := os.Stat(sourcePath); err != nil {
		if os.IsNotExist(err) {
			return contracts.ChecklistResult{}, fmt.Errorf("%s: missing checklist artifact: %s", errPrefix, sourcePath)
		}
		return contracts.ChecklistResult{}, err
	}
	checklist, err := internalio.ReadJSON[contracts.ChecklistResult](sourcePath)
	if err != nil {
		return contracts.ChecklistResult{}, err
	}
	if checklist.RunID != runID {
		return contracts.ChecklistResult{}, fmt.Errorf("%s: checklist run_id mismatch: got=%s want=%s", errPrefix, checklist.RunID, runID)
	}
	if checklist.Pass != pass {
		return contracts.ChecklistResult{}, fmt.Errorf("%s: checklist pass mismatch: got=%d want=%d", errPrefix, checklist.Pass, pass)
	}
	if checklist.Agent != agent {
		return contracts.ChecklistResult{}, fmt.Errorf("%s: checklist agent mismatch: got=%s want=%s", errPrefix, checklist.Agent, agent)
	}
	return checklist, nil
}

func gitOutputBytesContext(ctx context.Context, worktreePath, errPrefix string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", worktreePath}, args...)...)
	cmd.Env = processenv.Sanitize()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%s: git %s: %w: %s", errPrefix, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return output, nil
}

func gitOutputContext(ctx context.Context, mapFn func(string) string, worktreePath, errPrefix string, args ...string) (string, error) {
	output, err := gitOutputBytesContext(ctx, worktreePath, errPrefix, args...)
	if err != nil {
		return "", err
	}
	return mapFn(string(output)), nil
}

func gitNoIndexDiffContext(ctx context.Context, worktreePath, relativePath, errPrefix string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", "diff", "--binary", "--no-index", "--", "/dev/null", relativePath)
	cmd.Dir = worktreePath
	cmd.Env = processenv.Sanitize()
	output, err := cmd.CombinedOutput()
	if err == nil {
		return output, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return output, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return nil, fmt.Errorf("%s: git diff --binary --no-index -- /dev/null %s: %w: %s", errPrefix, relativePath, err, strings.TrimSpace(string(output)))
}

func streamGitCommandContext(ctx context.Context, worktreePath, errPrefix string, writer io.Writer, exitOneAllowed bool, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", worktreePath}, args...)...)
	cmd.Env = processenv.Sanitize()
	cmd.Stdout = writer
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var exitErr *exec.ExitError
		if exitOneAllowed && errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return nil
		}
		return fmt.Errorf("%s: git %s: %w: %s", errPrefix, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func streamGitNoIndexDiffContext(ctx context.Context, worktreePath, relativePath, errPrefix string, writer io.Writer) error {
	cmd := exec.CommandContext(ctx, "git", "diff", "--binary", "--no-index", "--", "/dev/null", relativePath)
	cmd.Dir = worktreePath
	cmd.Env = processenv.Sanitize()
	cmd.Stdout = writer
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return nil
		}
		return fmt.Errorf("%s: git diff --binary --no-index -- /dev/null %s: %w: %s", errPrefix, relativePath, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

type cappedDiffWriter struct {
	writer    io.Writer
	limit     int64
	written   int64
	truncated bool
}

func newCappedDiffWriter(writer io.Writer, limit int64) *cappedDiffWriter {
	return &cappedDiffWriter{writer: writer, limit: limit}
}

func (w *cappedDiffWriter) Write(p []byte) (int, error) {
	if w.limit <= 0 {
		w.truncated = true
		return len(p), nil
	}
	remaining := w.limit - w.written
	if remaining <= 0 {
		w.truncated = true
		return len(p), nil
	}
	toWrite := int64(len(p))
	if toWrite > remaining {
		toWrite = remaining
		w.truncated = true
	}
	if toWrite > 0 {
		n, err := w.writer.Write(p[:toWrite])
		w.written += int64(n)
		if err != nil {
			return n, err
		}
	}
	if int64(len(p)) > toWrite {
		w.truncated = true
	}
	return len(p), nil
}
