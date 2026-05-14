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

	"github.com/nishimoto265/harnest/internal/contracts"
	"github.com/nishimoto265/harnest/internal/policyartifact"
	"github.com/nishimoto265/harnest/internal/processenv"
)

const maxSuccessDiffBytes = 16 << 20
const maxChecklistArtifactBytes = 10 << 20

var (
	ErrSuccessDiffOverflow      = errors.New("agentrunner: success diff exceeded 16MB limit")
	ErrArtifactNotRegular       = errors.New("agentrunner: artifact path is not a regular file")
	ErrArtifactCollectionTTL    = errors.New("agentrunner: artifact collection deadline exceeded")
	ErrArtifactTooLarge         = errors.New("agentrunner: artifact exceeds size limit")
	ErrMissingChecklistArtifact = errors.New("agentrunner: missing checklist artifact")
)

var snapshotOpenValidatedRegularFile = OpenValidatedRegularFile
var snapshotCopyOpenFile = copySnapshotOpenFile
var writeSuccessDiffFileSync = func(f *os.File) error {
	return f.Sync()
}
var writeSuccessDiffRename = os.Rename
var writeSuccessDiffSyncDir = syncRescueDir

func syncRescueDir(path string) error {
	dir, err := os.Open(filepath.Clean(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func SuccessDiffBytes(ctx context.Context, worktreePath, baseSHA, errPrefix string) ([]byte, error) {
	tempDir, err := os.MkdirTemp("", "harnest-diff-*")
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

func successDiffShouldSkipUntracked(path string) bool {
	return policyartifact.Is(path)
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

	diffArgs := []string{"diff", baseSHA, "--binary", "--no-ext-diff", "--no-textconv", "--", "."}
	diffArgs = append(diffArgs, policyartifact.GitExcludePathspecs()...)
	if err := streamGitCommandContext(ctx, worktreePath, errPrefix, writer, false, diffArgs...); err != nil {
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
		if successDiffShouldSkipUntracked(entry) {
			continue
		}
		if err := contracts.EnsureCleanRelativePath(entry); err != nil {
			return closeWithErr(err)
		}
		collectPath := filepath.Join(worktreePath, filepath.FromSlash(entry))
		snapshotRoot, diffable, err := snapshotDiffableArtifact(ctx, collectPath, filepath.Dir(destPath), entry)
		if err != nil {
			return closeWithErr(err)
		}
		if !diffable {
			continue
		}
		if err := streamGitNoIndexDiffContext(ctx, snapshotRoot, entry, errPrefix, writer); err != nil {
			_ = os.RemoveAll(snapshotRoot)
			return closeWithErr(err)
		}
		_ = os.RemoveAll(snapshotRoot)
		if writer.truncated {
			return closeWithErr(fmt.Errorf("%w: worktree=%s entry=%s", ErrSuccessDiffOverflow, worktreePath, entry))
		}
	}
	if err := writeSuccessDiffFileSync(tempFile); err != nil {
		return closeWithErr(err)
	}
	if err := tempFile.Close(); err != nil {
		return closeWithErr(err)
	}
	if err := writeSuccessDiffRename(tempPath, destPath); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	return writeSuccessDiffSyncDir(filepath.Dir(destPath))
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
			return contracts.ChecklistResult{}, fmt.Errorf("%w: %s: %s", ErrMissingChecklistArtifact, errPrefix, sourcePath)
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
	cmd, err := processenv.TrustedCommandContext(ctx, "git", append([]string{"-C", worktreePath}, args...)...)
	if err != nil {
		return nil, fmt.Errorf("%s: resolve git: %w", errPrefix, err)
	}
	cmd.Env = processenv.GitLocalEnv()
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

func streamGitCommandContext(ctx context.Context, worktreePath, errPrefix string, writer io.Writer, exitOneAllowed bool, args ...string) error {
	cmd, err := processenv.TrustedCommandContext(ctx, "git", append([]string{"-C", worktreePath}, args...)...)
	if err != nil {
		return fmt.Errorf("%s: resolve git: %w", errPrefix, err)
	}
	cmd.Env = processenv.GitLocalEnv()
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
	cmd, err := processenv.TrustedCommandContext(ctx, "git", "diff", "--binary", "--no-ext-diff", "--no-textconv", "--no-index", "--", "/dev/null", relativePath)
	if err != nil {
		return fmt.Errorf("%s: resolve git: %w", errPrefix, err)
	}
	cmd.Dir = worktreePath
	cmd.Env = processenv.GitLocalEnv()
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
	_, _, _, err := validatedRegularFileIdentity(path)
	return err
}

func loadChecklistArtifactFileContext(ctx context.Context, path string) (contracts.ChecklistResult, error) {
	if err := artifactCollectionDeadline(ctx); err != nil {
		return contracts.ChecklistResult{}, err
	}
	file, _, size, err := OpenValidatedRegularFile(path)
	if err != nil {
		return contracts.ChecklistResult{}, err
	}
	defer file.Close()
	if size > maxChecklistArtifactBytes {
		return contracts.ChecklistResult{}, fmt.Errorf("%w: path=%s size=%d limit=%d", ErrArtifactTooLarge, path, size, maxChecklistArtifactBytes)
	}

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

func snapshotDiffableArtifact(ctx context.Context, sourcePath, tempDir, relativePath string) (string, bool, error) {
	file, perm, size, err := snapshotOpenValidatedRegularFile(sourcePath)
	if err != nil {
		if errors.Is(err, ErrArtifactNotRegular) {
			return "", false, nil
		}
		return "", false, err
	}
	defer file.Close()
	if size > maxSuccessDiffBytes {
		return "", false, fmt.Errorf("%w: path=%s size=%d limit=%d", ErrSuccessDiffOverflow, sourcePath, size, maxSuccessDiffBytes)
	}

	snapshotRoot, err := os.MkdirTemp(tempDir, "success-diff-snapshot-*")
	if err != nil {
		return "", false, err
	}
	snapshotPath := filepath.Join(snapshotRoot, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(snapshotPath), 0o755); err != nil {
		_ = os.RemoveAll(snapshotRoot)
		return "", false, err
	}
	tempFile, err := os.Create(snapshotPath)
	if err != nil {
		_ = os.RemoveAll(snapshotRoot)
		return "", false, err
	}
	cleanup := func(err error) (string, bool, error) {
		_ = tempFile.Close()
		_ = os.RemoveAll(snapshotRoot)
		return "", false, err
	}
	if err := tempFile.Chmod(perm); err != nil {
		return cleanup(err)
	}
	written, err := snapshotCopyOpenFile(ctx, tempFile, file, maxSuccessDiffBytes)
	if err != nil {
		return cleanup(err)
	}
	if written > maxSuccessDiffBytes {
		return cleanup(fmt.Errorf("%w: path=%s size=%d limit=%d", ErrSuccessDiffOverflow, sourcePath, written, maxSuccessDiffBytes))
	}
	if err := tempFile.Close(); err != nil {
		return cleanup(err)
	}
	return snapshotRoot, true, nil
}

func copySnapshotOpenFile(ctx context.Context, dst io.Writer, src *os.File, sizeLimit int64) (int64, error) {
	return io.CopyBuffer(
		dst,
		io.LimitReader(&contextReader{ctx: ctx, reader: src}, sizeLimit+1),
		make([]byte, 32<<10),
	)
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
