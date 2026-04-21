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
	"github.com/nishimoto265/auto-improve/internal/processenv"
)

const maxSuccessDiffBytes = 16 << 20
const maxChecklistArtifactBytes = 10 << 20

var (
	ErrSuccessDiffOverflow   = errors.New("agentrunner: success diff exceeded 16MB limit")
	ErrArtifactNotRegular    = errors.New("agentrunner: artifact path is not a regular file")
	ErrArtifactCollectionTTL = errors.New("agentrunner: artifact collection deadline exceeded")
	ErrArtifactTooLarge      = errors.New("agentrunner: artifact exceeds size limit")
)

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
	writer := newCappedDiffWriter(tempFile, maxSuccessDiffBytes)

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
		"--no-ext-diff",
		"--no-textconv",
		"--",
		".",
		":(exclude)checklist-result.json",
	); err != nil {
		return closeWithErr(err)
	}
	if writer.truncated {
		return closeWithErr(fmt.Errorf("%w: worktree=%s", ErrSuccessDiffOverflow, worktreePath))
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
		collectPath := filepath.Join(worktreePath, filepath.FromSlash(entry))
		diffable, err := diffableArtifactSource(collectPath)
		if err != nil {
			return closeWithErr(err)
		}
		if !diffable {
			continue
		}
		if err := streamGitNoIndexDiffContext(ctx, worktreePath, entry, errPrefix, writer); err != nil {
			return closeWithErr(err)
		}
		if writer.truncated {
			return closeWithErr(fmt.Errorf("%w: worktree=%s entry=%s", ErrSuccessDiffOverflow, worktreePath, entry))
		}
	}
	if err := tempFile.Close(); err != nil {
		return closeWithErr(err)
	}
	return os.Rename(tempPath, destPath)
}

func LoadChecklistArtifact(worktreePath, filename, errPrefix string, runID contracts.RunID, pass int, agent contracts.AgentID) (contracts.ChecklistResult, error) {
	return LoadChecklistArtifactContext(context.Background(), worktreePath, filename, errPrefix, runID, pass, agent)
}

func LoadChecklistArtifactContext(ctx context.Context, worktreePath, filename, errPrefix string, runID contracts.RunID, pass int, agent contracts.AgentID) (contracts.ChecklistResult, error) {
	if err := artifactCollectionDeadline(ctx); err != nil {
		return contracts.ChecklistResult{}, err
	}
	sourcePath := filepath.Join(worktreePath, filename)
	if err := ensureArtifactSourceRegular(sourcePath); err != nil {
		if os.IsNotExist(err) {
			return contracts.ChecklistResult{}, fmt.Errorf("%s: missing checklist artifact: %s", errPrefix, sourcePath)
		}
		return contracts.ChecklistResult{}, err
	}
	if err := artifactCollectionDeadline(ctx); err != nil {
		return contracts.ChecklistResult{}, err
	}
	checklist, err := loadChecklistArtifactFileContext(ctx, sourcePath)
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
	cmd := exec.CommandContext(ctx, "git", "diff", "--binary", "--no-ext-diff", "--no-textconv", "--no-index", "--", "/dev/null", relativePath)
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
	cmd := exec.CommandContext(ctx, "git", "diff", "--binary", "--no-ext-diff", "--no-textconv", "--no-index", "--", "/dev/null", relativePath)
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

func ensureArtifactSourceRegular(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s", ErrArtifactNotRegular, path)
	}
	if info.Mode().IsRegular() {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrArtifactNotRegular, path)
}

func diffableArtifactSource(path string) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, nil
	}
	return info.Mode().IsRegular(), nil
}

func loadChecklistArtifactFileContext(ctx context.Context, path string) (contracts.ChecklistResult, error) {
	if err := artifactCollectionDeadline(ctx); err != nil {
		return contracts.ChecklistResult{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return contracts.ChecklistResult{}, err
	}
	if info.Size() > maxChecklistArtifactBytes {
		return contracts.ChecklistResult{}, fmt.Errorf("%w: path=%s size=%d limit=%d", ErrArtifactTooLarge, path, info.Size(), maxChecklistArtifactBytes)
	}
	file, err := os.Open(path)
	if err != nil {
		return contracts.ChecklistResult{}, err
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(&contextReader{ctx: ctx, reader: file}, maxChecklistArtifactBytes+1))
	if err != nil {
		return contracts.ChecklistResult{}, err
	}
	if int64(len(data)) > maxChecklistArtifactBytes {
		return contracts.ChecklistResult{}, fmt.Errorf("%w: path=%s size=%d limit=%d", ErrArtifactTooLarge, path, len(data), maxChecklistArtifactBytes)
	}

	var checklist contracts.ChecklistResult
	if err := contracts.DecodeStrictJSON(data, &checklist); err != nil {
		return contracts.ChecklistResult{}, err
	}
	return checklist, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := artifactCollectionDeadline(r.ctx); err != nil {
		return 0, err
	}
	n, err := r.reader.Read(p)
	if err == nil {
		if ctxErr := artifactCollectionDeadline(r.ctx); ctxErr != nil {
			return n, ctxErr
		}
	}
	return n, err
}

func artifactCollectionDeadline(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("%w: %w", ErrArtifactCollectionTTL, err)
		}
		return err
	}
	return nil
}
