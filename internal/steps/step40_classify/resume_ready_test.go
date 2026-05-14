package step40_classify

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nishimoto265/harnest/internal/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun_RerunTruncatesClassificationJSONL(t *testing.T) {
	cfg := newTestConfig(t)
	writeScores(t, cfg.IO, testScoreEntries(cfg.IO.RunID)...)
	writeCompliance(t, cfg.IO,
		testComplianceEntry(cfg.IO.RunID, "rule-a", contracts.ComplianceVerdictViolated),
		testComplianceEntry(cfg.IO.RunID, "rule-b", contracts.ComplianceVerdictMissed),
	)

	_, err := Run(context.Background(), cfg)
	require.NoError(t, err)
	require.Len(t, readClassificationFile(t, cfg.IO), 2)

	writeCompliance(t, cfg.IO, testComplianceEntry(cfg.IO.RunID, "rule-a", contracts.ComplianceVerdictViolated))

	got, err := Run(context.Background(), cfg)
	require.NoError(t, err)
	require.Len(t, got.Candidates, 1)

	classifications := readClassificationFile(t, cfg.IO)
	require.Len(t, classifications, 1)
	assert.Equal(t, got.Candidates[0].CandidateID, classifications[0].CandidateID)
}

func TestRun_RerunKeepsCandidatesHashStable(t *testing.T) {
	cfg := newTestConfig(t)
	writeScores(t, cfg.IO, testScoreEntries(cfg.IO.RunID)...)
	writeCompliance(t, cfg.IO,
		testComplianceEntry(cfg.IO.RunID, "rule-updated", contracts.ComplianceVerdictViolated),
		testComplianceEntry(cfg.IO.RunID, "rule-added", contracts.ComplianceVerdictMissed),
	)
	writeRegistry(t, cfg.registryPath(),
		registryAdded("rule-updated", strings.Repeat("4", 64)),
		registryUpdated("rule-updated", strings.Repeat("5", 64)),
		registryAdded("rule-added", strings.Repeat("6", 64)),
	)

	first, err := Run(context.Background(), cfg)
	require.NoError(t, err)

	second, err := Run(context.Background(), cfg)
	require.NoError(t, err)

	assert.Equal(t, first.CandidatesHash, second.CandidatesHash)
	assert.NoError(t, second.VerifyCandidatesHash())
}

func TestRun_RejectsPartialStep30WithoutDoneMarker(t *testing.T) {
	cfg := newTestConfig(t)
	writeScores(t, cfg.IO, testScoreEntries(cfg.IO.RunID)...)
	writeCompliance(t, cfg.IO, testComplianceEntry(cfg.IO.RunID, "rule-a", contracts.ComplianceVerdictViolated))
	require.NoError(t, os.Remove(mustResolveClassifyPath(t, cfg.IO, "30/done.marker")))

	_, err := Run(context.Background(), cfg)
	require.ErrorContains(t, err, "step30 done.marker is missing or invalid")
}

func TestRun_RejectsStaleStep30DoneMarkerWhenCurrentScorableSetShrinks(t *testing.T) {
	cfg := newTestConfig(t)
	now := time.Date(2026, 4, 21, 11, 0, 0, 0, time.UTC)
	writeScores(t, cfg.IO,
		contracts.ScoreEntry{SchemaVersion: "1", RunID: cfg.IO.RunID, Pass: 1, Agent: "a1", Dimension: contracts.DimensionFidelity, Score: 80, Reasons: "a1", VerdictPath: contracts.VerdictPathSingle, RubricVersion: "default", PromptVersion: "phase0", ResolvedAt: now},
		contracts.ScoreEntry{SchemaVersion: "1", RunID: cfg.IO.RunID, Pass: 1, Agent: "a2", Dimension: contracts.DimensionFidelity, Score: 80, Reasons: "a2", VerdictPath: contracts.VerdictPathSingle, RubricVersion: "default", PromptVersion: "phase0", ResolvedAt: now},
		contracts.ScoreEntry{SchemaVersion: "1", RunID: cfg.IO.RunID, Pass: 1, Agent: "a3", Dimension: contracts.DimensionFidelity, Score: 80, Reasons: "a3", VerdictPath: contracts.VerdictPathSingle, RubricVersion: "default", PromptVersion: "phase0", ResolvedAt: now},
	)
	writeCompliance(t, cfg.IO, testComplianceEntry(cfg.IO.RunID, "rule-a", contracts.ComplianceVerdictViolated))
	writePass1Manifest(t, cfg.IO, cfg.IO.RunID, "a3", false)

	_, err := Run(context.Background(), cfg)
	require.ErrorContains(t, err, "step30 done.marker is missing or invalid")
}

func TestStep30Ready_RejectsTaskPackageWithPass1WorktreesButNoResolvableManifests(t *testing.T) {
	cfg := newTestConfig(t)
	writeScores(t, cfg.IO, testScoreEntries(cfg.IO.RunID)...)
	writeCompliance(t, cfg.IO, testComplianceEntry(cfg.IO.RunID, "rule-a", contracts.ComplianceVerdictViolated))

	for _, agent := range []contracts.AgentID{"a1", "a2", "a3"} {
		manifestPath, err := cfg.IO.ManifestPath(1, agent)
		require.NoError(t, err)
		require.NoError(t, os.Remove(manifestPath))
	}

	ready, err := step30Ready(cfg.IO, cfg.TaskPackage)
	require.ErrorContains(t, err, "pass1 worktrees exist but no pass1 manifests are resolvable")
	assert.False(t, ready)
}
