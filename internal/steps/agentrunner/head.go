package agentrunner

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/nishimoto265/harnest/internal/contracts"
	"github.com/nishimoto265/harnest/internal/processenv"
)

func ValidateSuccessHead(ctx context.Context, allocation contracts.WorktreeAllocation, headSHA, errPrefix string) error {
	currentBranch, err := gitOutputTrimmed(ctx, allocation.Path, errPrefix, "branch", "--show-current")
	if err != nil {
		return err
	}
	if currentBranch != allocation.Branch {
		return fmt.Errorf("%s: current branch mismatch: got=%s want=%s", errPrefix, currentBranch, allocation.Branch)
	}
	branchHead, err := gitOutputTrimmed(ctx, allocation.Path, errPrefix, "rev-parse", "refs/heads/"+allocation.Branch)
	if err != nil {
		return err
	}
	if branchHead != headSHA {
		return fmt.Errorf("%s: branch ref mismatch: head=%s branch=%s ref=%s", errPrefix, headSHA, allocation.Branch, branchHead)
	}
	if err := runGitCommand(ctx, allocation.Path, errPrefix, "merge-base", "--is-ancestor", allocation.BaseSHA, headSHA); err != nil {
		return fmt.Errorf("%s: base sha is not an ancestor of head: %w", errPrefix, err)
	}
	return nil
}

func gitOutputTrimmed(ctx context.Context, worktreePath, errPrefix string, args ...string) (string, error) {
	output, err := gitOutput(ctx, worktreePath, errPrefix, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func gitOutput(ctx context.Context, worktreePath, errPrefix string, args ...string) ([]byte, error) {
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

func runGitCommand(ctx context.Context, worktreePath, errPrefix string, args ...string) error {
	cmd, err := processenv.TrustedCommandContext(ctx, "git", append([]string{"-C", worktreePath}, args...)...)
	if err != nil {
		return fmt.Errorf("%s: resolve git: %w", errPrefix, err)
	}
	cmd.Env = processenv.GitLocalEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%s: git %s: %w: %s", errPrefix, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
