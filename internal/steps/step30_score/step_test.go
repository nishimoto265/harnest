package step30_score

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/judges"
	"github.com/nishimoto265/auto-improve/internal/steps/scorecore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStep30Score_RunAndResume(t *testing.T) {
	runCtx, pkg := seedStep30Fixtures(t, []contracts.AgentID{"a1", "a2", "a3"})

	step := New()
	err := step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg})
	require.NoError(t, err)

	markerPath, err := runCtx.ResolveRunRelative("30/done.marker")
	require.NoError(t, err)
	require.FileExists(t, markerPath)

	marker, err := internalio.ReadJSON[contracts.Step30DoneMarker](markerPath)
	require.NoError(t, err)
	assert.Equal(t, int64(15), marker.ExpectedCounts.Scores)    // 3 agents × 5 dims
	assert.Equal(t, int64(3), marker.ExpectedCounts.Compliance) // 3 agents × 1 stub rule
	assert.Len(t, marker.CompletedAgents, 3)

	scoreFinalPath, err := runCtx.ResolveRunRelative("30/scores-A.jsonl")
	require.NoError(t, err)
	scores, err := internalio.ReadJSONL[contracts.ScoreEntry](scoreFinalPath)
	require.NoError(t, err)
	assert.Len(t, scores, 15)

	// Resume: running again with a valid marker must be a no-op (file sizes unchanged).
	info1, err := os.Stat(scoreFinalPath)
	require.NoError(t, err)
	err = step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg})
	require.NoError(t, err)
	info2, err := os.Stat(scoreFinalPath)
	require.NoError(t, err)
	assert.Equal(t, info1.Size(), info2.Size(), "resume path must not re-append rows")

	// Invalidate the marker: a corrupt marker should be replaced, not error.
	require.NoError(t, os.WriteFile(markerPath, []byte("stub\n"), 0o644))
	err = step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg})
	require.NoError(t, err)
	rebuilt, err := internalio.ReadJSON[contracts.Step30DoneMarker](markerPath)
	require.NoError(t, err)
	assert.Equal(t, int64(15), rebuilt.ExpectedCounts.Scores)
}

func TestStep30Score_RejectsTaskPackageRunIDMismatch(t *testing.T) {
	runCtx, pkg := seedStep30Fixtures(t, []contracts.AgentID{"a1", "a2", "a3"})
	pkg.RunID = "2026-04-22-PR99-deadbee"

	err := New().Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg})
	require.ErrorContains(t, err, "task package run_id mismatch")
}

func TestStep30Score_SkipsUnscorableAgents(t *testing.T) {
	// TaskPackage requires exactly 6 worktrees (3 agents × 2 passes). Seed all
	// 3 agents but only write manifests for a1 / a2 — a3's missing manifest
	// must cause step30 to skip that agent.
	runCtx, pkg := seedStep30Fixtures(t, []contracts.AgentID{"a1", "a2", "a3"})
	a3ManifestPath, err := runCtx.ManifestPath(1, contracts.AgentID("a3"))
	require.NoError(t, err)
	require.NoError(t, os.Remove(a3ManifestPath))

	step := New()
	err = step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg})
	require.NoError(t, err)

	markerPath, err := runCtx.ResolveRunRelative("30/done.marker")
	require.NoError(t, err)
	marker, err := internalio.ReadJSON[contracts.Step30DoneMarker](markerPath)
	require.NoError(t, err)
	assert.Equal(t, int64(10), marker.ExpectedCounts.Scores) // only a1 + a2
	assert.Len(t, marker.CompletedAgents, 2)
}

func TestStep30Score_AllowsMultipleComplianceRowsPerAgent(t *testing.T) {
	runCtx, pkg := seedStep30Fixtures(t, []contracts.AgentID{"a1", "a2", "a3"})
	provider := &fakePanelProvider{
		outputs: func(input judges.JudgeInput, role contracts.JudgeRole) judges.JudgeOutput {
			score := 80
			if role == contracts.JudgeRoleSecondary {
				score = 79
			}
			return makeJudgeOutput(input, role, score, []ruleVerdict{
				{ruleID: "rule-a", verdict: contracts.ComplianceVerdictCompliant},
				{ruleID: "rule-b", verdict: contracts.ComplianceVerdictViolated},
			})
		},
	}

	step := New(WithPanelProvider(provider))
	err := step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg})
	require.NoError(t, err)

	markerPath, err := runCtx.ResolveRunRelative("30/done.marker")
	require.NoError(t, err)
	marker, err := internalio.ReadJSON[contracts.Step30DoneMarker](markerPath)
	require.NoError(t, err)
	assert.Equal(t, int64(15), marker.ExpectedCounts.Scores)
	assert.Equal(t, int64(6), marker.ExpectedCounts.Compliance)
}

func TestStep30Score_ResumeWithoutMarkerDoesNotRejudgeOrAppend(t *testing.T) {
	runCtx, pkg := seedStep30Fixtures(t, []contracts.AgentID{"a1", "a2", "a3"})
	provider := &fakePanelProvider{
		outputs: func(input judges.JudgeInput, role contracts.JudgeRole) judges.JudgeOutput {
			score := 80
			switch role {
			case contracts.JudgeRoleSecondary:
				score = 79
			case contracts.JudgeRoleArbiter:
				score = 78
			}
			return makeJudgeOutput(input, role, score, []ruleVerdict{
				{ruleID: "rule-a", verdict: contracts.ComplianceVerdictCompliant},
			})
		},
	}

	step := New(WithPanelProvider(provider))
	require.NoError(t, step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg}))

	markerPath, err := runCtx.ResolveRunRelative("30/done.marker")
	require.NoError(t, err)
	require.NoError(t, os.Remove(markerPath))

	before := statStep30Files(t, runCtx)
	provider.reset()

	require.NoError(t, step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg}))

	after := statStep30Files(t, runCtx)
	assert.Equal(t, before, after)
	assert.Zero(t, provider.calls[contracts.JudgeRolePrimary])
	assert.Zero(t, provider.calls[contracts.JudgeRoleSecondary])
	assert.Zero(t, provider.calls[contracts.JudgeRoleArbiter])
}

func TestStep30Score_ResumeRerunsRolesWhenOutputSHAChanges(t *testing.T) {
	runCtx, pkg := seedStep30Fixtures(t, []contracts.AgentID{"a1", "a2", "a3"})
	provider := &fakePanelProvider{
		outputs: func(input judges.JudgeInput, role contracts.JudgeRole) judges.JudgeOutput {
			score := 80
			switch role {
			case contracts.JudgeRoleSecondary:
				score = 60
			case contracts.JudgeRoleArbiter:
				score = 75
			}
			return makeJudgeOutput(input, role, score, []ruleVerdict{
				{ruleID: "rule-a", verdict: contracts.ComplianceVerdictCompliant},
			})
		},
	}

	step := New(WithPanelProvider(provider))
	require.NoError(t, step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg}))

	manifest, err := internalio.LoadScorableManifest(runCtx, 1, contracts.AgentID("a1"))
	require.NoError(t, err)
	diffAbs, err := runCtx.ResolveRunRelative(manifest.DiffPath)
	require.NoError(t, err)

	originalSHA, err := fileSha256(diffAbs)
	require.NoError(t, err)

	markerPath, err := runCtx.ResolveRunRelative("30/done.marker")
	require.NoError(t, err)
	require.NoError(t, os.Remove(markerPath))

	writeRel(t, runCtx, manifest.DiffPath, "updated fixture diff for a1\n")
	updatedSHA, err := fileSha256(diffAbs)
	require.NoError(t, err)
	require.NotEqual(t, originalSHA, updatedSHA)

	provider.reset()
	require.NoError(t, step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg}))

	assert.Equal(t, 1, provider.calls[contracts.JudgeRolePrimary])
	assert.Equal(t, 1, provider.calls[contracts.JudgeRoleSecondary])
	assert.Equal(t, 1, provider.calls[contracts.JudgeRoleArbiter])

	scoreRawPath, err := runCtx.ResolveRunRelative("30/scores-A-raw.jsonl")
	require.NoError(t, err)
	scoreRaw, err := internalio.ReadJSONL[contracts.RawScoreEntry](scoreRawPath)
	require.NoError(t, err)
	assert.Len(t, scoreRaw, 60)
	assert.Equal(t, 15, countRawScoresForAgentAndSHA(scoreRaw, contracts.AgentID("a1"), originalSHA))
	assert.Equal(t, 15, countRawScoresForAgentAndSHA(scoreRaw, contracts.AgentID("a1"), updatedSHA))

	complianceRawPath, err := runCtx.ResolveRunRelative("30/compliance-A-raw.jsonl")
	require.NoError(t, err)
	complianceRaw, err := internalio.ReadJSONL[contracts.RawComplianceEntry](complianceRawPath)
	require.NoError(t, err)
	assert.Len(t, complianceRaw, 12)
	assert.Equal(t, 3, countRawComplianceForAgentAndSHA(complianceRaw, contracts.AgentID("a1"), originalSHA))
	assert.Equal(t, 3, countRawComplianceForAgentAndSHA(complianceRaw, contracts.AgentID("a1"), updatedSHA))
}

func TestStep30Score_ResumeRerunsWhenPromptVersionChanges(t *testing.T) {
	runCtx, pkg := seedStep30Fixtures(t, []contracts.AgentID{"a1", "a2", "a3"})
	provider := &fakePanelProvider{
		outputs: func(input judges.JudgeInput, role contracts.JudgeRole) judges.JudgeOutput {
			score := 80
			if role == contracts.JudgeRoleSecondary {
				score = 79
			}
			return makeJudgeOutput(input, role, score, []ruleVerdict{{ruleID: "rule-a", verdict: contracts.ComplianceVerdictCompliant}})
		},
	}

	first := New(WithPanelProvider(provider), WithPromptVersion("prompt-v1"))
	require.NoError(t, first.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg}))

	provider.reset()
	second := New(WithPanelProvider(provider), WithPromptVersion("prompt-v2"))
	require.NoError(t, second.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg}))

	assert.Equal(t, 3, provider.calls[contracts.JudgeRolePrimary])
	assert.Equal(t, 3, provider.calls[contracts.JudgeRoleSecondary])
}

func TestStep30Score_ResolveRubricPathRejectsSymlink(t *testing.T) {
	runCtx, _ := seedStep30Fixtures(t, []contracts.AgentID{"a1", "a2", "a3"})
	step := New()

	escapePath := filepath.Join(t.TempDir(), "escape.md")
	require.NoError(t, os.WriteFile(escapePath, []byte("escape\n"), 0o644))
	rubricPath := filepath.Join(runCtx.RunsBase, ".rubrics", "default.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(rubricPath), 0o755))
	require.NoError(t, os.Symlink(escapePath, rubricPath))

	_, err := step.resolveRubricPath(runCtx)
	require.ErrorContains(t, err, "must not be a symlink")
}

func TestStep30Score_FailsClosedOnMalformedManifest(t *testing.T) {
	runCtx, pkg := seedStep30Fixtures(t, []contracts.AgentID{"a1", "a2", "a3"})
	manifestPath, err := runCtx.ManifestPath(1, "a1")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(manifestPath, []byte(`{"kind":"success","kind":"error"}`+"\n"), 0o644))

	err = New().Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg})
	require.Error(t, err)
	assert.ErrorIs(t, err, contracts.ErrDuplicateJSONKey)
}

func TestStep30Score_ResumeRerunsWhenRawComplianceIsMissing(t *testing.T) {
	runCtx, pkg := seedStep30Fixtures(t, []contracts.AgentID{"a1", "a2", "a3"})
	provider := &fakePanelProvider{
		outputs: func(input judges.JudgeInput, role contracts.JudgeRole) judges.JudgeOutput {
			score := 80
			if role == contracts.JudgeRoleSecondary {
				score = 79
			}
			return makeJudgeOutput(input, role, score, []ruleVerdict{{ruleID: "rule-a", verdict: contracts.ComplianceVerdictCompliant}})
		},
	}

	step := New(WithPanelProvider(provider))
	require.NoError(t, step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg}))

	complianceRawPath, err := runCtx.ResolveRunRelative("30/compliance-A-raw.jsonl")
	require.NoError(t, err)
	require.NoError(t, os.Remove(complianceRawPath))

	provider.reset()
	require.NoError(t, step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg}))

	assert.Equal(t, 3, provider.calls[contracts.JudgeRolePrimary])
	assert.Equal(t, 3, provider.calls[contracts.JudgeRoleSecondary])
}

func TestStep30Score_AllowsEmptyComplianceAcrossPanel(t *testing.T) {
	runCtx, pkg := seedStep30Fixtures(t, []contracts.AgentID{"a1", "a2", "a3"})
	provider := &fakePanelProvider{
		outputs: func(input judges.JudgeInput, role contracts.JudgeRole) judges.JudgeOutput {
			score := 80
			switch role {
			case contracts.JudgeRoleSecondary:
				score = 60
			case contracts.JudgeRoleArbiter:
				score = 75
			}
			return makeJudgeOutput(input, role, score, nil)
		},
	}

	step := New(WithPanelProvider(provider))
	require.NoError(t, step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg}))

	markerPath, err := runCtx.ResolveRunRelative("30/done.marker")
	require.NoError(t, err)
	marker, err := internalio.ReadJSON[contracts.Step30DoneMarker](markerPath)
	require.NoError(t, err)
	assert.Equal(t, int64(15), marker.ExpectedCounts.Scores)
	assert.Equal(t, int64(0), marker.ExpectedCounts.Compliance)
	assert.Equal(t, 3, provider.calls[contracts.JudgeRoleArbiter])

	complianceFinalPath, err := runCtx.ResolveRunRelative("30/compliance-A.jsonl")
	require.NoError(t, err)
	complianceFinal, err := internalio.ReadJSONL[contracts.ComplianceEntry](complianceFinalPath)
	require.NoError(t, err)
	assert.Empty(t, complianceFinal)
}

func TestStep30Score_DoesNotWriteDoneMarkerOnIncompleteArbiterComplianceCoverage(t *testing.T) {
	runCtx, pkg := seedStep30Fixtures(t, []contracts.AgentID{"a1", "a2", "a3"})
	provider := &fakePanelProvider{
		outputs: func(input judges.JudgeInput, role contracts.JudgeRole) judges.JudgeOutput {
			score := 80
			rules := []ruleVerdict{
				{ruleID: "rule-a", verdict: contracts.ComplianceVerdictCompliant},
				{ruleID: "rule-b", verdict: contracts.ComplianceVerdictViolated},
			}
			switch role {
			case contracts.JudgeRoleSecondary:
				score = 60
			case contracts.JudgeRoleArbiter:
				score = 75
				rules = []ruleVerdict{
					{ruleID: "rule-a", verdict: contracts.ComplianceVerdictCompliant},
				}
			}
			return makeJudgeOutput(input, role, score, rules)
		},
	}

	step := New(WithPanelProvider(provider))
	err := step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg})
	require.ErrorIs(t, err, scorecore.ErrPanelArbiterRuleCoverage)

	markerPath, markerErr := runCtx.ResolveRunRelative("30/done.marker")
	require.NoError(t, markerErr)
	_, statErr := os.Stat(markerPath)
	require.Error(t, statErr)
	assert.True(t, os.IsNotExist(statErr))
}

func TestStep30Score_PreservesComplianceHistoryAfterRuleShrink(t *testing.T) {
	runCtx, pkg := seedStep30Fixtures(t, []contracts.AgentID{"a1", "a2", "a3"})
	currentVerdicts := []ruleVerdict{
		{ruleID: "rule-a", verdict: contracts.ComplianceVerdictCompliant},
		{ruleID: "rule-b", verdict: contracts.ComplianceVerdictViolated},
	}
	provider := &fakePanelProvider{
		outputs: func(input judges.JudgeInput, role contracts.JudgeRole) judges.JudgeOutput {
			score := 80
			if role == contracts.JudgeRoleSecondary {
				score = 79
			}
			verdicts := append([]ruleVerdict(nil), currentVerdicts...)
			return makeJudgeOutput(input, role, score, verdicts)
		},
	}

	step := New(WithPanelProvider(provider))
	require.NoError(t, step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg}))

	markerPath, err := runCtx.ResolveRunRelative("30/done.marker")
	require.NoError(t, err)
	require.NoError(t, os.Remove(markerPath))

	currentVerdicts = []ruleVerdict{
		{ruleID: "rule-a", verdict: contracts.ComplianceVerdictCompliant},
	}
	for _, agent := range []contracts.AgentID{"a1", "a2", "a3"} {
		manifest, manifestErr := internalio.LoadScorableManifest(runCtx, 1, agent)
		require.NoError(t, manifestErr)
		writeRel(t, runCtx, manifest.DiffPath, "updated fixture diff for "+string(agent)+"\n")
	}

	require.NoError(t, step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg}))

	complianceFinalPath, err := runCtx.ResolveRunRelative("30/compliance-A.jsonl")
	require.NoError(t, err)
	complianceFinal, err := internalio.ReadJSONL[contracts.ComplianceEntry](complianceFinalPath)
	require.NoError(t, err)
	require.Len(t, complianceFinal, 6)
	collapsed := scorecore.CollapseFinalCompliance(complianceFinal)
	require.Len(t, collapsed, 6)

	ruleCounts := map[string]int{}
	for _, row := range complianceFinal {
		ruleCounts[row.RuleID]++
	}
	assert.Equal(t, map[string]int{"rule-a": 3, "rule-b": 3}, ruleCounts)

	marker, err := internalio.ReadJSON[contracts.Step30DoneMarker](markerPath)
	require.NoError(t, err)
	assert.Equal(t, int64(6), marker.ExpectedCounts.Compliance)
}

func TestStep30Score_RunSerializesConcurrentWriters(t *testing.T) {
	runCtx, pkg := seedStep30Fixtures(t, []contracts.AgentID{"a1", "a2", "a3"})
	primaryStarted := make(chan struct{})
	releasePrimary := make(chan struct{})
	var blockOnce sync.Once

	provider := &fakePanelProvider{
		outputs: func(input judges.JudgeInput, role contracts.JudgeRole) judges.JudgeOutput {
			if role == contracts.JudgeRolePrimary {
				blockOnce.Do(func() {
					close(primaryStarted)
					<-releasePrimary
				})
			}

			score := 80
			if role == contracts.JudgeRoleSecondary {
				score = 79
			}
			return makeJudgeOutput(input, role, score, []ruleVerdict{
				{ruleID: "rule-a", verdict: contracts.ComplianceVerdictCompliant},
			})
		},
	}

	step := New(WithPanelProvider(provider))
	errCh := make(chan error, 2)

	go func() {
		errCh <- step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg})
	}()

	<-primaryStarted

	go func() {
		errCh <- step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg})
	}()

	select {
	case err := <-errCh:
		t.Fatalf("Run returned before lock release: %v", err)
	case <-time.After(150 * time.Millisecond):
	}

	assert.Equal(t, 1, provider.callCount(contracts.JudgeRolePrimary))

	close(releasePrimary)

	require.NoError(t, <-errCh)
	require.NoError(t, <-errCh)

	assert.Equal(t, 3, provider.callCount(contracts.JudgeRolePrimary))
	assert.Equal(t, 3, provider.callCount(contracts.JudgeRoleSecondary))
	assert.Equal(t, 0, provider.callCount(contracts.JudgeRoleArbiter))

	scoreRawPath, err := runCtx.ResolveRunRelative("30/scores-A-raw.jsonl")
	require.NoError(t, err)
	scoreRaw, err := internalio.ReadJSONL[contracts.RawScoreEntry](scoreRawPath)
	require.NoError(t, err)
	assert.Len(t, scoreRaw, 30)
}

// seedStep30Fixtures creates a minimal RunContext + TaskPackage with pass-1
// manifests for every agent in `agents`. Pass-2 worktrees are included in the
// package but without manifests so step30 ignores them.
func seedStep30Fixtures(t *testing.T, agents []contracts.AgentID) (internalio.RunContext, contracts.TaskPackage) {
	t.Helper()
	runsBase := t.TempDir()
	worktreeBase := t.TempDir()
	runID := internalio.NewRunID(99)
	base := strings.Repeat("a", 40)

	pkg := contracts.TaskPackage{
		SchemaVersion:           "1",
		RunID:                   runID,
		PR:                      99,
		Title:                   "step30 fixture",
		BaseSHA:                 base,
		BestBranch:              "best",
		ReconstructedTaskPrompt: "step30 test",
		CreatedAt:               time.Now().UTC(),
	}
	for _, agent := range agents {
		for pass := 1; pass <= 2; pass++ {
			pkg.Worktrees = append(pkg.Worktrees, contracts.WorktreeAllocation{
				Agent:   agent,
				Pass:    pass,
				Path:    filepath.Join(worktreeBase, string(runID)+"-pass"+itoa(pass)+"-"+string(agent)),
				Branch:  "stub/" + string(agent) + "/pass" + itoa(pass),
				BaseSHA: base,
				HeadSHA: base,
			})
		}
	}

	runCtx, err := internalio.RunContextFromTaskPackage(pkg, runsBase, worktreeBase)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(runCtx.RunDir(), 0o755))
	require.NoError(t, internalio.WriteJSONAtomic(runCtx.TaskPackagePath(), pkg))

	// Seed pass-1 manifests only so step30 treats `agents` as scorable.
	for _, agent := range agents {
		prefix := filepath.Join("20-pass1", string(agent))
		diffRel := filepath.Join(prefix, "diff.patch")
		sessionRel := filepath.Join(prefix, "session.jsonl")
		checklistRel := filepath.Join(prefix, "checklist-result.json")
		writeRel(t, runCtx, diffRel, "fixture diff for "+string(agent)+"\n")
		writeRel(t, runCtx, sessionRel, "")
		writeRel(t, runCtx, checklistRel, "{}\n")
		manifest := contracts.Manifest{
			Kind: contracts.ManifestKindSuccess,
			Value: contracts.ManifestSuccess{
				Kind:          contracts.ManifestKindSuccess,
				SchemaVersion: "1",
				RunID:         runID,
				Pass:          1,
				Agent:         agent,
				BranchName:    "stub/" + string(agent) + "/pass1",
				HeadSHA:       base,
				BaseSHA:       base,
				DiffPath:      diffRel,
				SessionPath:   sessionRel,
				ChecklistPath: checklistRel,
				PromptVersion: "stub",
				StartedAt:     time.Now().UTC(),
				FinishedAt:    time.Now().UTC(),
			},
		}
		manifestPath, err := runCtx.ManifestPath(1, agent)
		require.NoError(t, err)
		require.NoError(t, internalio.WriteJSONAtomic(manifestPath, manifest))
	}
	return runCtx, pkg
}

func writeRel(t *testing.T, runCtx internalio.RunContext, rel, content string) {
	t.Helper()
	abs, err := runCtx.ResolveRunRelative(rel)
	require.NoError(t, err)
	require.NoError(t, internalio.WriteAtomic(abs, []byte(content)))
}

func itoa(i int) string {
	switch i {
	case 1:
		return "1"
	case 2:
		return "2"
	default:
		return "x"
	}
}

type fakePanelProvider struct {
	outputs func(input judges.JudgeInput, role contracts.JudgeRole) judges.JudgeOutput
	mu      sync.Mutex
	calls   map[contracts.JudgeRole]int
}

func (p *fakePanelProvider) Judges(input judges.JudgeInput) (judges.Judge, judges.Judge, judges.Judge, error) {
	p.mu.Lock()
	if p.calls == nil {
		p.calls = make(map[contracts.JudgeRole]int)
	}
	p.mu.Unlock()
	return fakePanelJudge{provider: p, input: input, role: contracts.JudgeRolePrimary},
		fakePanelJudge{provider: p, input: input, role: contracts.JudgeRoleSecondary},
		fakePanelJudge{provider: p, input: input, role: contracts.JudgeRoleArbiter},
		nil
}

func (p *fakePanelProvider) reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	clear(p.calls)
}

func (p *fakePanelProvider) callCount(role contracts.JudgeRole) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[role]
}

type fakePanelJudge struct {
	provider *fakePanelProvider
	input    judges.JudgeInput
	role     contracts.JudgeRole
}

func (j fakePanelJudge) ScoreOutput(_ context.Context, _ judges.JudgeInput) (judges.JudgeOutput, error) {
	j.provider.mu.Lock()
	j.provider.calls[j.role]++
	j.provider.mu.Unlock()
	return j.provider.outputs(j.input, j.role), nil
}

type ruleVerdict struct {
	ruleID  string
	verdict contracts.ComplianceVerdict
}

func makeJudgeOutput(input judges.JudgeInput, role contracts.JudgeRole, score int, verdicts []ruleVerdict) judges.JudgeOutput {
	verdictPath := contracts.VerdictPathSingle
	if role == contracts.JudgeRoleArbiter {
		verdictPath = contracts.VerdictPathArbitrated
	}

	scores := make([]contracts.ScoreEntry, 0, 5)
	for _, dimension := range []contracts.Dimension{
		contracts.DimensionFidelity,
		contracts.DimensionCorrectness,
		contracts.DimensionMaintainability,
		contracts.DimensionDiscipline,
		contracts.DimensionCommunication,
	} {
		scores = append(scores, contracts.ScoreEntry{
			SchemaVersion: "1",
			RunID:         input.RunID,
			Pass:          input.Pass,
			Agent:         input.Agent,
			Dimension:     dimension,
			Score:         score,
			Reasons:       "fixture",
			VerdictPath:   verdictPath,
			RubricVersion: "default",
			PromptVersion: "phase0-stub",
			ResolvedAt:    time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
		})
	}

	compliance := make([]contracts.ComplianceEntry, 0, len(verdicts))
	for _, verdict := range verdicts {
		compliance = append(compliance, contracts.ComplianceEntry{
			SchemaVersion: "1",
			RunID:         input.RunID,
			Pass:          input.Pass,
			Agent:         input.Agent,
			RuleID:        verdict.ruleID,
			Verdict:       verdict.verdict,
			Rationale:     "fixture",
			VerdictPath:   verdictPath,
			RubricVersion: "default",
			PromptVersion: "phase0-stub",
			ResolvedAt:    time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
		})
	}

	return judges.JudgeOutput{
		Scores:     scores,
		Compliance: compliance,
		Arbiter:    role == contracts.JudgeRoleArbiter,
	}
}

func statStep30Files(t *testing.T, runCtx internalio.RunContext) map[string]int64 {
	t.Helper()

	files := []string{
		"30/scores-A-raw.jsonl",
		"30/compliance-A-raw.jsonl",
		"30/scores-A.jsonl",
		"30/compliance-A.jsonl",
	}
	out := make(map[string]int64, len(files))
	for _, rel := range files {
		abs, err := runCtx.ResolveRunRelative(rel)
		require.NoError(t, err)
		info, err := os.Stat(abs)
		require.NoError(t, err)
		out[rel] = info.Size()
	}
	return out
}

func countRawScoresForAgentAndSHA(rows []contracts.RawScoreEntry, agent contracts.AgentID, sha string) int {
	count := 0
	for _, row := range rows {
		if row.Agent == agent && row.OutputSha256 == sha {
			count++
		}
	}
	return count
}

func countRawComplianceForAgentAndSHA(rows []contracts.RawComplianceEntry, agent contracts.AgentID, sha string) int {
	count := 0
	for _, row := range rows {
		if row.Agent == agent && row.OutputSha256 == sha {
			count++
		}
	}
	return count
}
