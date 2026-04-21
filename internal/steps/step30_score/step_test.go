package step30_score

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/judges"
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

func TestStep30Score_ResumeWithoutMarkerIsNoOp(t *testing.T) {
	runCtx, pkg := seedStep30Fixtures(t, []contracts.AgentID{"a1", "a2", "a3"})
	calls := &judgeCallCounter{}
	step := New(WithPanelProvider(newCountingPanelProvider(calls, 1)))

	require.NoError(t, step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg}))
	assert.Equal(t, 3, calls.primary)
	assert.Equal(t, 3, calls.secondary)
	assert.Equal(t, 0, calls.arbiter)

	scoreRawPath, err := runCtx.ResolveRunRelative("30/scores-A-raw.jsonl")
	require.NoError(t, err)
	scoreFinalPath, err := runCtx.ResolveRunRelative("30/scores-A.jsonl")
	require.NoError(t, err)
	complianceRawPath, err := runCtx.ResolveRunRelative("30/compliance-A-raw.jsonl")
	require.NoError(t, err)
	complianceFinalPath, err := runCtx.ResolveRunRelative("30/compliance-A.jsonl")
	require.NoError(t, err)
	markerPath, err := runCtx.ResolveRunRelative("30/done.marker")
	require.NoError(t, err)

	before := map[string]int64{}
	for _, path := range []string{scoreRawPath, scoreFinalPath, complianceRawPath, complianceFinalPath} {
		info, statErr := os.Stat(path)
		require.NoError(t, statErr)
		before[path] = info.Size()
	}

	require.NoError(t, os.Remove(markerPath))
	calls.reset()
	require.NoError(t, step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg}))
	assert.Equal(t, 0, calls.primary)
	assert.Equal(t, 0, calls.secondary)
	assert.Equal(t, 0, calls.arbiter)

	for _, path := range []string{scoreRawPath, scoreFinalPath, complianceRawPath, complianceFinalPath} {
		info, statErr := os.Stat(path)
		require.NoError(t, statErr)
		assert.Equal(t, before[path], info.Size(), "path=%s", path)
	}
}

func TestStep30Score_AllowsMultipleComplianceRulesPerAgent(t *testing.T) {
	runCtx, pkg := seedStep30Fixtures(t, []contracts.AgentID{"a1", "a2", "a3"})
	step := New(WithPanelProvider(newCountingPanelProvider(&judgeCallCounter{}, 2)))

	require.NoError(t, step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg}))

	markerPath, err := runCtx.ResolveRunRelative("30/done.marker")
	require.NoError(t, err)
	marker, err := internalio.ReadJSON[contracts.Step30DoneMarker](markerPath)
	require.NoError(t, err)
	assert.Equal(t, int64(15), marker.ExpectedCounts.Scores)
	assert.Equal(t, int64(6), marker.ExpectedCounts.Compliance)

	compliancePath, err := runCtx.ResolveRunRelative("30/compliance-A.jsonl")
	require.NoError(t, err)
	rows, err := internalio.ReadJSONL[contracts.ComplianceEntry](compliancePath)
	require.NoError(t, err)
	assert.Len(t, rows, 6)
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

type judgeCallCounter struct {
	primary   int
	secondary int
	arbiter   int
}

func (c *judgeCallCounter) reset() {
	c.primary = 0
	c.secondary = 0
	c.arbiter = 0
}

type countingJudge struct {
	role            contracts.JudgeRole
	complianceRules int
	calls           *judgeCallCounter
}

func (j countingJudge) ScoreOutput(_ context.Context, input judges.JudgeInput) (judges.JudgeOutput, error) {
	switch j.role {
	case contracts.JudgeRolePrimary:
		j.calls.primary++
	case contracts.JudgeRoleSecondary:
		j.calls.secondary++
	case contracts.JudgeRoleArbiter:
		j.calls.arbiter++
	}

	verdictPath := contracts.VerdictPathAgreement
	scoreBase := 80
	switch j.role {
	case contracts.JudgeRolePrimary:
		verdictPath = contracts.VerdictPathSingle
	case contracts.JudgeRoleSecondary:
		scoreBase = 79
	case contracts.JudgeRoleArbiter:
		verdictPath = contracts.VerdictPathArbitrated
		scoreBase = 78
	}

	scores := make([]contracts.ScoreEntry, 0, len(step30Dimensions))
	for idx, dimension := range step30Dimensions {
		scores = append(scores, contracts.ScoreEntry{
			SchemaVersion: "1",
			RunID:         input.RunID,
			Pass:          input.Pass,
			Agent:         input.Agent,
			Dimension:     dimension,
			Score:         scoreBase + idx,
			Reasons:       string(j.role) + " reasons",
			VerdictPath:   verdictPath,
			RubricVersion: "default",
			PromptVersion: "phase0-stub",
			ResolvedAt:    time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
		})
	}

	compliance := make([]contracts.ComplianceEntry, 0, j.complianceRules)
	for i := 0; i < j.complianceRules; i++ {
		compliance = append(compliance, contracts.ComplianceEntry{
			SchemaVersion: "1",
			RunID:         input.RunID,
			Pass:          input.Pass,
			Agent:         input.Agent,
			RuleID:        "rule-" + string(rune('a'+i)),
			Verdict:       contracts.ComplianceVerdictCompliant,
			Rationale:     string(j.role) + " rationale",
			VerdictPath:   verdictPath,
			RubricVersion: "default",
			PromptVersion: "phase0-stub",
			ResolvedAt:    time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
		})
	}

	return judges.JudgeOutput{
		Scores:     scores,
		Compliance: compliance,
		Arbiter:    j.role == contracts.JudgeRoleArbiter,
	}, nil
}

func newCountingPanelProvider(calls *judgeCallCounter, complianceRules int) PanelProvider {
	return FuncPanelProvider(func(input judges.JudgeInput) (judges.Judge, judges.Judge, judges.Judge, error) {
		return countingJudge{role: contracts.JudgeRolePrimary, complianceRules: complianceRules, calls: calls},
			countingJudge{role: contracts.JudgeRoleSecondary, complianceRules: complianceRules, calls: calls},
			countingJudge{role: contracts.JudgeRoleArbiter, complianceRules: complianceRules, calls: calls},
			nil
	})
}
