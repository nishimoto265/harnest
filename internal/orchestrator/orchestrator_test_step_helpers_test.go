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

	"github.com/nishimoto265/auto-improve/internal/agents"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/contracts/stepio"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/judges"
	"github.com/nishimoto265/auto-improve/internal/state"
	"github.com/nishimoto265/auto-improve/internal/steps/agentrunner"
	"github.com/nishimoto265/auto-improve/internal/steps/step20_implement"
	"github.com/nishimoto265/auto-improve/internal/steps/step60_scorepairwise"
	"github.com/nishimoto265/auto-improve/internal/steps/step70_decide"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type callRecorder struct {
	mu    sync.Mutex
	calls []string
}

type cancelAfterNoopStep struct {
	cancel context.CancelFunc
}

func (s *cancelAfterNoopStep) Run(ctx context.Context, run *StepRunContext) error {
	if err := (stubStep70{}).Run(ctx, run); err != nil {
		return err
	}
	if s.cancel != nil {
		s.cancel()
	}
	return nil
}

type cancelAfterTerminalPromoteStep struct {
	cancel context.CancelFunc
}

func (s *cancelAfterTerminalPromoteStep) Run(ctx context.Context, run *StepRunContext) error {
	if err := (terminalPromoteStep{}).Run(ctx, run); err != nil {
		return err
	}
	if s.cancel != nil {
		s.cancel()
	}
	return nil
}

type policySnapshotStaleStep struct{}

func (policySnapshotStaleStep) Run(context.Context, *StepRunContext) error {
	return &step70_decide.PolicySnapshotStaleError{Reason: "policy_branch_stale"}
}

type blockingStartStep struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

type manualRecoveryStep struct {
	reason contracts.RollbackReason
	detail string
}

func (s manualRecoveryStep) Run(context.Context, *StepRunContext) error {
	return &agentrunner.ManualRecoveryRequiredError{
		Reason: s.reason,
		Detail: s.detail,
	}
}

type tamperedDecisionStep70 struct {
	runID contracts.RunID
}

type assertPolicyBranchStep struct {
	t    *testing.T
	want string
}

func (s assertPolicyBranchStep) Run(_ context.Context, run *StepRunContext) error {
	s.t.Helper()
	require.NotNil(s.t, run.Config)
	assert.Equal(s.t, s.want, run.Config.Repo.PolicyBranch)
	return stubStep70{}.Run(context.Background(), run)
}

type assertImplementerProviderStep struct {
	t    *testing.T
	want agents.Provider
}

func (s assertImplementerProviderStep) Run(_ context.Context, run *StepRunContext) error {
	s.t.Helper()
	require.NotNil(s.t, run.Config)
	profile, err := run.Config.AgentProfile(agents.RoleImplementer)
	require.NoError(s.t, err)
	assert.Equal(s.t, s.want, profile.Provider)
	return stubStep70{}.Run(context.Background(), run)
}

func (s tamperedDecisionStep70) Run(_ context.Context, run *StepRunContext) error {
	path, err := run.IO.ResolveRunRelative("70/decision.json")
	if err != nil {
		return err
	}
	return internalio.WriteJSONAtomic(path, contracts.Decision{
		Action: contracts.DecisionActionNoop,
		Value: contracts.DecisionNoop{
			Action:        contracts.DecisionActionNoop,
			SchemaVersion: "1",
			RunID:         s.runID,
			Reason:        "tampered",
			DecidedAt:     time.Now().UTC(),
		},
	})
}

func newBlockingStartStep() *blockingStartStep {
	return &blockingStartStep{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (s *blockingStartStep) Run(ctx context.Context, run *StepRunContext) error {
	s.once.Do(func() {
		close(s.started)
	})
	<-s.release
	return stubStep10{}.Run(ctx, run)
}

func stubPipelineSteps(step10 Step, step70 Step) Steps {
	if step10 == nil {
		step10 = stubStep10{}
	}
	if step70 == nil {
		step70 = stubStep70{}
	}
	return Steps{
		Step10: step10,
		Step20: map[contracts.AgentID]Step{
			"a1": stubImplementStep{},
			"a2": stubImplementStep{},
			"a3": stubImplementStep{},
		},
		Step30: stubMarkerStep{path: "30/done.marker"},
		Step40: duplicateOnlyCandidateStep{},
		Step50: map[contracts.AgentID]Step{
			"a1": stubImplementStep{},
			"a2": stubImplementStep{},
			"a3": stubImplementStep{},
		},
		Step60:  step60Step{},
		Step70:  step70,
		Archive: stubArchiveStep{},
	}
}

type forcedCandidateStep struct{}

func (forcedCandidateStep) Run(ctx context.Context, run *StepRunContext) error {
	_ = ctx
	body := "# Forced rule\n\nUse explicit resource cleanup.\n"
	candidate := contracts.Candidate{
		CandidateID:        "cand-forced-001",
		Kind:               contracts.CandidateKindNew,
		Title:              "Forced rule",
		Problem:            "problem",
		Rationale:          "rationale",
		ProposedBodyPath:   "40/candidates/cand-forced-001.md",
		ProposedBodySha256: sha256String(body),
	}
	if err := candidate.Validate(); err != nil {
		return err
	}
	candidates := &contracts.Candidates{
		SchemaVersion:  "1",
		RunID:          run.IO.RunID,
		Candidates:     []contracts.Candidate{candidate},
		CandidatesHash: contracts.CanonicalCandidatesHash([]contracts.Candidate{candidate}),
		CreatedAt:      time.Now().UTC(),
	}
	sidecarPath, err := run.IO.ResolveRunRelative(candidate.ProposedBodyPath)
	if err != nil {
		return err
	}
	if err := internalio.WriteAtomic(sidecarPath, []byte(body)); err != nil {
		return err
	}
	candidatesPath, err := run.IO.ResolveRunRelative("40/candidates.json")
	if err != nil {
		return err
	}
	if err := internalio.WriteJSONAtomic(candidatesPath, candidates); err != nil {
		return err
	}
	run.Candidates = candidates
	return nil
}

type duplicateOnlyCandidateStep struct{}

func (duplicateOnlyCandidateStep) Run(ctx context.Context, run *StepRunContext) error {
	_ = ctx
	body := "# Duplicate rule\n\n- source_rule_id: rule-existing\n- classification: duplicate\n"
	candidate := contracts.Candidate{
		CandidateID:        "cand-dup-only",
		Kind:               contracts.CandidateKindDuplicate,
		TargetRuleID:       "rule-existing",
		Title:              "Duplicate rule",
		Problem:            "problem",
		Rationale:          "rationale",
		ProposedBodyPath:   "40/candidates/cand-dup-only.md",
		ProposedBodySha256: sha256String(body),
	}
	candidates := &contracts.Candidates{
		SchemaVersion:  "1",
		RunID:          run.IO.RunID,
		Candidates:     []contracts.Candidate{candidate},
		CandidatesHash: contracts.CanonicalCandidatesHash([]contracts.Candidate{candidate}),
		CreatedAt:      time.Now().UTC(),
	}
	sidecarPath, err := run.IO.ResolveRunRelative(candidate.ProposedBodyPath)
	if err != nil {
		return err
	}
	if err := internalio.WriteAtomic(sidecarPath, []byte(body)); err != nil {
		return err
	}
	candidatesPath, err := run.IO.ResolveRunRelative("40/candidates.json")
	if err != nil {
		return err
	}
	if err := internalio.WriteJSONAtomic(candidatesPath, candidates); err != nil {
		return err
	}
	run.Candidates = candidates
	return nil
}

type rescueExhaustedStep struct{}

func (rescueExhaustedStep) Run(ctx context.Context, run *StepRunContext) error {
	_ = ctx
	return &step20_implement.RescueExhaustedError{
		Rescue: stepio.RescueExhausted{
			Agent:      run.Agent,
			RetryCount: 3,
		},
	}
}

type leaseContendedOnceStep struct {
	mu   sync.Mutex
	seen bool
}

func (s *leaseContendedOnceStep) Run(ctx context.Context, run *StepRunContext) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.seen {
		s.seen = true
		return fmt.Errorf("%w: agent %s", step20_implement.ErrAgentLeaseContended, run.Agent)
	}
	return stubImplementStep{}.Run(ctx, run)
}

type terminalPromoteStep struct{}

func (terminalPromoteStep) Run(ctx context.Context, run *StepRunContext) error {
	_ = ctx
	path, err := run.IO.ResolveRunRelative("70/decision.json")
	if err != nil {
		return err
	}
	bestShaBefore := strings.Repeat("1", 40)
	targetSha := strings.Repeat("2", 40)
	candidatesHash := strings.Repeat("3", 64)
	decision := contracts.Decision{
		Action: contracts.DecisionActionAdopt,
		Value: contracts.DecisionAdopt{
			Action:        contracts.DecisionActionAdopt,
			SchemaVersion: "1",
			RunID:         run.IO.RunID,
			IdempotencyKey: contracts.ComputeAdoptIdempotencyKey(
				string(run.IO.RunID),
				targetSha,
				bestShaBefore,
				candidatesHash,
			),
			BestShaBefore:  bestShaBefore,
			TargetSha:      targetSha,
			CandidatesHash: candidatesHash,
			RegistryAppendResult: contracts.RegistryAppendResult{
				Offset: 0,
				Sha256: strings.Repeat("a", 64),
			},
			DecidedAt: time.Now().UTC(),
		},
	}
	if err := internalio.WriteJSONAtomic(path, decision); err != nil {
		return err
	}
	writer := state.NewWriter(run.IO)
	if err := writer.Append(promotedEntry(run.PR, run.IO.RunID, time.Now().UTC())); err != nil {
		return err
	}
	run.Decision = &decision
	return nil
}

func forcedCandidate(runID contracts.RunID) *contracts.Candidates {
	body := "# Forced rule\n\nUse explicit resource cleanup.\n"
	candidate := contracts.Candidate{
		CandidateID:        "cand-forced-001",
		Kind:               contracts.CandidateKindNew,
		Title:              "Forced rule",
		Problem:            "problem",
		Rationale:          "rationale",
		ProposedBodyPath:   "40/candidates/cand-forced-001.md",
		ProposedBodySha256: sha256String(body),
	}
	return &contracts.Candidates{
		SchemaVersion:  "1",
		RunID:          runID,
		Candidates:     []contracts.Candidate{candidate},
		CandidatesHash: contracts.CanonicalCandidatesHash([]contracts.Candidate{candidate}),
		CreatedAt:      time.Now().UTC(),
	}
}

type scriptedStep60Step struct {
	decode func([]byte, any) (any, error)
}

func (s scriptedStep60Step) Run(ctx context.Context, run *StepRunContext) error {
	pass1RubricVersion := step30RubricVersionForStep60(run.IO)
	if err := step60_scorepairwise.Run(ctx, step60_scorepairwise.Input{
		IO:            run.IO,
		TaskPackage:   run.TaskPackage,
		RubricVersion: pass1RubricVersion,
		Primary:       orchestratorJudge{score: 95},
		Secondary:     orchestratorJudge{score: 94},
		Arbiter:       orchestratorJudge{score: 95},
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
	versions, err := step60ScoringVersions(run.IO)
	if err != nil {
		return err
	}
	req := stepio.Step60Request{
		TaskPackage:    *run.TaskPackage,
		ScorableAgents: scorableAgents,
		RubricVersion:  versions.RubricVersion,
		PromptVersion:  versions.PromptVersion,
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

type orchestratorJudge struct {
	score int
}

type branchPushedCrashStep struct {
	t *testing.T
}

func (s branchPushedCrashStep) Run(ctx context.Context, run *StepRunContext) error {
	s.t.Helper()
	repoRoot, err := run.Config.RepoRoot()
	require.NoError(s.t, err)
	resolver := step70_decide.FilesystemResolver{RepoDir: repoRoot}
	target, ok, err := resolver.Resolve(run.IO, run.TaskPackage, run.Candidates)
	require.NoError(s.t, err)
	require.True(s.t, ok)

	bestSHA := os.Getenv("AUTO_IMPROVE_TEST_BEST_SHA")
	target.TargetSHA = bestSHA
	target.BestShaBefore = bestSHA
	idempotencyKey := contracts.ComputeAdoptIdempotencyKey(string(run.IO.RunID), target.TargetSHA, bestSHA, run.Candidates.CandidatesHash)
	plannedEntries := make([]contracts.PlannedAdoptionEntry, 0, len(target.RulesToAppend))
	for idx, entry := range target.RulesToAppend {
		var (
			ruleID   string
			rulePath string
			sha256   string
		)
		switch value := entry.Value.(type) {
		case contracts.RuleRegistryAdded:
			ruleID = value.RuleID
			rulePath = value.RulePath
			sha256 = value.Sha256
		case contracts.RuleRegistryUpdated:
			ruleID = value.RuleID
			rulePath = value.RulePath
			sha256 = value.Sha256
		default:
			s.t.Fatalf("unexpected planned registry entry type %T", entry.Value)
		}
		plannedEntries = append(plannedEntries, contracts.PlannedAdoptionEntry{
			OpID:     contracts.ComputePlannedAdoptionEntryOpID(idempotencyKey, idx, ruleID),
			Kind:     entry.Kind,
			RuleID:   ruleID,
			RulePath: rulePath,
			Sha256:   sha256,
		})
	}

	intention := contracts.IntentionRecord{
		SchemaVersion:      "1",
		Stage:              contracts.IntentionStageBranchPushed,
		IdempotencyKey:     idempotencyKey,
		RunID:              run.IO.RunID,
		BestShaBefore:      bestSHA,
		TargetSha:          target.TargetSHA,
		CandidatesHash:     run.Candidates.CandidatesHash,
		RegistryHeadBefore: "",
		PlannedAdoption: &contracts.PlannedAdoption{
			IdempotencyKey: idempotencyKey,
			Entries:        plannedEntries,
		},
		StartedAt: time.Now().UTC(),
	}
	require.NoError(s.t, run.IntentionFile.Save(intention))
	return fmt.Errorf("simulated branch_pushed crash")
}

func (j orchestratorJudge) ScoreOutput(ctx context.Context, input judges.JudgeInput) (judges.JudgeOutput, error) {
	if err := input.Validate(); err != nil {
		return judges.JudgeOutput{}, err
	}
	select {
	case <-ctx.Done():
		return judges.JudgeOutput{}, ctx.Err()
	default:
	}
	scores := make([]contracts.ScoreEntry, 0, 5)
	dimensions := []contracts.Dimension{
		contracts.DimensionFidelity,
		contracts.DimensionCorrectness,
		contracts.DimensionMaintainability,
		contracts.DimensionDiscipline,
		contracts.DimensionCommunication,
	}
	for _, dimension := range dimensions {
		scores = append(scores, contracts.ScoreEntry{
			SchemaVersion: "1",
			RunID:         input.RunID,
			Pass:          input.Pass,
			Agent:         input.Agent,
			Dimension:     dimension,
			Score:         j.score,
			Reasons:       "scripted adopt score",
			VerdictPath:   contracts.VerdictPathSingle,
			RubricVersion: "default",
			PromptVersion: "phase0-stub",
			ResolvedAt:    time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
		})
	}
	ruleIDs := input.ExpectedComplianceRuleIDs
	if len(ruleIDs) == 0 {
		ruleIDs = []string{"shared"}
	}
	compliance := make([]contracts.ComplianceEntry, 0, len(ruleIDs))
	for _, ruleID := range ruleIDs {
		compliance = append(compliance, contracts.ComplianceEntry{
			SchemaVersion: "1",
			RunID:         input.RunID,
			Pass:          input.Pass,
			Agent:         input.Agent,
			RuleID:        ruleID,
			Verdict:       contracts.ComplianceVerdictCompliant,
			Rationale:     "scripted adopt compliance",
			VerdictPath:   contracts.VerdictPathSingle,
			RubricVersion: "default",
			PromptVersion: "phase0-stub",
			ResolvedAt:    time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
		})
	}
	output := judges.JudgeOutput{
		Scores:     scores,
		Compliance: compliance,
	}
	return output, output.ValidateFor(input)
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

func eventKinds(events []contracts.StateEntry) []contracts.StateKind {
	kinds := make([]contracts.StateKind, 0, len(events))
	for _, event := range events {
		kinds = append(kinds, event.Kind)
	}
	return kinds
}

type recordingStep struct {
	label    string
	recorder *callRecorder
}

type failOnceStep struct {
	err  error
	seen bool
}

func (s *failOnceStep) Run(ctx context.Context, run *StepRunContext) error {
	_ = ctx
	_ = run
	if !s.seen {
		s.seen = true
		return s.err
	}
	return nil
}

type corruptCleanupStep70 struct{}

func (corruptCleanupStep70) Run(ctx context.Context, run *StepRunContext) error {
	if err := (stubStep70{}).Run(ctx, run); err != nil {
		return err
	}
	if run.TaskPackage == nil || len(run.TaskPackage.Worktrees) == 0 {
		return nil
	}
	run.TaskPackage.Worktrees[0].Path = filepath.Join(filepath.Dir(run.IO.WorktreeBase), "outside-cleanup")
	return nil
}

func (s recordingStep) Run(ctx context.Context, run *StepRunContext) error {
	_ = ctx
	s.recorder.add(s.label)
	switch s.label {
	case "10":
		return stubStep10{}.Run(ctx, run)
	case "30":
		return stubMarkerStep{path: "30/done.marker"}.Run(ctx, run)
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

type nonScorableImplementStep struct {
	kind     contracts.ManifestKind
	exitCode int
	reason   string
	detail   string
}

type cancelingStep struct {
	cancel context.CancelFunc
}

func (s cancelingStep) Run(ctx context.Context, run *StepRunContext) error {
	_ = run
	s.cancel()
	<-ctx.Done()
	return ctx.Err()
}

func (s nonScorableImplementStep) Run(ctx context.Context, run *StepRunContext) error {
	_ = ctx
	manifestPath, err := run.IO.ManifestPath(run.Pass, run.Agent)
	if err != nil {
		return err
	}
	startedAt := time.Now().UTC()
	switch s.kind {
	case contracts.ManifestKindTimeout:
		return internalio.WriteJSONAtomic(manifestPath, contracts.Manifest{
			Kind: contracts.ManifestKindTimeout,
			Value: contracts.ManifestTimeout{
				Kind:           contracts.ManifestKindTimeout,
				SchemaVersion:  "1",
				RunID:          run.IO.RunID,
				Pass:           run.Pass,
				Agent:          run.Agent,
				TimeoutSeconds: 300,
				StartedAt:      startedAt,
				FinishedAt:     startedAt,
			},
		})
	default:
		reason := s.reason
		if reason == "" {
			reason = "unknown"
		}
		detail := s.detail
		if detail == "" {
			detail = "fixture non-scorable manifest"
		}
		return internalio.WriteJSONAtomic(manifestPath, contracts.Manifest{
			Kind: contracts.ManifestKindError,
			Value: contracts.ManifestError{
				Kind:          contracts.ManifestKindError,
				SchemaVersion: "1",
				RunID:         run.IO.RunID,
				Pass:          run.Pass,
				Agent:         run.Agent,
				ExitCode:      s.exitCode,
				Reason:        reason,
				Detail:        detail,
				StartedAt:     startedAt,
				FinishedAt:    startedAt,
			},
		})
	}
}

func nonScorableAgentSteps(kind contracts.ManifestKind) map[contracts.AgentID]Step {
	steps := make(map[contracts.AgentID]Step, len(defaultAgents))
	for _, agent := range defaultAgents {
		steps[agent] = nonScorableImplementStep{kind: kind, exitCode: 1}
	}
	return steps
}

func providerInterruptedAgentSteps(reason string) map[contracts.AgentID]Step {
	steps := make(map[contracts.AgentID]Step, len(defaultAgents))
	for _, agent := range defaultAgents {
		steps[agent] = nonScorableImplementStep{kind: contracts.ManifestKindError, exitCode: 1, reason: reason}
	}
	return steps
}

func noChangeAgentSteps() map[contracts.AgentID]Step {
	steps := make(map[contracts.AgentID]Step, len(defaultAgents))
	for _, agent := range defaultAgents {
		steps[agent] = nonScorableImplementStep{
			kind:     contracts.ManifestKindError,
			exitCode: 0,
			reason:   "unknown",
			detail:   "agent produced no diff",
		}
	}
	return steps
}

type orchestratorStep70 struct {
	git      step70_decide.GitOps
	resolver step70_decide.TargetResolver
}

func (s orchestratorStep70) Run(ctx context.Context, run *StepRunContext) error {
	if err := step70_decide.Run(ctx, run.PR, run.IO, run.TaskPackage, run.Candidates, run.IntentionFile, step70_decide.Deps{
		Git:      s.git,
		Resolver: s.resolver,
		Now: func() time.Time {
			return time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
		},
	}); err != nil {
		return err
	}
	decisionPath, err := run.IO.ResolveRunRelative("70/decision.json")
	if err != nil {
		return err
	}
	if !fileExists(decisionPath) {
		return nil
	}
	decision, err := internalio.ReadJSON[contracts.Decision](decisionPath)
	if err != nil {
		return err
	}
	run.Decision = &decision
	return nil
}

type testStep70Resolver struct{}

func (testStep70Resolver) Resolve(runCtx internalio.RunContext, pkg *contracts.TaskPackage, candidates *contracts.Candidates) (step70_decide.Target, bool, error) {
	_ = pkg
	if candidates == nil || len(candidates.Candidates) == 0 {
		return step70_decide.Target{}, false, nil
	}
	ruleID := "r-bf1d22bf4a85"
	ruleBodyPath, err := runCtx.ResolveRunRelative("staging/rules/" + ruleID + ".md")
	if err != nil {
		return step70_decide.Target{}, false, err
	}
	bodyPath, err := runCtx.ResolveRunRelative(candidates.Candidates[0].ProposedBodyPath)
	if err != nil {
		return step70_decide.Target{}, false, err
	}
	body, err := os.ReadFile(bodyPath)
	if err != nil {
		return step70_decide.Target{}, false, err
	}
	if err := internalio.WriteAtomic(ruleBodyPath, body); err != nil {
		return step70_decide.Target{}, false, err
	}
	return step70_decide.Target{
		BestBranch:    "best",
		BestShaBefore: strings.Repeat("1", 40),
		TargetSHA:     strings.Repeat("2", 40),
		RulesToAppend: []contracts.RuleRegistryEntry{{
			Kind: contracts.RegistryKindAdded,
			Value: contracts.RuleRegistryAdded{
				Kind:          contracts.RegistryKindAdded,
				SchemaVersion: "1",
				RuleID:        ruleID,
				RulePath:      "rules/" + ruleID + ".md",
				Sha256:        candidates.Candidates[0].ProposedBodySha256,
			},
		}},
	}, true, nil
}

type testStep70Git struct {
	head    string
	pushErr error
	state   *testStep70GitState
}

func (g testStep70Git) RemoteHead(context.Context, string) (string, error) {
	if g.state != nil {
		return g.state.head, nil
	}
	return g.head, nil
}

func (g testStep70Git) PushForceWithLease(context.Context, string, string, string) error {
	if g.state != nil && g.state.onPush != nil {
		g.state.onPush(g.state)
	}
	return g.pushErr
}

func (g testStep70Git) RemoveWorktree(context.Context, string) error {
	return nil
}

type testStep70GitState struct {
	head   string
	onPush func(*testStep70GitState)
}
