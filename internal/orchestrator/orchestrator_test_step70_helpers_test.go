package orchestrator

import (
	"context"
	"fmt"
	"github.com/nishimoto265/auto-improve/internal/agents"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/state"
	"github.com/nishimoto265/auto-improve/internal/steps/agentrunner"
	"github.com/nishimoto265/auto-improve/internal/steps/step70_decide"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type policySnapshotStaleStep struct{}

func (policySnapshotStaleStep) Run(context.Context, *StepRunContext) error {
	return &step70_decide.PolicySnapshotStaleError{Reason: "policy_branch_stale"}
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
