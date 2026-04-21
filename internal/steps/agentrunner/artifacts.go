package agentrunner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
)

func SuccessDiffBytes(ctx context.Context, worktreePath, baseSHA, errPrefix string) ([]byte, error) {
	tracked, err := gitOutputBytesContext(
		ctx,
		worktreePath,
		errPrefix,
		"diff",
		baseSHA,
		"--binary",
		"--",
		".",
		":(exclude)checklist-result.json",
	)
	if err != nil {
		return nil, err
	}

	untrackedList, err := gitOutputBytesContext(ctx, worktreePath, errPrefix, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}

	var combined bytes.Buffer
	if _, err := combined.Write(tracked); err != nil {
		return nil, err
	}
	for _, entry := range strings.Split(string(untrackedList), "\x00") {
		if entry == "" {
			continue
		}
		if entry == "checklist-result.json" {
			continue
		}
		if err := contracts.EnsureCleanRelativePath(entry); err != nil {
			return nil, err
		}
		diff, err := gitNoIndexDiffContext(ctx, worktreePath, entry, errPrefix)
		if err != nil {
			return nil, err
		}
		if _, err := combined.Write(diff); err != nil {
			return nil, err
		}
	}
	return combined.Bytes(), nil
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
