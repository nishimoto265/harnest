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

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/judges"
	"github.com/nishimoto265/auto-improve/internal/steps/step60_scorepairwise"
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
		Step60:  step60Step{},
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
	if s.path == "30/done.marker" {
		if err := seedStubPass1Scores(ctx, run); err != nil {
			return err
		}
	}
	path, err := run.IO.ResolveRunRelative(s.path)
	if err != nil {
		return err
	}
	return internalio.WriteAtomic(path, []byte("stub\n"))
}

type step60Step struct{}

func (step60Step) Run(ctx context.Context, run *StepRunContext) error {
	if err := seedStubPass1Scores(ctx, run); err != nil {
		return err
	}
	return step60_scorepairwise.Run(ctx, step60_scorepairwise.Input{
		IO:          run.IO,
		TaskPackage: run.TaskPackage,
		Primary:     judges.NewPrimaryStub(),
		Secondary:   judges.NewSecondaryStub(),
		Arbiter:     judges.NewArbiterStub(),
	})
}

func seedStubPass1Scores(ctx context.Context, run *StepRunContext) error {
	scoresPath, err := run.IO.ResolveRunRelative("30/scores-A.jsonl")
	if err != nil {
		return fmt.Errorf("orchestrator: resolve step30 scores path: %w", err)
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
	}
	payload, err := marshalScoreJSONL(rows)
	if err != nil {
		return err
	}
	return internalio.WriteAtomic(scoresPath, payload)
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
