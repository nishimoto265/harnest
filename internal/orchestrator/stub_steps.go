package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/steps/step40_classify"
)

func defaultSteps() Steps {
	step20 := make(map[contracts.AgentID]Step, len(defaultAgents))
	step50 := make(map[contracts.AgentID]Step, len(defaultAgents))
	for _, agent := range defaultAgents {
		step20[agent] = stubImplementStep{}
		step50[agent] = stubImplementStep{}
	}
	return Steps{
		Step10:  stubStep10{},
		Step20:  step20,
		Step30:  stubMarkerStep{path: "30/done.marker"},
		Step40:  stubStep40{},
		Step50:  step50,
		Step60:  stubMarkerStep{path: "60/done.marker"},
		Step70:  stubStep70{},
		Archive: stubArchiveStep{},
	}
}

type stubStep10 struct{}

func (stubStep10) Run(ctx context.Context, run *StepRunContext) error {
	_ = ctx
	baseSHA := strings.Repeat("a", 40)
	worktrees := make([]contracts.WorktreeAllocation, 0, len(defaultAgents)*2)
	for pass := 1; pass <= 2; pass++ {
		for _, agent := range defaultAgents {
			path := filepath.Join(run.IO.WorktreeBase, fmt.Sprintf("%s-pass%d-%s", run.IO.RunID, pass, agent))
			if err := ensureDir(path); err != nil {
				return err
			}
			worktrees = append(worktrees, contracts.WorktreeAllocation{
				Agent:   agent,
				Pass:    pass,
				Path:    path,
				Branch:  fmt.Sprintf("auto-improve/%s/pass%d/%s", run.IO.RunID, pass, agent),
				BaseSHA: baseSHA,
				HeadSHA: baseSHA,
			})
		}
	}

	pkg := contracts.TaskPackage{
		SchemaVersion:           "1",
		RunID:                   run.IO.RunID,
		PR:                      run.PR,
		Title:                   fmt.Sprintf("PR #%d", run.PR),
		BaseSHA:                 baseSHA,
		BestBranch:              run.Config.Repo.BestBranch,
		ReconstructedTaskPrompt: fmt.Sprintf("stub task prompt for PR #%d", run.PR),
		Worktrees:               worktrees,
		CreatedAt:               time.Now().UTC(),
	}
	if err := internalio.WriteJSONAtomic(run.IO.TaskPackagePath(), pkg); err != nil {
		return err
	}
	if err := internalio.WriteAtomic(run.IO.BaseSHAPath(), []byte(baseSHA+"\n")); err != nil {
		return err
	}
	run.TaskPackage = &pkg
	return nil
}

type stubImplementStep struct{}

func (stubImplementStep) Run(ctx context.Context, run *StepRunContext) error {
	_ = ctx
	allocation, err := worktreeFor(run.TaskPackage, run.Pass, run.Agent)
	if err != nil {
		return err
	}
	prefix := manifestPrefix(run.Pass, run.Agent)
	if err := writeRunText(run.IO, filepath.Join(prefix, "diff.patch"), ""); err != nil {
		return err
	}
	if err := writeRunText(run.IO, filepath.Join(prefix, "session.jsonl"), ""); err != nil {
		return err
	}
	if err := writeRunText(run.IO, filepath.Join(prefix, "checklist-result.json"), "{}\n"); err != nil {
		return err
	}

	startedAt := time.Now().UTC()
	manifest := contracts.Manifest{
		Kind: contracts.ManifestKindSuccess,
		Value: contracts.ManifestSuccess{
			Kind:          contracts.ManifestKindSuccess,
			SchemaVersion: "1",
			RunID:         run.IO.RunID,
			Pass:          run.Pass,
			Agent:         run.Agent,
			BranchName:    allocation.Branch,
			HeadSHA:       allocation.HeadSHA,
			BaseSHA:       allocation.BaseSHA,
			DiffPath:      filepath.Join(prefix, "diff.patch"),
			SessionPath:   filepath.Join(prefix, "session.jsonl"),
			ChecklistPath: filepath.Join(prefix, "checklist-result.json"),
			PromptVersion: "stub-prompt-v1",
			StartedAt:     startedAt,
			FinishedAt:    startedAt,
		},
	}
	manifestPath, err := run.IO.ManifestPath(run.Pass, run.Agent)
	if err != nil {
		return err
	}
	return internalio.WriteJSONAtomic(manifestPath, manifest)
}

type stubMarkerStep struct {
	path string
}

func (s stubMarkerStep) Run(ctx context.Context, run *StepRunContext) error {
	_ = ctx
	path, err := run.IO.ResolveRunRelative(s.path)
	if err != nil {
		return err
	}
	return internalio.WriteAtomic(path, []byte("stub\n"))
}

type stubStep40 struct{}

func (stubStep40) Run(ctx context.Context, run *StepRunContext) error {
	candidates, err := step40_classify.Run(ctx, step40_classify.Config{
		IO:           run.IO,
		RegistryPath: run.IO.RulesRegistryPath(),
		TaskPackage:  run.TaskPackage,
		Now:          time.Now,
	})
	if err != nil {
		return err
	}
	run.Candidates = candidates
	return nil
}

type stubStep70 struct{}

func (stubStep70) Run(ctx context.Context, run *StepRunContext) error {
	_ = ctx
	decision := contracts.Decision{
		Action: contracts.DecisionActionNoop,
		Value: contracts.DecisionNoop{
			Action:        contracts.DecisionActionNoop,
			SchemaVersion: "1",
			RunID:         run.IO.RunID,
			Reason:        "stub_noop",
			DecidedAt:     time.Now().UTC(),
		},
	}
	path, err := run.IO.ResolveRunRelative("70/decision.json")
	if err != nil {
		return err
	}
	if err := internalio.WriteJSONAtomic(path, decision); err != nil {
		return err
	}
	run.Decision = &decision
	return run.IntentionFile.Delete()
}

type stubArchiveStep struct{}

func (stubArchiveStep) Run(ctx context.Context, run *StepRunContext) error {
	_ = ctx
	_ = run
	return nil
}

func worktreeFor(pkg *contracts.TaskPackage, pass int, agent contracts.AgentID) (contracts.WorktreeAllocation, error) {
	if pkg == nil {
		return contracts.WorktreeAllocation{}, errors.New("orchestrator: task package is required")
	}
	for _, worktree := range pkg.Worktrees {
		if worktree.Pass == pass && worktree.Agent == agent {
			return worktree, nil
		}
	}
	return contracts.WorktreeAllocation{}, fmt.Errorf("orchestrator: missing worktree allocation: pass=%d agent=%s", pass, agent)
}

func manifestPrefix(pass int, agent contracts.AgentID) string {
	if pass == 2 {
		return filepath.Join("50-pass2", string(agent))
	}
	return filepath.Join("20-pass1", string(agent))
}

func writeRunText(runCtx internalio.RunContext, rel string, content string) error {
	path, err := runCtx.ResolveRunRelative(rel)
	if err != nil {
		return err
	}
	return internalio.WriteAtomic(path, []byte(content))
}

func ensureDir(path string) error {
	return os.MkdirAll(path, 0o755)
}
