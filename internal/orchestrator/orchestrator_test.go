package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun_OrdersStepsAcrossParallelPasses(t *testing.T) {
	cfg := testConfig(t)
	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)

	recorder := &callRecorder{}
	orch.steps = Steps{
		Step10:  recordingStep{label: "10", recorder: recorder},
		Step20:  recordingAgentSteps("20", recorder),
		Step30:  recordingStep{label: "30", recorder: recorder},
		Step40:  recordingStep{label: "40", recorder: recorder},
		Step50:  recordingAgentSteps("50", recorder),
		Step60:  recordingStep{label: "60", recorder: recorder},
		Step70:  recordingStep{label: "70", recorder: recorder},
		Archive: recordingStep{label: "archive", recorder: recorder},
	}

	err = orch.Run(context.Background(), 42, RunOptions{
		RunID: "2026-04-21-PR42-abcdef0",
	})
	require.NoError(t, err)

	calls := recorder.snapshot()
	require.Len(t, calls, 12)
	assert.Equal(t, "10", calls[0])
	assert.Contains(t, calls[1:4], "20:a1")
	assert.Contains(t, calls[1:4], "20:a2")
	assert.Contains(t, calls[1:4], "20:a3")
	assert.Equal(t, "30", calls[4])
	assert.Equal(t, "40", calls[5])
	assert.Contains(t, calls[6:9], "50:a1")
	assert.Contains(t, calls[6:9], "50:a2")
	assert.Contains(t, calls[6:9], "50:a3")
	assert.Equal(t, "60", calls[9])
	assert.Equal(t, "70", calls[10])
	assert.Equal(t, "archive", calls[11])
}

func TestRun_ResumesFromIntentionStage(t *testing.T) {
	cfg := testConfig(t)
	runID := contracts.RunID("2026-04-21-PR55-abcdef0")
	runCtx, err := internalio.NewRunContext(runID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)

	require.NoError(t, seedResumeRun(t, runCtx, 55))
	writer := state.NewWriter(runCtx)
	require.NoError(t, writer.Append(contracts.StateEntry{
		Kind: contracts.StateKindInterrupted,
		Value: contracts.StateEntryInterrupted{
			Kind:   contracts.StateKindInterrupted,
			PR:     55,
			RunID:  runID,
			Step:   contracts.FailedStep70,
			Reason: contracts.InterruptedReasonUnknown,
			At:     time.Now().UTC(),
		},
	}))

	store := NewIntentionStore(runCtx)
	intention := validPlanningIntention(runID)
	require.NoError(t, store.Save(intention))

	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)
	recorder := &callRecorder{}
	orch.steps = Steps{
		Step10:  recordingStep{label: "10", recorder: recorder},
		Step20:  recordingAgentSteps("20", recorder),
		Step30:  recordingStep{label: "30", recorder: recorder},
		Step40:  recordingStep{label: "40", recorder: recorder},
		Step50:  recordingAgentSteps("50", recorder),
		Step60:  recordingStep{label: "60", recorder: recorder},
		Step70:  stubStep70{},
		Archive: recordingStep{label: "archive", recorder: recorder},
	}

	err = orch.Run(context.Background(), 55, RunOptions{})
	require.NoError(t, err)

	assert.Equal(t, []string{"archive"}, recorder.snapshot())
	decisionPath, err := runCtx.ResolveRunRelative("70/decision.json")
	require.NoError(t, err)
	assert.FileExists(t, decisionPath)
	loaded, err := store.Load()
	require.NoError(t, err)
	assert.Nil(t, loaded)
}

func TestRun_DefaultStub_EndToEnd(t *testing.T) {
	cfg := testConfig(t)
	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)
	orch.steps.Step10 = stubStep10{}
	orch.steps.Step20 = stubAgentSteps()
	orch.steps.Step50 = stubAgentSteps()
	orch.steps.Step70 = stubStep70{}

	err = orch.Run(context.Background(), 77, RunOptions{
		RunID: "2026-04-21-PR77-abcdef0",
	})
	require.NoError(t, err)

	taskPackagePath := filepath.Join(cfg.Paths.Runs, "2026-04-21-PR77-abcdef0", "task-package.json")
	decisionPath := filepath.Join(cfg.Paths.Runs, "2026-04-21-PR77-abcdef0", "70", "decision.json")
	assert.FileExists(t, taskPackagePath)
	assert.FileExists(t, decisionPath)

	pkg, err := internalio.ReadJSON[contracts.TaskPackage](taskPackagePath)
	require.NoError(t, err)
	for _, worktree := range pkg.Worktrees {
		assert.NoDirExists(t, worktree.Path)
	}

	runCtx, err := internalio.RunContextFromTaskPackage(pkg, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	events, err := state.ScanEventsForRun(runCtx, pkg.RunID)
	require.NoError(t, err)
	require.NotEmpty(t, events)
	assert.Equal(t, contracts.StateKindCompleted, events[len(events)-1].Kind)
}

func TestRun_DefaultSteps_RealWiringWithFakeCLIs(t *testing.T) {
	cfg := testConfig(t)
	cfg.Repo.Root = repoRootFromTestFile(t)
	binDir := installFakeCLI(t)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cfg.ClaudeCLIPath = filepath.Join(binDir, "claude")

	t.Setenv("AUTO_IMPROVE_TEST_BASE_SHA", strings.Repeat("a", 40))
	t.Setenv("AUTO_IMPROVE_TEST_TARGET_SHA", strings.Repeat("b", 40))
	t.Setenv("AUTO_IMPROVE_TEST_MERGE_SHA", strings.Repeat("c", 40))
	t.Setenv("AUTO_IMPROVE_TEST_BEST_SHA", strings.Repeat("d", 40))

	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)

	runID := contracts.RunID("2026-04-21-PR42-abcdeff")
	require.NoError(t, orch.Run(context.Background(), 42, RunOptions{RunID: runID}))

	runDir := filepath.Join(cfg.Paths.Runs, string(runID))
	taskPackagePath := filepath.Join(runDir, "task-package.json")
	decisionPath := filepath.Join(runDir, "70", "decision.json")
	assert.FileExists(t, taskPackagePath)
	assert.FileExists(t, decisionPath)
	assert.FileExists(t, filepath.Join(runDir, "30", "done.marker"))
	assert.NoFileExists(t, filepath.Join(runDir, "60", "done.marker"))

	decision, err := internalio.ReadJSON[contracts.Decision](decisionPath)
	require.NoError(t, err)
	assert.Equal(t, contracts.DecisionActionNoop, decision.Action)

	_, err = internalio.ReadJSON[contracts.TaskPackage](taskPackagePath)
	require.NoError(t, err)
	for _, agent := range []contracts.AgentID{"a1", "a2", "a3"} {
		manifest, manifestErr := internalio.LoadFinalizedManifest(internalio.RunContext{
			RunID:        runID,
			RunsBase:     cfg.Paths.Runs,
			WorktreeBase: cfg.Worktree.Base,
		}, 1, agent)
		require.NoError(t, manifestErr)
		require.NotNil(t, manifest)
		assert.Equal(t, contracts.ManifestKindSuccess, manifest.Kind)
	}
}

func TestStubMarkerStep_SeedsPass1ScoresFromTaskPackageWorktrees(t *testing.T) {
	cfg := testConfig(t)
	agents := []contracts.AgentID{"a2", "a4", "a7"}
	runID := contracts.RunID("2026-04-21-PR66-abcdef0")

	worktrees := make([]contracts.WorktreeAllocation, 0, len(agents)*2)
	for pass := 1; pass <= 2; pass++ {
		for _, agent := range agents {
			worktrees = append(worktrees, contracts.WorktreeAllocation{
				Agent:   agent,
				Pass:    pass,
				Path:    filepath.Join(cfg.Worktree.Base, string(runID), fmt.Sprintf("pass%d", pass), string(agent)),
				Branch:  fmt.Sprintf("stub/%s/pass%d/%s", runID, pass, agent),
				BaseSHA: strings.Repeat("a", 40),
				HeadSHA: strings.Repeat("b", 40),
			})
		}
	}
	pkg := contracts.TaskPackage{
		SchemaVersion:           "1",
		RunID:                   runID,
		PR:                      66,
		Title:                   "stub seed",
		BaseSHA:                 strings.Repeat("a", 40),
		BestBranch:              "best",
		ReconstructedTaskPrompt: "stub seed prompt",
		Worktrees:               worktrees,
		CreatedAt:               time.Now().UTC(),
	}
	require.NoError(t, pkg.Validate())

	runCtx, err := internalio.RunContextFromTaskPackage(pkg, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(runCtx.RunDir(), 0o755))

	stepRun := &StepRunContext{Config: cfg, IO: runCtx, TaskPackage: &pkg}
	for _, agent := range agents {
		require.NoError(t, stubImplementStep{}.Run(context.Background(), &StepRunContext{
			Config:      cfg,
			IO:          runCtx,
			TaskPackage: &pkg,
			Pass:        1,
			Agent:       agent,
		}))
	}

	require.NoError(t, stubMarkerStep{path: "30/done.marker"}.Run(context.Background(), stepRun))

	scoresPath, err := runCtx.ResolveRunRelative("30/scores-A.jsonl")
	require.NoError(t, err)
	scores, err := internalio.ReadJSONL[contracts.ScoreEntry](scoresPath)
	require.NoError(t, err)
	require.Len(t, scores, len(agents)*5)

	seenAgents := make(map[contracts.AgentID]int, len(agents))
	for _, score := range scores {
		seenAgents[score.Agent]++
	}
	assert.Equal(t, map[contracts.AgentID]int{"a2": 5, "a4": 5, "a7": 5}, seenAgents)

	before := mustReadFile(t, scoresPath)
	require.NoError(t, stubMarkerStep{path: "30/done.marker"}.Run(context.Background(), stepRun))
	assert.Equal(t, before, mustReadFile(t, scoresPath))
}

func TestIntentionStore_RoundTrip(t *testing.T) {
	cfg := testConfig(t)
	runCtx, err := internalio.NewRunContext("2026-04-21-PR88-abcdef0", cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)

	store := NewIntentionStore(runCtx)
	record := validPlanningIntention(runCtx.RunID)
	require.NoError(t, store.Save(record))

	loaded, err := store.Load()
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, contracts.IntentionStagePlanning, loaded.Stage)

	require.NoError(t, store.Transition(contracts.IntentionStageNeedsManualRecovery, func(r *contracts.IntentionRecord) error {
		r.RecoveryReason = contracts.RollbackReasonRemoteDivergence
		r.FailedStep = contracts.FailedStep70
		return nil
	}))
	loaded, err = store.Load()
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, contracts.IntentionStageNeedsManualRecovery, loaded.Stage)

	require.NoError(t, store.Delete())
	loaded, err = store.Load()
	require.NoError(t, err)
	assert.Nil(t, loaded)
}

func TestRealArchiveStep_NoOpLeavesSunsetStateUntouched(t *testing.T) {
	cfg := testConfig(t)
	runCtx, err := internalio.NewRunContext("2026-04-21-PR89-abcdef0", cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(runCtx.RunsBase, 0o755))

	markerPath := filepath.Join(runCtx.RunsBase, "sunset-running.marker")
	lastSunsetPath := filepath.Join(runCtx.RunsBase, "last-sunset-at")
	require.NoError(t, os.WriteFile(markerPath, []byte("2026-04-21T09:00:00Z\nstale-run\n"), 0o644))
	require.NoError(t, os.WriteFile(lastSunsetPath, []byte("2026-04-21T08:00:00Z\n"), 0o644))

	step := realArchiveStep{}
	require.NoError(t, step.Run(context.Background(), &StepRunContext{IO: runCtx}))
	require.NoError(t, step.Run(context.Background(), &StepRunContext{IO: runCtx}))

	markerBody, err := os.ReadFile(markerPath)
	require.NoError(t, err)
	assert.Equal(t, "2026-04-21T09:00:00Z\nstale-run\n", string(markerBody))

	lastSunsetBody, err := os.ReadFile(lastSunsetPath)
	require.NoError(t, err)
	assert.Equal(t, "2026-04-21T08:00:00Z\n", string(lastSunsetBody))
}

type callRecorder struct {
	mu    sync.Mutex
	calls []string
}

func (r *callRecorder) add(call string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, call)
}

func (r *callRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

type recordingStep struct {
	label    string
	recorder *callRecorder
}

func (s recordingStep) Run(ctx context.Context, run *StepRunContext) error {
	_ = ctx
	s.recorder.add(s.label)
	switch s.label {
	case "10":
		return stubStep10{}.Run(ctx, run)
	case "40":
		if err := (stubStep40{}).Run(ctx, run); err != nil {
			return err
		}
		if run.Candidates == nil || len(run.Candidates.Candidates) == 0 {
			candidates := []contracts.Candidate{{
				CandidateID:        "cand-test-001",
				Kind:               contracts.CandidateKindNew,
				Title:              "stub candidate",
				Problem:            "problem",
				Rationale:          "rationale",
				ProposedBodyPath:   "40/candidates/cand-test-001.md",
				ProposedBodySha256: strings.Repeat("a", 64),
			}}
			run.Candidates = &contracts.Candidates{
				SchemaVersion:  "1",
				RunID:          run.IO.RunID,
				Candidates:     candidates,
				CandidatesHash: contracts.CanonicalCandidatesHash(candidates),
				CreatedAt:      time.Now().UTC(),
			}
			candidatesPath, err := run.IO.ResolveRunRelative("40/candidates.json")
			if err != nil {
				return err
			}
			if err := internalio.WriteJSONAtomic(candidatesPath, run.Candidates); err != nil {
				return err
			}
		}
		return nil
	case "70":
		return stubStep70{}.Run(ctx, run)
	default:
		return nil
	}
}

type recordingAgentStep struct {
	prefix   string
	recorder *callRecorder
}

func (s recordingAgentStep) Run(ctx context.Context, run *StepRunContext) error {
	s.recorder.add(s.prefix + ":" + string(run.Agent))
	return stubImplementStep{}.Run(ctx, run)
}

func recordingAgentSteps(prefix string, recorder *callRecorder) map[contracts.AgentID]Step {
	steps := make(map[contracts.AgentID]Step, len(defaultAgents))
	for _, agent := range defaultAgents {
		steps[agent] = recordingAgentStep{
			prefix:   prefix,
			recorder: recorder,
		}
	}
	return steps
}

func stubAgentSteps() map[contracts.AgentID]Step {
	steps := make(map[contracts.AgentID]Step, len(defaultAgents))
	for _, agent := range defaultAgents {
		steps[agent] = stubImplementStep{}
	}
	return steps
}

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	runsBase := filepath.Join(t.TempDir(), "runs")
	worktreeBase := filepath.Join(t.TempDir(), "worktrees")
	return &config.Config{
		Repo: config.RepoConfig{
			Root:          t.TempDir(),
			DefaultBranch: "main",
			BestBranch:    "best",
		},
		Worktree: config.WorktreeConfig{
			Base: worktreeBase,
		},
		Paths: config.PathsConfig{
			Runs: runsBase,
		},
		RegistryHighThreshold:     config.DefaultRegistryHighThreshold,
		RegistryCriticalThreshold: config.DefaultRegistryCriticalThreshold,
		StepTimeouts: map[string]int{
			"step10": 300,
			"step20": 300,
			"step30": 300,
			"step40": 300,
			"step50": 300,
			"step60": 300,
			"step70": 300,
		},
	}
}

func repoRootFromTestFile(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

func installFakeCLI(t *testing.T) string {
	t.Helper()
	sourceDir := filepath.Join(repoRootFromTestFile(t), "internal", "orchestrator", "testdata", "bin")
	destDir := t.TempDir()
	for _, name := range []string{"gh", "git", "claude"} {
		src := filepath.Join(sourceDir, name)
		data, err := os.ReadFile(src)
		require.NoError(t, err)
		dst := filepath.Join(destDir, name)
		require.NoError(t, os.WriteFile(dst, data, 0o755))
	}
	return destDir
}

func seedResumeRun(t *testing.T, runCtx internalio.RunContext, pr int) error {
	t.Helper()
	if err := os.MkdirAll(runCtx.RunDir(), 0o755); err != nil {
		return err
	}
	pkg := contracts.TaskPackage{
		SchemaVersion:           "1",
		RunID:                   runCtx.RunID,
		PR:                      pr,
		Title:                   "resume",
		BaseSHA:                 strings.Repeat("a", 40),
		BestBranch:              "best",
		ReconstructedTaskPrompt: "resume prompt",
		CreatedAt:               time.Now().UTC(),
	}
	for pass := 1; pass <= 2; pass++ {
		for _, agent := range defaultAgents {
			pkg.Worktrees = append(pkg.Worktrees, contracts.WorktreeAllocation{
				Agent:   agent,
				Pass:    pass,
				Path:    filepath.Join(runCtx.WorktreeBase, fmt.Sprintf("%s-pass%d-%s", runCtx.RunID, pass, agent)),
				Branch:  fmt.Sprintf("resume/%s/pass%d/%s", runCtx.RunID, pass, agent),
				BaseSHA: strings.Repeat("a", 40),
				HeadSHA: strings.Repeat("a", 40),
			})
		}
	}
	if err := internalio.WriteJSONAtomic(runCtx.TaskPackagePath(), pkg); err != nil {
		return err
	}
	if err := internalio.WriteAtomic(runCtx.BaseSHAPath(), []byte(strings.Repeat("a", 40)+"\n")); err != nil {
		return err
	}
	for pass := 1; pass <= 2; pass++ {
		for _, agent := range defaultAgents {
			prefix := manifestPrefix(pass, agent)
			if err := writeRunText(runCtx, filepath.Join(prefix, "diff.patch"), ""); err != nil {
				return err
			}
			if err := writeRunText(runCtx, filepath.Join(prefix, "session.jsonl"), ""); err != nil {
				return err
			}
			if err := writeRunText(runCtx, filepath.Join(prefix, "checklist-result.json"), "{}\n"); err != nil {
				return err
			}
			manifest := contracts.Manifest{
				Kind: contracts.ManifestKindSuccess,
				Value: contracts.ManifestSuccess{
					Kind:          contracts.ManifestKindSuccess,
					SchemaVersion: "1",
					RunID:         runCtx.RunID,
					Pass:          pass,
					Agent:         agent,
					BranchName:    fmt.Sprintf("resume/%s/pass%d/%s", runCtx.RunID, pass, agent),
					HeadSHA:       strings.Repeat("a", 40),
					BaseSHA:       strings.Repeat("a", 40),
					DiffPath:      filepath.Join(prefix, "diff.patch"),
					SessionPath:   filepath.Join(prefix, "session.jsonl"),
					ChecklistPath: filepath.Join(prefix, "checklist-result.json"),
					PromptVersion: "stub",
					StartedAt:     time.Now().UTC(),
					FinishedAt:    time.Now().UTC(),
				},
			}
			manifestPath, err := runCtx.ManifestPath(pass, agent)
			if err != nil {
				return err
			}
			if err := internalio.WriteJSONAtomic(manifestPath, manifest); err != nil {
				return err
			}
		}
	}
	if err := writeRunText(runCtx, "30/done.marker", "stub\n"); err != nil {
		return err
	}
	candidates := contracts.Candidates{
		SchemaVersion:  "1",
		RunID:          runCtx.RunID,
		Candidates:     []contracts.Candidate{},
		CandidatesHash: contracts.CanonicalCandidatesHash(nil),
		CreatedAt:      time.Now().UTC(),
	}
	candidatesPath, err := runCtx.ResolveRunRelative("40/candidates.json")
	if err != nil {
		return err
	}
	if err := internalio.WriteJSONAtomic(candidatesPath, candidates); err != nil {
		return err
	}
	return writeRunText(runCtx, "60/done.marker", "stub\n")
}

func validPlanningIntention(runID contracts.RunID) contracts.IntentionRecord {
	best := strings.Repeat("1", 40)
	target := strings.Repeat("2", 40)
	hash := strings.Repeat("3", 64)
	idempotencyKey := contracts.ComputeAdoptIdempotencyKey(string(runID), target, best, hash)
	return contracts.IntentionRecord{
		SchemaVersion:      "1",
		Stage:              contracts.IntentionStagePlanning,
		IdempotencyKey:     idempotencyKey,
		RunID:              runID,
		BestShaBefore:      best,
		TargetSha:          target,
		CandidatesHash:     hash,
		RegistryHeadBefore: "",
		PlannedAdoption: &contracts.PlannedAdoption{
			IdempotencyKey: idempotencyKey,
			Entries: []contracts.PlannedAdoptionEntry{
				{
					OpID:     contracts.ComputePlannedAdoptionEntryOpID(idempotencyKey, 0, "r-0001"),
					Kind:     contracts.RegistryKindAdded,
					RuleID:   "r-0001",
					RulePath: "rules/r-0001.md",
					Sha256:   strings.Repeat("4", 64),
				},
			},
		},
		StartedAt: time.Now().UTC(),
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}
