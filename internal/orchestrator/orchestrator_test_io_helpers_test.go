package orchestrator

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/state"
	"github.com/nishimoto265/auto-improve/internal/steps/scorecore"
	"github.com/stretchr/testify/require"
)

func sha256String(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func stubTaskPackageForRun(runCtx internalio.RunContext, pr int) contracts.TaskPackage {
	worktrees := make([]contracts.WorktreeAllocation, 0, len(defaultAgents)*2)
	for pass := 1; pass <= 2; pass++ {
		for _, agent := range defaultAgents {
			worktrees = append(worktrees, contracts.WorktreeAllocation{
				Agent:   agent,
				Pass:    pass,
				Path:    filepath.Join(runCtx.WorktreeBase, fmt.Sprintf("%s-pass%d-%s", runCtx.RunID, pass, agent)),
				Branch:  fmt.Sprintf("stub/%s/pass%d/%s", runCtx.RunID, pass, agent),
				BaseSHA: strings.Repeat("a", 40),
				HeadSHA: strings.Repeat("a", 40),
			})
		}
	}
	return contracts.TaskPackage{
		SchemaVersion:           "1",
		RunID:                   runCtx.RunID,
		PR:                      pr,
		Title:                   "stub task",
		BaseSHA:                 strings.Repeat("a", 40),
		BestBranch:              "best",
		ReconstructedTaskPrompt: "stub prompt",
		Worktrees:               worktrees,
		CreatedAt:               time.Now().UTC(),
	}
}

func appendJSONLForTest(runCtx internalio.RunContext, rel string, record any) error {
	path, err := runCtx.ResolveRunRelative(rel)
	if err != nil {
		return err
	}
	return internalio.AppendJSONL(path, record)
}

func mustReadJSONL[T any](t *testing.T, runCtx internalio.RunContext, rel string) []T {
	t.Helper()
	path, err := runCtx.ResolveRunRelative(rel)
	require.NoError(t, err)
	rows, err := internalio.ReadJSONL[T](path)
	require.NoError(t, err)
	return rows
}

func writePass1ScoringRowsForAdapterTest(
	t *testing.T,
	runCtx internalio.RunContext,
	runID contracts.RunID,
	agent contracts.AgentID,
	rubricVersion string,
	promptVersion string,
) {
	t.Helper()
	now := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
	dimensions := []contracts.Dimension{
		contracts.DimensionFidelity,
		contracts.DimensionCorrectness,
		contracts.DimensionMaintainability,
		contracts.DimensionDiscipline,
		contracts.DimensionCommunication,
	}
	for i, dimension := range dimensions {
		require.NoError(t, appendJSONLForTest(runCtx, "30/scores-A.jsonl", contracts.ScoreEntry{
			SchemaVersion: "1",
			RunID:         runID,
			Pass:          1,
			Agent:         agent,
			Dimension:     dimension,
			Score:         80 + i,
			Reasons:       "pass1 fixture score",
			VerdictPath:   contracts.VerdictPathAgreement,
			RubricVersion: rubricVersion,
			PromptVersion: promptVersion,
			ResolvedAt:    now,
		}))
	}
	require.NoError(t, appendJSONLForTest(runCtx, "30/compliance-A.jsonl", contracts.ComplianceEntry{
		SchemaVersion: "1",
		RunID:         runID,
		Pass:          1,
		Agent:         agent,
		RuleID:        "stub-rubric-rule",
		Verdict:       contracts.ComplianceVerdictCompliant,
		Rationale:     "pass1 fixture compliance",
		VerdictPath:   contracts.VerdictPathAgreement,
		RubricVersion: rubricVersion,
		PromptVersion: promptVersion,
		ResolvedAt:    now,
	}))
}

func writeValidStep30ArtifactsForTest(runCtx internalio.RunContext) error {
	scoreFinalPath, err := runCtx.ResolveRunRelative("30/scores-A.jsonl")
	if err != nil {
		return err
	}
	complianceFinalPath, err := runCtx.ResolveRunRelative("30/compliance-A.jsonl")
	if err != nil {
		return err
	}
	scoreRawPath, err := runCtx.ResolveRunRelative("30/scores-A-raw.jsonl")
	if err != nil {
		return err
	}
	complianceRawPath, err := runCtx.ResolveRunRelative("30/compliance-A-raw.jsonl")
	if err != nil {
		return err
	}
	scoreFinal, err := internalio.ReadJSONL[contracts.ScoreEntry](scoreFinalPath)
	if err != nil {
		return err
	}
	complianceFinal, err := internalio.ReadJSONL[contracts.ComplianceEntry](complianceFinalPath)
	if err != nil {
		return err
	}
	if err := internalio.WriteAtomic(scoreRawPath, nil); err != nil {
		return err
	}
	for _, row := range scoreFinal {
		if err := internalio.AppendJSONL(scoreRawPath, contracts.RawScoreEntry{
			SchemaVersion: "1",
			RunID:         row.RunID,
			Pass:          row.Pass,
			Agent:         row.Agent,
			JudgeRole:     contracts.JudgeRolePrimary,
			Dimension:     row.Dimension,
			Score:         row.Score,
			Reasons:       row.Reasons,
			OutputSha256:  strings.Repeat("a", 64),
			RubricVersion: row.RubricVersion,
			PromptVersion: row.PromptVersion,
			ResolvedAt:    row.ResolvedAt,
		}); err != nil {
			return err
		}
	}
	if err := internalio.WriteAtomic(complianceRawPath, nil); err != nil {
		return err
	}
	for _, row := range complianceFinal {
		if err := internalio.AppendJSONL(complianceRawPath, contracts.RawComplianceEntry{
			SchemaVersion: "1",
			RunID:         row.RunID,
			Pass:          row.Pass,
			Agent:         row.Agent,
			JudgeRole:     contracts.JudgeRolePrimary,
			RuleID:        row.RuleID,
			Verdict:       row.Verdict,
			Rationale:     row.Rationale,
			OutputSha256:  strings.Repeat("a", 64),
			RubricVersion: row.RubricVersion,
			PromptVersion: row.PromptVersion,
			ResolvedAt:    row.ResolvedAt,
		}); err != nil {
			return err
		}
	}
	marker, err := scorecore.BuildStep30DoneMarker(scorecore.Step30MarkerInputs{
		Agents: []contracts.AgentID{"a1"},
		Paths: scorecore.Step30MarkerPaths{
			ScoreFinal:      scoreFinalPath,
			ComplianceFinal: complianceFinalPath,
			ScoreRaw:        scoreRawPath,
			ComplianceRaw:   complianceRawPath,
		},
		ResolvedAt: time.Now().UTC(),
	})
	if err != nil {
		return err
	}
	return scorecore.WriteStep30DoneMarker(runCtx, marker)
}

func seedResumeRun(t *testing.T, runCtx internalio.RunContext, pr int) error {
	t.Helper()
	if err := os.MkdirAll(runCtx.RunDir(), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(runCtx.RunDir(), "config.snapshot.yaml"), []byte(
		"repo:\n"+
			"  root: "+runCtx.RunsBase+"\n"+
			"  default_branch: main\n"+
			"  best_branch: best\n"+
			"paths:\n"+
			"  runs: "+runCtx.RunsBase+"\n"+
			"worktree:\n"+
			"  base: "+runCtx.WorktreeBase+"\n",
	), 0o644); err != nil {
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
	if err := writeRunText(runCtx, "60/done.marker", "stub\n"); err != nil {
		return err
	}
	return state.Append(runCtx, startedEntry(pr, runCtx.RunID, time.Now().UTC()))
}

func overwriteTimeoutManifests(t *testing.T, runCtx internalio.RunContext, pass int) {
	t.Helper()
	for _, agent := range defaultAgents {
		manifestPath, err := runCtx.ManifestPath(pass, agent)
		require.NoError(t, err)
		now := time.Now().UTC()
		manifest := contracts.Manifest{
			Kind: contracts.ManifestKindTimeout,
			Value: contracts.ManifestTimeout{
				Kind:           contracts.ManifestKindTimeout,
				SchemaVersion:  "1",
				RunID:          runCtx.RunID,
				Pass:           pass,
				Agent:          agent,
				TimeoutSeconds: 1,
				StartedAt:      now.Add(-time.Second),
				FinishedAt:     now,
			},
		}
		require.NoError(t, internalio.WriteJSONAtomic(manifestPath, manifest))
	}
}

func validPlanningIntention(runID contracts.RunID) contracts.IntentionRecord {
	best := strings.Repeat("1", 40)
	target := strings.Repeat("2", 40)
	hash := strings.Repeat("3", 64)
	body := "r-0001 body\n"
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
					Sha256:   sha256String(body),
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
