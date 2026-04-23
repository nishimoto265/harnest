package orchestrator

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
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
	if pkg == nil {
		return nil
	}
	for _, worktree := range pkg.Worktrees {
		if err := runCtx.ValidateWorktreeAllocation(worktree); err != nil {
			return err
		}
		if err := os.RemoveAll(filepath.Clean(worktree.Path)); err != nil {
			return err
		}
	}
	return nil
}
