package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/processenv"
)

type IntentionStore struct {
	runCtx internalio.RunContext
}

func NewIntentionStore(runCtx internalio.RunContext) *IntentionStore {
	return &IntentionStore{runCtx: runCtx}
}

func (s *IntentionStore) Path() (string, error) {
	return s.runCtx.ResolveRunRelative("70/intention.json")
}

func (s *IntentionStore) Load() (*contracts.IntentionRecord, error) {
	path, err := s.Path()
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	record, err := internalio.ReadJSON[contracts.IntentionRecord](path)
	if err != nil {
		return nil, err
	}
	if record.RunID != s.runCtx.RunID {
		return nil, errors.New("orchestrator: intention run_id mismatch")
	}
	return &record, nil
}

func (s *IntentionStore) Save(record contracts.IntentionRecord) error {
	path, err := s.Path()
	if err != nil {
		return err
	}
	if record.RunID != s.runCtx.RunID {
		return errors.New("orchestrator: intention run_id mismatch")
	}
	return internalio.WriteJSONAtomic(path, record)
}

func (s *IntentionStore) Delete() error {
	path, err := s.Path()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s *IntentionStore) Transition(stage contracts.IntentionStage, mutate func(*contracts.IntentionRecord) error) error {
	record, err := s.Load()
	if err != nil {
		return err
	}
	if record == nil {
		return errors.New("orchestrator: intention record does not exist")
	}
	clone := *record
	if mutate != nil {
		if err := mutate(&clone); err != nil {
			return err
		}
	}
	clone.Stage = stage
	return s.Save(clone)
}

func cleanupWorktrees(runCtx internalio.RunContext, pkg *contracts.TaskPackage) error {
	return cleanupWorktreesWithGit(runCtx, pkg, "")
}

func cleanupWorktreesWithGit(runCtx internalio.RunContext, pkg *contracts.TaskPackage, repoRoot string) error {
	if pkg == nil {
		return nil
	}
	for _, worktree := range pkg.Worktrees {
		if err := runCtx.ValidateWorktreeAllocation(worktree); err != nil {
			return err
		}
		path := filepath.Clean(worktree.Path)
		if _, err := removeRegisteredGitWorktree(repoRoot, path); err != nil {
			return err
		}
		if _, err := os.Lstat(path); err == nil {
			if err := os.RemoveAll(path); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func removeRegisteredGitWorktree(repoRoot, path string) (bool, error) {
	if strings.TrimSpace(repoRoot) == "" {
		return false, nil
	}
	ctx := context.Background()
	registered, err := gitWorktreeRegistered(ctx, repoRoot, path)
	if err != nil || !registered {
		return false, nil
	}
	if err := runTrustedGit(ctx, repoRoot, "worktree", "remove", "--force", path); err != nil {
		if pruneErr := pruneMissingGitWorktree(ctx, repoRoot, path); pruneErr == nil {
			return true, nil
		}
		return true, err
	}
	return true, nil
}

func pruneMissingGitWorktree(ctx context.Context, repoRoot, path string) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("orchestrator: registered git worktree still exists after failed remove: %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := runTrustedGit(ctx, repoRoot, "worktree", "prune"); err != nil {
		return err
	}
	registered, err := gitWorktreeRegistered(ctx, repoRoot, path)
	if err != nil {
		return err
	}
	if registered {
		return fmt.Errorf("orchestrator: git worktree prune left stale registration: %s", path)
	}
	return nil
}

func gitWorktreeRegistered(ctx context.Context, repoRoot, path string) (bool, error) {
	out, err := trustedGitOutput(ctx, repoRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return false, err
	}
	want, err := contracts.CanonicalizePathForUniqueness(path)
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, "worktree ") {
			continue
		}
		have, err := contracts.CanonicalizePathForUniqueness(filepath.Clean(strings.TrimSpace(strings.TrimPrefix(line, "worktree "))))
		if err != nil {
			return false, err
		}
		if have == want {
			return true, nil
		}
	}
	return false, nil
}

func runTrustedGit(ctx context.Context, repoRoot string, args ...string) error {
	_, err := trustedGitOutput(ctx, repoRoot, args...)
	return err
}

func trustedGitOutput(ctx context.Context, repoRoot string, args ...string) ([]byte, error) {
	cmdArgs := append([]string{"-C", repoRoot}, args...)
	cmd, err := processenv.TrustedCommandContext(ctx, "git", cmdArgs...)
	if err != nil {
		return nil, err
	}
	cmd.Env = processenv.GitLocalEnv()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		msgs := make([]string, 0, 2)
		if out := strings.TrimSpace(stdout.String()); out != "" {
			msgs = append(msgs, "stdout="+out)
		}
		if out := strings.TrimSpace(stderr.String()); out != "" {
			msgs = append(msgs, "stderr="+out)
		}
		if len(msgs) == 0 {
			return nil, fmt.Errorf("orchestrator: git %s: %w", strings.Join(cmdArgs, " "), err)
		}
		return nil, fmt.Errorf("orchestrator: git %s: %w: %s", strings.Join(cmdArgs, " "), err, strings.Join(msgs, "; "))
	}
	return stdout.Bytes(), nil
}
