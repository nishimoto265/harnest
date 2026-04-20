package io

import (
	crand "crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/validation"
)

type RunContext struct {
	RunID        contracts.RunID
	RunsBase     string
	WorktreeBase string
	worktrees    map[int]map[contracts.AgentID]contracts.WorktreeAllocation
}

// NewRunID returns a fresh run identifier with the canonical
// YYYY-MM-DD-PR<num>-<hex7> format.
func NewRunID(pr int) contracts.RunID {
	var entropy [4]byte
	if _, err := crand.Read(entropy[:]); err != nil {
		fallback := atomicNowFunc().UnixNano() ^ int64(os.Getpid())
		copy(entropy[:], []byte(fmt.Sprintf("%08x", fallback)))
	}
	suffix := hex.EncodeToString(entropy[:])[:7]
	return contracts.RunID(fmt.Sprintf("%s-PR%d-%s", atomicNowFunc().Format("2006-01-02"), pr, suffix))
}

func NewRunContext(runID contracts.RunID, runsBase, worktreeBase string) (RunContext, error) {
	if err := validation.Instance().Var(runID, "required,run_id_fmt"); err != nil {
		return RunContext{}, err
	}
	if err := contracts.EnsureCleanAbsolutePath(runsBase); err != nil {
		return RunContext{}, err
	}
	if err := contracts.EnsureCleanAbsolutePath(worktreeBase); err != nil {
		return RunContext{}, err
	}
	return RunContext{
		RunID:        runID,
		RunsBase:     runsBase,
		WorktreeBase: worktreeBase,
	}, nil
}

func RunContextFromTaskPackage(pkg contracts.TaskPackage, runsBase, worktreeBase string) (RunContext, error) {
	if err := pkg.Validate(); err != nil {
		return RunContext{}, err
	}
	ctx, err := NewRunContext(pkg.RunID, runsBase, worktreeBase)
	if err != nil {
		return RunContext{}, err
	}
	ctx.worktrees = make(map[int]map[contracts.AgentID]contracts.WorktreeAllocation, 2)
	for _, worktree := range pkg.Worktrees {
		if _, ok := ctx.worktrees[worktree.Pass]; !ok {
			ctx.worktrees[worktree.Pass] = make(map[contracts.AgentID]contracts.WorktreeAllocation)
		}
		ctx.worktrees[worktree.Pass][worktree.Agent] = worktree
	}
	return ctx, nil
}

func (ctx RunContext) RunDir() string {
	return filepath.Join(ctx.RunsBase, string(ctx.RunID))
}

func (ctx RunContext) ResolveRunRelative(path string) (string, error) {
	if err := contracts.EnsureCleanRelativePath(path); err != nil {
		return "", err
	}
	return filepath.Join(ctx.RunDir(), path), nil
}

func (ctx RunContext) TaskPackagePath() string {
	return filepath.Join(ctx.RunDir(), "task-package.json")
}

func (ctx RunContext) BaseSHAPath() string {
	return filepath.Join(ctx.RunDir(), "base.sha")
}

func (ctx RunContext) ProcessedPath() string {
	return filepath.Join(ctx.RunsBase, "processed.jsonl")
}

func (ctx RunContext) RulesRegistryPath() string {
	return filepath.Join(ctx.RunsBase, "rules-registry.jsonl")
}

func (ctx RunContext) RulesIdempotencyIndexPath() string {
	return filepath.Join(ctx.RunsBase, "rules-idempotency-index.jsonl")
}

func (ctx RunContext) PromotionLockPath() string {
	return filepath.Join(ctx.RunsBase, "promotion.lock")
}

func (ctx RunContext) Pass1WorktreePath(agent contracts.AgentID) (string, error) {
	return ctx.worktreePath(1, agent)
}

func (ctx RunContext) Pass2WorktreePath(agent contracts.AgentID) (string, error) {
	return ctx.worktreePath(2, agent)
}

func (ctx RunContext) ManifestPath(pass int, agent contracts.AgentID) (string, error) {
	dir, err := ctx.agentDir(pass, agent)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "manifest.json"), nil
}

func (ctx RunContext) worktreePath(pass int, agent contracts.AgentID) (string, error) {
	if err := validatePass(pass); err != nil {
		return "", err
	}
	if err := validation.Instance().Var(agent, "required,agent_id_fmt"); err != nil {
		return "", err
	}
	if ctx.worktrees == nil {
		return "", ErrWorktreePathUnavailable
	}
	if allocation, ok := ctx.worktrees[pass][agent]; ok {
		return allocation.Path, nil
	}
	return "", ErrWorktreePathUnavailable
}

func (ctx RunContext) passDir(pass int) (string, error) {
	switch pass {
	case 1:
		return filepath.Join(ctx.RunDir(), "20-pass1"), nil
	case 2:
		return filepath.Join(ctx.RunDir(), "50-pass2"), nil
	default:
		return "", ErrInvalidPass
	}
}

func (ctx RunContext) agentDir(pass int, agent contracts.AgentID) (string, error) {
	if err := validatePass(pass); err != nil {
		return "", err
	}
	if err := validation.Instance().Var(agent, "required,agent_id_fmt"); err != nil {
		return "", err
	}
	passDir, _ := ctx.passDir(pass)
	return filepath.Join(passDir, string(agent)), nil
}

func LoadFinalizedManifest(ctx RunContext, pass int, agent contracts.AgentID) (*contracts.Manifest, error) {
	path, err := ctx.ManifestPath(pass, agent)
	if err != nil {
		return nil, err
	}
	manifest, err := ReadJSON[contracts.Manifest](path)
	if err != nil {
		return nil, err
	}
	return &manifest, nil
}

func LoadScorableManifest(ctx RunContext, pass int, agent contracts.AgentID) (*contracts.ManifestSuccess, error) {
	manifest, err := LoadFinalizedManifest(ctx, pass, agent)
	if err != nil {
		return nil, err
	}
	switch v := manifest.Value.(type) {
	case contracts.ManifestSuccess:
		return &v, nil
	case *contracts.ManifestSuccess:
		if v == nil {
			return nil, ErrNotScorable
		}
		cloned := *v
		return &cloned, nil
	default:
		return nil, ErrNotScorable
	}
}

func validatePass(pass int) error {
	if pass != 1 && pass != 2 {
		return fmt.Errorf("%w: pass=%d", ErrInvalidPass, pass)
	}
	return nil
}
