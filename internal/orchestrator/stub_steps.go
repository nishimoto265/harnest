package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nishimoto265/auto-improve/internal/archive"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/steps/step70_decide"
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
		Step70:  realStep70{},
		Archive: realArchiveStep{},
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
	_ = ctx
	candidates := contracts.Candidates{
		SchemaVersion:  "1",
		RunID:          run.IO.RunID,
		Candidates:     []contracts.Candidate{},
		CandidatesHash: contracts.CanonicalCandidatesHash(nil),
		CreatedAt:      time.Now().UTC(),
	}
	path, err := run.IO.ResolveRunRelative("40/candidates.json")
	if err != nil {
		return err
	}
	if err := internalio.WriteJSONAtomic(path, candidates); err != nil {
		return err
	}
	run.Candidates = &candidates
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

// realStep70 is the production wiring for step70, delegating to
// internal/steps/step70_decide. The resolver defaults to NoopResolver so that
// pipeline runs without a promotion target emit a noop decision (matches the
// prior stubStep70 behaviour for tests and empty-candidate flows).
type realStep70 struct{}

func (realStep70) Run(ctx context.Context, run *StepRunContext) error {
	if run.TaskPackage == nil {
		return errors.New("orchestrator: step70 requires task_package")
	}
	if run.Candidates == nil {
		return errors.New("orchestrator: step70 requires candidates")
	}
	if run.Config == nil {
		return errors.New("orchestrator: step70 requires config")
	}
	repoRoot, err := run.Config.RepoRoot()
	if err != nil {
		return err
	}
	store := run.IntentionFile
	deps := step70_decide.Deps{
		Git: step70_decide.RealGitOps{
			RepoDir: repoRoot,
		},
		Resolver: step70_decide.FilesystemResolver{
			RepoDir: repoRoot,
		},
	}
	if err := step70_decide.Run(ctx, run.PR, run.IO, run.TaskPackage, run.Candidates, store, deps); err != nil {
		return err
	}
	decisionPath, err := run.IO.ResolveRunRelative("70/decision.json")
	if err != nil {
		return err
	}
	if fileExists(decisionPath) {
		decision, err := internalio.ReadJSON[contracts.Decision](decisionPath)
		if err != nil {
			return err
		}
		run.Decision = &decision
	}
	return nil
}

// realArchiveStep performs the lightweight post-finalize worktree cleanup that
// step70 delegated to the orchestrator prior to Phase 1-F. The heavy sunset
// transitions are run by the sunset_tick / `auto-improve sunset` entry points
// (both delegating to internal/archive.RunSunsetWithLock).
type realArchiveStep struct{}

func (realArchiveStep) Run(ctx context.Context, run *StepRunContext) error {
	_ = ctx
	// Opportunistically invoke archive with an empty transition list when a
	// sunset_run_id is available; otherwise this is a no-op because the
	// orchestrator only performs worktree cleanup after step70 finalizes. The
	// empty-Transitions call still emits registry-size telemetry for operators.
	if run == nil {
		return nil
	}
	if run.IO.RunsBase == "" {
		return nil
	}
	opts := archive.Opts{
		RunsBase:    run.IO.RunsBase,
		SunsetRunID: archiveRunIDFromRun(run.IO.RunID),
	}
	if run.Config != nil {
		opts.RegistryHighAt = 1500
		opts.RegistryCritAt = 2000
	}
	if _, err := archive.RunSunsetWithLock(ctx, opts); err != nil {
		return err
	}
	return nil
}

// archiveRunIDFromRun derives a short sunset-run fingerprint from a pipeline
// run_id. This is a wiring detail — the full sunset scheduler (not in this
// phase) provides its own sha256(date || fingerprint).
func archiveRunIDFromRun(runID contracts.RunID) string {
	return "orchestrator:" + string(runID)
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
