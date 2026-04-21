package step40_classify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun_EmptyInputsEmitsZeroCandidates(t *testing.T) {
	cfg := setupConfig(t)

	candidates, err := Run(context.Background(), cfg)
	require.NoError(t, err)
	require.NotNil(t, candidates)
	require.Empty(t, candidates.Candidates)
	require.Equal(t, contracts.CanonicalCandidatesHash(nil), candidates.CandidatesHash)
	require.NoError(t, candidates.VerifyCandidatesHash())

	candidatesPath, err := cfg.IO.ResolveRunRelative("40/candidates.json")
	require.NoError(t, err)
	decoded, err := internalio.ReadJSON[contracts.Candidates](candidatesPath)
	require.NoError(t, err)
	require.NoError(t, decoded.Validate())
	require.Empty(t, decoded.Candidates)

	classificationPath, err := cfg.IO.ResolveRunRelative("40/classification.jsonl")
	require.NoError(t, err)
	rows, err := internalio.ReadJSONL[contracts.ClassificationEntry](classificationPath)
	require.NoError(t, err)
	require.Empty(t, rows)
}

func TestRun_ViolationsOnlyProducesKindNew(t *testing.T) {
	cfg := setupConfig(t)
	writeCompliance(t, cfg.IO, []contracts.ComplianceEntry{
		mkCompliance(cfg.IO.RunID, "rule-alpha", contracts.ComplianceVerdictViolated),
		mkCompliance(cfg.IO.RunID, "rule-alpha", contracts.ComplianceVerdictMissed),
		mkCompliance(cfg.IO.RunID, "rule-beta", contracts.ComplianceVerdictInvalidException),
		mkCompliance(cfg.IO.RunID, "rule-ignored", contracts.ComplianceVerdictCompliant),
	})

	candidates, err := Run(context.Background(), cfg)
	require.NoError(t, err)
	require.Len(t, candidates.Candidates, 2)
	for _, c := range candidates.Candidates {
		require.Equal(t, contracts.CandidateKindNew, c.Kind)
		require.Empty(t, c.TargetRuleID)
	}
	require.Equal(t, "rule-alpha", candidateRuleTitle(candidates.Candidates[0]))
	require.Equal(t, "rule-beta", candidateRuleTitle(candidates.Candidates[1]))

	assertCandidatesRoundTrip(t, cfg)
	assertClassificationRoundTrip(t, cfg, 2, contracts.CandidateKindNew)
	assertBodyFiles(t, cfg, candidates.Candidates)
}

func TestRun_RegistryMatchProducesKindUpdate(t *testing.T) {
	cfg := setupConfig(t)
	writeCompliance(t, cfg.IO, []contracts.ComplianceEntry{
		mkCompliance(cfg.IO.RunID, "rule-alpha", contracts.ComplianceVerdictViolated),
		mkCompliance(cfg.IO.RunID, "rule-beta", contracts.ComplianceVerdictMissed),
	})
	writeRegistry(t, cfg.RegistryPath, []contracts.RuleRegistryEntry{
		mkRegistryAdded(cfg.IO.RunID, "rule-alpha", 1, ""),
	})

	candidates, err := Run(context.Background(), cfg)
	require.NoError(t, err)
	require.Len(t, candidates.Candidates, 2)

	var alpha, beta contracts.Candidate
	for _, c := range candidates.Candidates {
		switch candidateRuleTitle(c) {
		case "rule-alpha":
			alpha = c
		case "rule-beta":
			beta = c
		}
	}
	require.Equal(t, contracts.CandidateKindUpdate, alpha.Kind)
	require.Equal(t, "rule-alpha", alpha.TargetRuleID)
	require.Equal(t, contracts.CandidateKindNew, beta.Kind)
	require.Empty(t, beta.TargetRuleID)

	classificationPath, err := cfg.IO.ResolveRunRelative("40/classification.jsonl")
	require.NoError(t, err)
	rows, err := internalio.ReadJSONL[contracts.ClassificationEntry](classificationPath)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	byID := make(map[string]contracts.ClassificationEntry, 2)
	for _, r := range rows {
		byID[r.CandidateID] = r
	}
	require.Equal(t, contracts.CandidateKindUpdate, byID[alpha.CandidateID].Kind)
	require.Equal(t, "rule-alpha", byID[alpha.CandidateID].MatchedRuleID)
	require.Equal(t, 90, byID[alpha.CandidateID].SimilarityScore)
	require.Equal(t, contracts.CandidateKindNew, byID[beta.CandidateID].Kind)
	require.Empty(t, byID[beta.CandidateID].MatchedRuleID)
	require.Equal(t, 0, byID[beta.CandidateID].SimilarityScore)

	assertCandidatesRoundTrip(t, cfg)
	assertBodyFiles(t, cfg, candidates.Candidates)
}

func TestRun_IsIdempotentAcrossReruns(t *testing.T) {
	cfg := setupConfig(t)
	writeCompliance(t, cfg.IO, []contracts.ComplianceEntry{
		mkCompliance(cfg.IO.RunID, "rule-alpha", contracts.ComplianceVerdictViolated),
	})

	_, err := Run(context.Background(), cfg)
	require.NoError(t, err)
	second, err := Run(context.Background(), cfg)
	require.NoError(t, err)
	require.Len(t, second.Candidates, 1)

	classificationPath, err := cfg.IO.ResolveRunRelative("40/classification.jsonl")
	require.NoError(t, err)
	rows, err := internalio.ReadJSONL[contracts.ClassificationEntry](classificationPath)
	require.NoError(t, err)
	require.Len(t, rows, 1)
}

// --- helpers ------------------------------------------------------------

func setupConfig(t *testing.T) Config {
	t.Helper()
	runsBase := t.TempDir()
	worktreeBase := t.TempDir()
	runID := contracts.RunID("2026-04-21-PR7-abcdef0")
	io, err := internalio.NewRunContext(runID, runsBase, worktreeBase)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(io.RunDir(), 0o755))
	pkg := &contracts.TaskPackage{
		SchemaVersion:           "1",
		RunID:                   runID,
		PR:                      7,
		Title:                   "Stub",
		BaseSHA:                 "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		BestBranch:              "best",
		ReconstructedTaskPrompt: "task",
		Worktrees:               mkWorktrees(worktreeBase, runID),
		CreatedAt:               time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
	}
	now := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
	return Config{
		IO:           io,
		RegistryPath: filepath.Join(runsBase, "rules-registry.jsonl"),
		TaskPackage:  pkg,
		Now:          func() time.Time { return now },
	}
}

func mkWorktrees(base string, runID contracts.RunID) []contracts.WorktreeAllocation {
	const sha = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	agents := []contracts.AgentID{"a1", "a2", "a3"}
	out := make([]contracts.WorktreeAllocation, 0, 6)
	for pass := 1; pass <= 2; pass++ {
		for _, agent := range agents {
			out = append(out, contracts.WorktreeAllocation{
				Agent:   agent,
				Pass:    pass,
				Path:    filepath.Join(base, string(runID)+"-pass"+itoa(pass)+"-"+string(agent)),
				Branch:  "auto-improve/" + string(runID) + "/pass" + itoa(pass) + "/" + string(agent),
				BaseSHA: sha,
				HeadSHA: sha,
			})
		}
	}
	return out
}

func itoa(n int) string {
	if n == 1 {
		return "1"
	}
	return "2"
}

func writeCompliance(t *testing.T, io internalio.RunContext, entries []contracts.ComplianceEntry) {
	t.Helper()
	path, err := io.ResolveRunRelative("30/compliance-A.jsonl")
	require.NoError(t, err)
	for _, e := range entries {
		require.NoError(t, internalio.AppendJSONL(path, e))
	}
}

func writeRegistry(t *testing.T, path string, entries []contracts.RuleRegistryEntry) {
	t.Helper()
	for _, e := range entries {
		_, err := internalio.AppendRegistryEntry(path, e)
		require.NoError(t, err)
	}
}

func mkCompliance(runID contracts.RunID, ruleID string, verdict contracts.ComplianceVerdict) contracts.ComplianceEntry {
	return contracts.ComplianceEntry{
		SchemaVersion: "1",
		RunID:         runID,
		Pass:          1,
		Agent:         "a1",
		RuleID:        ruleID,
		Verdict:       verdict,
		Rationale:     "stub",
		VerdictPath:   contracts.VerdictPathSingle,
		RubricVersion: "default",
		PromptVersion: "phase0-stub",
		ResolvedAt:    time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
	}
}

func mkRegistryAdded(runID contracts.RunID, ruleID string, versionSeq int64, prevHash string) contracts.RuleRegistryEntry {
	// content-dependent sha256 placeholders just need to satisfy sha256_hex.
	const dummy = "0000000000000000000000000000000000000000000000000000000000000000"
	value := contracts.RuleRegistryAdded{
		Kind:           contracts.RegistryKindAdded,
		SchemaVersion:  "1",
		RuleID:         ruleID,
		RulePath:       "rules/" + ruleID + ".md",
		Sha256:         dummy,
		IdempotencyKey: dummy,
		VersionSeq:     versionSeq,
		PrevHash:       prevHash,
		ByRunID:        runID,
		At:             time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC),
	}
	return contracts.RuleRegistryEntry{Kind: value.Kind, Value: value}
}

func candidateRuleTitle(c contracts.Candidate) string {
	const prefix = "Rule candidate for "
	return c.Title[len(prefix):]
}

func assertCandidatesRoundTrip(t *testing.T, cfg Config) {
	t.Helper()
	path, err := cfg.IO.ResolveRunRelative("40/candidates.json")
	require.NoError(t, err)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var decoded contracts.Candidates
	require.NoError(t, decoded.UnmarshalJSON(data))
	require.NoError(t, decoded.VerifyCandidatesHash())
}

func assertClassificationRoundTrip(t *testing.T, cfg Config, want int, kind contracts.CandidateKind) {
	t.Helper()
	path, err := cfg.IO.ResolveRunRelative("40/classification.jsonl")
	require.NoError(t, err)
	rows, err := internalio.ReadJSONL[contracts.ClassificationEntry](path)
	require.NoError(t, err)
	require.Len(t, rows, want)
	for _, r := range rows {
		require.Equal(t, kind, r.Kind)
	}
}

func assertBodyFiles(t *testing.T, cfg Config, candidates []contracts.Candidate) {
	t.Helper()
	for _, c := range candidates {
		abs, err := cfg.IO.ResolveRunRelative(c.ProposedBodyPath)
		require.NoError(t, err)
		body, err := os.ReadFile(abs)
		require.NoError(t, err)
		sum := sha256.Sum256(body)
		assert.Equal(t, hex.EncodeToString(sum[:]), c.ProposedBodySha256)
	}
}
