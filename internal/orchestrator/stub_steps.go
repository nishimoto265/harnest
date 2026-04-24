package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/contracts/stepio"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/judges"
	"github.com/nishimoto265/auto-improve/internal/policyrepo"
	step10restorebase "github.com/nishimoto265/auto-improve/internal/steps/step10_restorebase"
	"github.com/nishimoto265/auto-improve/internal/steps/step20_implement"
	"github.com/nishimoto265/auto-improve/internal/steps/step30_score"
	"github.com/nishimoto265/auto-improve/internal/steps/step40_classify"
	"github.com/nishimoto265/auto-improve/internal/steps/step50_implement"
	"github.com/nishimoto265/auto-improve/internal/steps/step60_scorepairwise"
	"github.com/nishimoto265/auto-improve/internal/steps/step70_decide"
)

func defaultSteps(cfg *config.Config, decoders ContractDecoders) Steps {
	step20 := make(map[contracts.AgentID]Step, len(defaultAgents))
	step50 := make(map[contracts.AgentID]Step, len(defaultAgents))
	implStep := step20_implement.NewStep(cfg)
	step50Impl := step50_implement.NewStep(cfg)
	for _, agent := range defaultAgents {
		step20[agent] = step20Adapter{impl: implStep}
		step50[agent] = step50Adapter{impl: step50Impl}
	}
	return Steps{
		Step10:  step10Adapter{runner: step10restorebase.NewRunner(), decode: decoders.Step10},
		Step20:  step20,
		Step30:  newStep30ScoreAdapter(step30_score.New(step30_score.WithPanelProvider(step30_score.ConfigPanelProvider(cfg))), decoders.Step30),
		Step40:  stubStep40{decode: decoders.Step40},
		Step50:  step50,
		Step60:  step60Step{cfg: cfg, decode: decoders.Step60},
		Step70:  realStep70{cfg: cfg, decode: decoders.Step70},
		Archive: realArchiveStep{},
	}
}

// step30ScoreAdapter bridges step30_score.Step to orchestrator.Step without
// pulling the orchestrator package into step30_score (one-way import graph).
type step30ScoreAdapter struct {
	step   *step30_score.Step
	decode func([]byte, any) (any, error)
}

func newStep30ScoreAdapter(step *step30_score.Step, decode func([]byte, any) (any, error)) step30ScoreAdapter {
	return step30ScoreAdapter{step: step, decode: decode}
}

func (a step30ScoreAdapter) Run(ctx context.Context, run *StepRunContext) error {
	if err := a.step.Run(ctx, step30_score.Request{
		RunContext:  run.IO,
		TaskPackage: run.TaskPackage,
	}); err != nil {
		return err
	}
	if a.decode == nil {
		return nil
	}
	scorableAgents, err := scorableAgentsForPass(run.IO, run.TaskPackage, 1)
	if err != nil {
		return err
	}
	req := stepio.Step30Request{
		TaskPackage:    *run.TaskPackage,
		ScorableAgents: scorableAgents,
		RubricVersion:  "default",
		PromptVersion:  "phase0-stub",
	}
	markerPath, err := run.IO.ResolveRunRelative("30/done.marker")
	if err != nil {
		return err
	}
	marker, err := internalio.ReadJSON[contracts.Step30DoneMarker](markerPath)
	if err != nil {
		return err
	}
	resp := stepio.Step30Response{
		RunID:           run.IO.RunID,
		ScoresCount:     int(marker.ExpectedCounts.Scores),
		ComplianceCount: int(marker.ExpectedCounts.Compliance),
		ResolvedAt:      marker.ResolvedAt,
	}
	payload, err := contracts.MarshalStrict(resp)
	if err != nil {
		return err
	}
	_, err = a.decode(payload, req)
	return err
}

type step20Adapter struct {
	impl *step20_implement.Step
}

func (s step20Adapter) Run(ctx context.Context, run *StepRunContext) error {
	if s.impl == nil {
		return errors.New("orchestrator: step20 implementation is not configured")
	}
	if run == nil {
		return errors.New("orchestrator: step run context is required")
	}
	return s.impl.Run(ctx, step20_implement.RunContext{
		Config:      run.Config,
		Logger:      run.Logger,
		PR:          run.PR,
		Pass:        run.Pass,
		Agent:       run.Agent,
		IO:          run.IO,
		TaskPackage: run.TaskPackage,
	})
}

type step10Adapter struct {
	runner *step10restorebase.Runner
	decode func([]byte, any) (any, error)
}

func (a step10Adapter) Run(ctx context.Context, run *StepRunContext) error {
	if a.runner == nil {
		return errors.New("orchestrator: step10 runner is not configured")
	}
	if run == nil {
		return errors.New("orchestrator: step run context is required")
	}
	if run.Config == nil {
		return errors.New("orchestrator: step10 config is required")
	}
	repoRoot, err := run.Config.RepoRoot()
	if err != nil {
		return err
	}
	req := stepio.Step10Request{
		PR:            run.PR,
		BestBranch:    run.Config.Repo.BestBranch,
		ExpectedRunID: run.IO.RunID,
		HarnessFiles:  true,
	}
	result, err := a.runner.Run(ctx, step10restorebase.Input{
		PR:               run.PR,
		BestBranch:       run.Config.Repo.BestBranch,
		PolicyBranch:     run.Config.Repo.PolicyBranch,
		TaskPromptSource: run.Config.TaskPromptSource(),
		HarnessFiles:     true,
		ExpectedRunID:    run.IO.RunID,
		RepoRoot:         repoRoot,
		Repo:             run.Config.Repo.GitHub,
		RunCtx:           run.IO,
		Agents:           defaultAgents,
		Logger:           run.Logger,
	})
	if err != nil {
		return err
	}
	if a.decode == nil {
		run.TaskPackage = &result.Response.TaskPackage
		return nil
	}
	payload, err := contracts.MarshalStrict(result.Response)
	if err != nil {
		return err
	}
	decoded, err := a.decode(payload, req)
	if err != nil {
		return err
	}
	switch value := decoded.(type) {
	case stepio.Step10Response:
		run.TaskPackage = &value.TaskPackage
	case *stepio.Step10Response:
		if value == nil {
			return errors.New("orchestrator: step10 decoder returned nil response")
		}
		run.TaskPackage = &value.TaskPackage
	default:
		return fmt.Errorf("orchestrator: unexpected step10 decoder result type %T", decoded)
	}
	return nil
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

type step50Adapter struct {
	impl *step50_implement.Step
}

func (s step50Adapter) Run(ctx context.Context, run *StepRunContext) error {
	if s.impl == nil {
		return errors.New("orchestrator: step50 implementation is not configured")
	}
	if run == nil {
		return errors.New("orchestrator: step run context is required")
	}
	return s.impl.Run(ctx, step50_implement.RunContext{
		Config:      run.Config,
		Logger:      run.Logger,
		PR:          run.PR,
		Pass:        run.Pass,
		Agent:       run.Agent,
		IO:          run.IO,
		TaskPackage: run.TaskPackage,
	})
}

type stubMarkerStep struct {
	path string
}

func (s stubMarkerStep) Run(ctx context.Context, run *StepRunContext) error {
	if s.path == "30/done.marker" {
		return step30_score.New().Run(ctx, step30_score.Request{
			RunContext:  run.IO,
			TaskPackage: run.TaskPackage,
		})
	}
	path, err := run.IO.ResolveRunRelative(s.path)
	if err != nil {
		return err
	}
	return internalio.WriteAtomic(path, []byte("stub\n"))
}

type step60Step struct {
	cfg    *config.Config
	decode func([]byte, any) (any, error)
}

func (s step60Step) Run(ctx context.Context, run *StepRunContext) error {
	cfg := s.cfg
	if run != nil && run.Config != nil {
		cfg = run.Config
	}
	primary, err := judges.NewJudgeFromConfig(cfg, contracts.JudgeRolePrimary)
	if err != nil {
		return err
	}
	secondary, err := judges.NewJudgeFromConfig(cfg, contracts.JudgeRoleSecondary)
	if err != nil {
		return err
	}
	arbiter, err := judges.NewJudgeFromConfig(cfg, contracts.JudgeRoleArbiter)
	if err != nil {
		return err
	}
	if err := step60_scorepairwise.Run(ctx, step60_scorepairwise.Input{
		IO:          run.IO,
		TaskPackage: run.TaskPackage,
		Primary:     primary,
		Secondary:   secondary,
		Arbiter:     arbiter,
	}); err != nil {
		return err
	}
	if s.decode == nil {
		return nil
	}
	scorableAgents, err := step60ScorableAgents(run.IO, run.TaskPackage)
	if err != nil {
		return err
	}
	req := stepio.Step60Request{
		TaskPackage:    *run.TaskPackage,
		ScorableAgents: scorableAgents,
		RubricVersion:  "default",
		PromptVersion:  "phase0-stub",
	}
	markerPath, err := run.IO.ResolveRunRelative("60/done.marker")
	if err != nil {
		return err
	}
	marker, err := internalio.ReadJSON[contracts.Step60DoneMarker](markerPath)
	if err != nil {
		return err
	}
	resp := stepio.Step60Response{
		RunID:           run.IO.RunID,
		ScoresCount:     int(marker.ExpectedCounts.Scores),
		ComplianceCount: int(marker.ExpectedCounts.Compliance),
		PairwiseCount:   int(marker.ExpectedCounts.Pairwise),
		ResolvedAt:      marker.ResolvedAt,
	}
	payload, err := contracts.MarshalStrict(resp)
	if err != nil {
		return err
	}
	_, err = s.decode(payload, req)
	return err
}

func seedStubPass1Scores(ctx context.Context, run *StepRunContext) error {
	scoresPath, err := run.IO.ResolveRunRelative("30/scores-A.jsonl")
	if err != nil {
		return fmt.Errorf("orchestrator: resolve step30 scores path: %w", err)
	}
	compliancePath, err := run.IO.ResolveRunRelative("30/compliance-A.jsonl")
	if err != nil {
		return fmt.Errorf("orchestrator: resolve step30 compliance path: %w", err)
	}
	if _, err := os.Stat(scoresPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("orchestrator: stat step30 scores path: %w", err)
	}

	agents, err := pass1Agents(run.TaskPackage)
	if err != nil {
		return err
	}
	complete, err := stubPass1ScoresComplete(scoresPath, agents)
	if err == nil && complete {
		return nil
	}

	judge := judges.NewPrimaryStub()
	rubricPath := filepath.Join(run.IO.RunDir(), "rubrics", "default.md")
	rows := make([]contracts.ScoreEntry, 0, len(agents)*5)
	complianceRows := make([]contracts.ComplianceEntry, 0, len(agents))
	for _, agent := range agents {
		manifest, err := internalio.LoadScorableManifest(run.IO, 1, agent)
		if err != nil {
			return fmt.Errorf("orchestrator: load pass1 manifest for agent=%s: %w", agent, err)
		}
		outputPath, err := run.IO.ResolveRunRelative(manifest.DiffPath)
		if err != nil {
			return fmt.Errorf("orchestrator: resolve pass1 diff path for agent=%s: %w", agent, err)
		}
		output, err := judge.ScoreOutput(ctx, judges.JudgeInput{
			RunID:      run.IO.RunID,
			Pass:       1,
			Agent:      agent,
			OutputPath: outputPath,
			RubricPath: rubricPath,
		})
		if err != nil {
			return fmt.Errorf("orchestrator: score pass1 stub output for agent=%s: %w", agent, err)
		}
		rows = append(rows, output.Scores...)
		complianceRows = append(complianceRows, output.Compliance...)
	}
	payload, err := marshalScoreJSONL(rows)
	if err != nil {
		return err
	}
	if err := internalio.WriteAtomic(scoresPath, payload); err != nil {
		return err
	}
	compliancePayload, err := marshalComplianceJSONL(complianceRows)
	if err != nil {
		return err
	}
	return internalio.WriteAtomic(compliancePath, compliancePayload)
}

type stubScoreKey struct {
	Agent     contracts.AgentID
	Dimension contracts.Dimension
}

var stubPass1Dimensions = []contracts.Dimension{
	contracts.DimensionFidelity,
	contracts.DimensionCorrectness,
	contracts.DimensionMaintainability,
	contracts.DimensionDiscipline,
	contracts.DimensionCommunication,
}

func stubPass1ScoresComplete(path string, agents []contracts.AgentID) (bool, error) {
	rows, err := internalio.ReadJSONL[contracts.ScoreEntry](path)
	if err != nil {
		return false, nil
	}
	collapsed := internalio.CollapseByKey(rows, func(entry contracts.ScoreEntry) stubScoreKey {
		return stubScoreKey{Agent: entry.Agent, Dimension: entry.Dimension}
	})
	expectedAgents := make(map[contracts.AgentID]struct{}, len(agents))
	perAgentDimensions := make(map[contracts.AgentID]map[contracts.Dimension]struct{}, len(agents))
	for _, agent := range agents {
		expectedAgents[agent] = struct{}{}
		perAgentDimensions[agent] = make(map[contracts.Dimension]struct{}, len(stubPass1Dimensions))
	}
	if len(collapsed) != len(agents)*len(stubPass1Dimensions) {
		return false, nil
	}
	for _, entry := range collapsed {
		if _, ok := expectedAgents[entry.Agent]; !ok {
			return false, nil
		}
		if entry.Pass != 1 {
			return false, nil
		}
		perAgentDimensions[entry.Agent][entry.Dimension] = struct{}{}
	}
	for _, agent := range agents {
		if len(perAgentDimensions[agent]) != len(stubPass1Dimensions) {
			return false, nil
		}
	}
	return true, nil
}

func pass1Agents(pkg *contracts.TaskPackage) ([]contracts.AgentID, error) {
	if pkg == nil {
		return nil, errors.New("orchestrator: task package is required")
	}
	agentsSet := make(map[contracts.AgentID]struct{}, len(pkg.Worktrees))
	for _, worktree := range pkg.Worktrees {
		if worktree.Pass == 1 {
			agentsSet[worktree.Agent] = struct{}{}
		}
	}
	agents := make([]contracts.AgentID, 0, len(agentsSet))
	for agent := range agentsSet {
		agents = append(agents, agent)
	}
	sort.Slice(agents, func(i, j int) bool { return agents[i] < agents[j] })
	return agents, nil
}

func step60ScorableAgents(runIO internalio.RunContext, pkg *contracts.TaskPackage) ([]contracts.AgentID, error) {
	if pkg == nil {
		return nil, errors.New("orchestrator: task package is required")
	}
	agents := make([]contracts.AgentID, 0, len(pkg.Worktrees))
	seen := make(map[contracts.AgentID]struct{}, len(pkg.Worktrees))
	for _, wt := range pkg.Worktrees {
		if wt.Pass != 2 {
			continue
		}
		if _, ok := seen[wt.Agent]; ok {
			continue
		}
		if _, err := internalio.LoadScorableManifest(runIO, 1, wt.Agent); err != nil {
			if shouldSkipScorableManifest(err) || os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("orchestrator: load step60 pass1 manifest for agent=%s: %w", wt.Agent, err)
		}
		if _, err := internalio.LoadScorableManifest(runIO, 2, wt.Agent); err != nil {
			if shouldSkipScorableManifest(err) {
				continue
			}
			return nil, fmt.Errorf("orchestrator: load step60 pass2 manifest for agent=%s: %w", wt.Agent, err)
		}
		seen[wt.Agent] = struct{}{}
		agents = append(agents, wt.Agent)
	}
	sort.Slice(agents, func(i, j int) bool { return agents[i] < agents[j] })
	return agents, nil
}

func marshalScoreJSONL(rows []contracts.ScoreEntry) ([]byte, error) {
	var buf bytes.Buffer
	for _, row := range rows {
		if _, err := contracts.MarshalStrict(row); err != nil {
			return nil, err
		}
		payload, err := contracts.CanonicalMarshal(row)
		if err != nil {
			return nil, err
		}
		if _, err := buf.Write(payload); err != nil {
			return nil, err
		}
		if err := buf.WriteByte('\n'); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

func marshalComplianceJSONL(rows []contracts.ComplianceEntry) ([]byte, error) {
	var buf bytes.Buffer
	for _, row := range rows {
		if _, err := contracts.MarshalStrict(row); err != nil {
			return nil, err
		}
		payload, err := contracts.CanonicalMarshal(row)
		if err != nil {
			return nil, err
		}
		if _, err := buf.Write(payload); err != nil {
			return nil, err
		}
		if err := buf.WriteByte('\n'); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

type stubStep40 struct {
	decode func([]byte, any) (any, error)
}

func (s stubStep40) Run(ctx context.Context, run *StepRunContext) error {
	registryPath, err := policyrepo.RegistryPathForRun(run.IO)
	if err != nil {
		return err
	}
	_, err = step40_classify.Run(ctx, step40_classify.Config{
		IO:           run.IO,
		RegistryPath: registryPath,
		TaskPackage:  run.TaskPackage,
		Now:          time.Now,
	})
	if err != nil {
		return err
	}
	candidatesPath, err := run.IO.ResolveRunRelative("40/candidates.json")
	if err != nil {
		return err
	}
	candidates, err := internalio.ReadJSON[contracts.Candidates](candidatesPath)
	if err != nil {
		return err
	}
	if s.decode != nil {
		req := stepio.Step40Request{
			TaskPackage:  *run.TaskPackage,
			RegistryPath: registryPath,
		}
		resp := stepio.Step40Response{
			RunID:           run.IO.RunID,
			Candidates:      candidates,
			CandidatesCount: len(candidates.Candidates),
		}
		payload, err := contracts.CanonicalMarshal(resp)
		if err != nil {
			return err
		}
		decoded, err := s.decode(payload, req)
		if err != nil {
			return err
		}
		validated, ok := decoded.(stepio.Step40Response)
		if !ok {
			return fmt.Errorf("orchestrator: step40 decoder returned %T", decoded)
		}
		run.Candidates = &validated.Candidates
		return nil
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
type realStep70 struct {
	cfg    *config.Config
	decode func([]byte, any) (any, error)
	runFn  func(context.Context, int, internalio.RunContext, *contracts.TaskPackage, *contracts.Candidates, step70_decide.IntentionWriter, step70_decide.Deps) error
}

func (s realStep70) Run(ctx context.Context, run *StepRunContext) error {
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
		RegistryHighAt: run.Config.RegistryHighThreshold,
		RegistryCritAt: run.Config.RegistryCriticalThreshold,
		RepoRoot:       repoRoot,
		PolicyBranch:   run.Config.Repo.PolicyBranch,
	}
	runStep := step70_decide.Run
	if s.runFn != nil {
		runStep = s.runFn
	}
	if err := runStep(ctx, run.PR, run.IO, run.TaskPackage, run.Candidates, store, deps); err != nil {
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
		if s.decode != nil {
			req := stepio.Step70Request{
				TaskPackage:  *run.TaskPackage,
				Candidates:   *run.Candidates,
				RegistryPath: run.IO.RulesRegistryPath(),
			}
			resp, err := step70_decide.BuildResponse(run.IO.RunID, decision, decision.Action == contracts.DecisionActionAdopt, req)
			if err != nil {
				return err
			}
			payload, err := resp.MarshalJSON()
			if err != nil {
				return err
			}
			if _, err := s.decode(payload, req); err != nil {
				return err
			}
		}
		run.Decision = &decision
	}
	return nil
}

// realArchiveStep is intentionally a no-op in the per-run step pipeline.
// Sunset/archive is a separate cycle-level tick owned by sunset_tick / the
// `auto-improve sunset` entry points, both of which delegate to
// internal/archive.RunSunsetWithLock.
type realArchiveStep struct{}

func (realArchiveStep) Run(ctx context.Context, run *StepRunContext) error {
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
