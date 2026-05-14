package step30_score

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/nishimoto265/harnest/internal/judges"
	"github.com/stretchr/testify/require"
)

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

func setStepRubric(t *testing.T, step *Step, ruleIDs ...string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("# test rubric\n")
	if len(ruleIDs) > 0 {
		b.WriteString("\n## Active Rule IDs\n")
		for _, ruleID := range ruleIDs {
			b.WriteString("- ")
			b.WriteString(ruleID)
			b.WriteByte('\n')
		}
	}
	rubricPath := filepath.Join(t.TempDir(), "rubric.md")
	require.NoError(t, os.WriteFile(rubricPath, []byte(b.String()), 0o644))
	step.rubricPathFn = func(internalio.RunContext) (string, error) {
		return rubricPath, nil
	}
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

func (p *fakePanelProvider) Judge(input judges.JudgeInput) (judges.Judge, error) {
	p.mu.Lock()
	if p.calls == nil {
		p.calls = make(map[contracts.JudgeRole]int)
	}
	p.mu.Unlock()
	return fakePanelJudge{provider: p, role: contracts.JudgeRolePrimary}, nil
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
	role     contracts.JudgeRole
}

func (j fakePanelJudge) ScoreOutput(_ context.Context, input judges.JudgeInput) (judges.JudgeOutput, error) {
	j.provider.mu.Lock()
	j.provider.calls[j.role]++
	j.provider.mu.Unlock()
	return j.provider.outputs(input, j.role), nil
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
	if input.EnforceExpectedCompliance && len(input.ExpectedComplianceRuleIDs) == 0 {
		verdicts = nil
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

func finalComplianceRuleCounts(rows []contracts.ComplianceEntry) map[string]int {
	counts := make(map[string]int)
	for _, row := range rows {
		counts[row.RuleID]++
	}
	return counts
}

func rawComplianceRuleCounts(rows []contracts.RawComplianceEntry) map[string]int {
	counts := make(map[string]int)
	for _, row := range rows {
		counts[row.RuleID]++
	}
	return counts
}

func finalScoreAgents(rows []contracts.ScoreEntry) map[contracts.AgentID]struct{} {
	agents := make(map[contracts.AgentID]struct{})
	for _, row := range rows {
		agents[row.Agent] = struct{}{}
	}
	return agents
}

func finalComplianceAgents(rows []contracts.ComplianceEntry) map[contracts.AgentID]struct{} {
	agents := make(map[contracts.AgentID]struct{})
	for _, row := range rows {
		agents[row.Agent] = struct{}{}
	}
	return agents
}

func rawScoreAgents(rows []contracts.RawScoreEntry) map[contracts.AgentID]struct{} {
	agents := make(map[contracts.AgentID]struct{})
	for _, row := range rows {
		agents[row.Agent] = struct{}{}
	}
	return agents
}
