package step40_classify

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun_RegistryMatchesProduceMixedNewAndUpdateCandidates(t *testing.T) {
	cfg := newTestConfig(t)
	writeScores(t, cfg.IO, testScoreEntries(cfg.IO.RunID)...)
	writeCompliance(t, cfg.IO,
		testComplianceEntry(cfg.IO.RunID, "rule-active", contracts.ComplianceVerdictViolated),
		testComplianceEntry(cfg.IO.RunID, "rule-archived", contracts.ComplianceVerdictMissed),
		testComplianceEntry(cfg.IO.RunID, "rule-restored", contracts.ComplianceVerdictInvalidException),
		testComplianceEntry(cfg.IO.RunID, "rule-fresh", contracts.ComplianceVerdictViolated),
	)
	writeRegistry(t, cfg.registryPath(),
		registryAdded("rule-active", "1111111111111111111111111111111111111111111111111111111111111111"),
		registryAdded("rule-archived", "2222222222222222222222222222222222222222222222222222222222222222"),
		registryAdded("rule-restored", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaab"),
		registryArchived("rule-archived", "3333333333333333333333333333333333333333333333333333333333333333"),
		registryRestored("rule-restored", "4444444444444444444444444444444444444444444444444444444444444444"),
	)

	got, err := Run(context.Background(), cfg)
	require.NoError(t, err)
	require.Len(t, got.Candidates, 4)

	kinds := map[string]contracts.CandidateKind{}
	targets := map[string]string{}
	for _, candidate := range got.Candidates {
		ruleID := experimentLessonTitleID(candidate.Title)
		kinds[ruleID] = candidate.Kind
		targets[ruleID] = candidate.TargetRuleID
	}

	assert.Equal(t, contracts.CandidateKindUpdate, kinds["rule-active"])
	assert.Equal(t, "rule-active", targets["rule-active"])
	assert.Equal(t, contracts.CandidateKindNew, kinds["rule-archived"])
	assert.Empty(t, targets["rule-archived"])
	assert.Equal(t, contracts.CandidateKindUpdate, kinds["rule-restored"])
	assert.Equal(t, "rule-restored", targets["rule-restored"])
	assert.Equal(t, contracts.CandidateKindNew, kinds["rule-fresh"])

	classifications := readClassificationFile(t, cfg.IO)
	require.Len(t, classifications, 4)
	for _, entry := range classifications {
		ruleID := experimentLessonTitleID(got.Candidates[indexOfCandidate(t, got.Candidates, entry.CandidateID)].Title)
		switch ruleID {
		case "rule-active", "rule-restored":
			assert.Equal(t, 90, entry.SimilarityScore)
			assert.Equal(t, ruleID, entry.MatchedRuleID)
			assert.Equal(t, contracts.CandidateKindUpdate, entry.Kind)
		default:
			assert.Zero(t, entry.SimilarityScore)
			assert.Empty(t, entry.MatchedRuleID)
			assert.Equal(t, contracts.CandidateKindNew, entry.Kind)
		}
	}
}

func TestRun_RejectsBrokenRegistryPrevHashChain(t *testing.T) {
	cfg := newTestConfig(t)
	writeScores(t, cfg.IO, testScoreEntries(cfg.IO.RunID)...)
	writeCompliance(t, cfg.IO, testComplianceEntry(cfg.IO.RunID, "rule-b", contracts.ComplianceVerdictViolated))

	first := registryAdded("rule-a", strings.Repeat("1", 64))
	writeRegistry(t, cfg.registryPath(), first)

	second := registryAdded("rule-b", strings.Repeat("2", 64))
	second = setRegistryChainFields(second, 2, strings.Repeat("f", 64))
	writeRegistryRuleSidecar(t, filepath.Dir(cfg.registryPath()), filepath.Join("rules", "rule-b.md"), "# rule-b added\n")
	require.NoError(t, internalio.AppendJSONL(cfg.registryPath(), second))

	got, err := Run(context.Background(), cfg)
	require.ErrorIs(t, err, internalio.ErrRegistryCASMismatch)
	assert.Nil(t, got)
}

func TestRun_RejectsTamperedActiveRuleSidecar(t *testing.T) {
	cfg := newTestConfig(t)
	writeScores(t, cfg.IO, testScoreEntries(cfg.IO.RunID)...)
	writeCompliance(t, cfg.IO, testComplianceEntry(cfg.IO.RunID, "rule-tampered", contracts.ComplianceVerdictViolated))

	ruleBody := "# canonical body\n"
	rulePath := filepath.Join(cfg.IO.RunsBase, "rules", "rule-tampered.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(rulePath), 0o755))
	require.NoError(t, os.WriteFile(rulePath, []byte("# tampered body\n"), 0o644))
	writeRegistry(t, cfg.registryPath(), contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindAdded,
		Value: contracts.RuleRegistryAdded{
			Kind:           contracts.RegistryKindAdded,
			SchemaVersion:  "1",
			RuleID:         "rule-tampered",
			RulePath:       filepath.Join("rules", "rule-tampered.md"),
			Sha256:         sha256String(ruleBody),
			IdempotencyKey: strings.Repeat("a", 64),
			VersionSeq:     1,
			ByRunID:        "2026-04-20-PR1-aaaaaaa",
			At:             time.Date(2026, 4, 20, 9, 0, 0, 0, time.UTC),
		},
	})

	_, err := Run(context.Background(), cfg)
	require.ErrorContains(t, err, "rule sidecar sha mismatch")
}

func TestRun_RegistryVariantsProduceExpectedCandidateKinds(t *testing.T) {
	cfg := newTestConfig(t)
	writeScores(t, cfg.IO, testScoreEntries(cfg.IO.RunID)...)
	writeCompliance(t, cfg.IO,
		testComplianceEntry(cfg.IO.RunID, "rule-updated", contracts.ComplianceVerdictViolated),
		testComplianceEntry(cfg.IO.RunID, "rule-status-active", contracts.ComplianceVerdictMissed),
		testComplianceEntry(cfg.IO.RunID, "rule-rolled-back", contracts.ComplianceVerdictInvalidException),
	)
	updatedAdded := registryAdded("rule-updated", strings.Repeat("7", 64))
	updated := registryUpdated("rule-updated", strings.Repeat("7", 64))
	statusAdded := registryAdded("rule-status-active", strings.Repeat("6", 64))
	statusDeprecated := registryStatusChanged(
		"rule-status-active",
		contracts.RuleStatusActive,
		contracts.RuleStatusDeprecated,
		contracts.SunsetTransitionDeprecate,
		strings.Repeat("8", 64),
	)
	rolledBack := registryAdded("rule-rolled-back", strings.Repeat("9", 64))
	writeRegistry(t, cfg.registryPath(),
		updatedAdded,
		updated,
		statusAdded,
		statusDeprecated,
		rolledBack,
		registryRolledBackForEntries(t, cfg.registryPath(), []contracts.RuleRegistryEntry{updatedAdded, updated, statusAdded, statusDeprecated, rolledBack}, strings.Repeat("9", 64)),
	)

	got, err := Run(context.Background(), cfg)
	require.NoError(t, err)
	require.Len(t, got.Candidates, 3)

	kinds := map[string]contracts.CandidateKind{}
	targets := map[string]string{}
	for _, candidate := range got.Candidates {
		ruleID := experimentLessonTitleID(candidate.Title)
		kinds[ruleID] = candidate.Kind
		targets[ruleID] = candidate.TargetRuleID
	}

	assert.Equal(t, contracts.CandidateKindUpdate, kinds["rule-updated"])
	assert.Equal(t, "rule-updated", targets["rule-updated"])
	assert.Equal(t, contracts.CandidateKindUpdate, kinds["rule-status-active"])
	assert.Equal(t, "rule-status-active", targets["rule-status-active"])
	assert.Equal(t, contracts.CandidateKindNew, kinds["rule-rolled-back"])
	assert.Empty(t, targets["rule-rolled-back"])
}

func TestRun_RejectsInvalidExistingRulePath(t *testing.T) {
	cfg := newTestConfig(t)
	writeScores(t, cfg.IO, testScoreEntries(cfg.IO.RunID)...)
	writeCompliance(t, cfg.IO, testComplianceEntry(cfg.IO.RunID, "rule-a", contracts.ComplianceVerdictViolated))
	require.NoError(t, os.WriteFile(cfg.registryPath(), []byte(`{"kind":"added","schema_version":"1","rule_id":"rule-a","rule_path":"../needs-recovery/pwn.md","sha256":"`+strings.Repeat("1", 64)+`","idempotency_key":"`+strings.Repeat("2", 64)+`","version_seq":1,"by_run_id":"2026-04-21-PR42-abcdef0","at":"2026-04-21T12:00:00Z"}`+"\n"), 0o644))

	_, err := Run(context.Background(), cfg)
	require.ErrorContains(t, err, "invalid rule_path")
}

func TestActiveRulesFromRegistry_Variants(t *testing.T) {
	t.Run("updated entry keeps rule active", func(t *testing.T) {
		active, err := activeRulesFromRegistry([]contracts.RuleRegistryEntry{
			registryAdded("rule-updated", strings.Repeat("0", 64)),
			registryUpdated("rule-updated", strings.Repeat("a", 64)),
		})
		require.NoError(t, err)

		assert.True(t, active["rule-updated"])
	})

	t.Run("status_changed follows archived state", func(t *testing.T) {
		active, err := activeRulesFromRegistry([]contracts.RuleRegistryEntry{
			registryArchived("rule-status-archived", strings.Repeat("b", 64)),
			registryStatusChanged(
				"rule-status-deprecated",
				contracts.RuleStatusActive,
				contracts.RuleStatusDeprecated,
				contracts.SunsetTransitionDeprecate,
				strings.Repeat("c", 64),
			),
		})
		require.NoError(t, err)

		assert.False(t, active["rule-status-archived"])
		assert.True(t, active["rule-status-deprecated"])
	})

	t.Run("rolled_back added entry restores previous inactive state", func(t *testing.T) {
		added := registryAdded("rule-rolled-back", strings.Repeat("d", 64))
		active, err := activeRulesFromRegistry([]contracts.RuleRegistryEntry{
			added,
			registryRolledBackForEntries(t, "", []contracts.RuleRegistryEntry{added}, strings.Repeat("d", 64)),
		})
		require.NoError(t, err)

		assert.False(t, active["rule-rolled-back"])
	})

	t.Run("shared rollback target only reverts the latest matching rule", func(t *testing.T) {
		shared := strings.Repeat("f", 64)
		entryA := registryAdded("rule-a", shared)
		entryB := registryAdded("rule-b", shared)
		entryC := registryAdded("rule-c", shared)
		active, err := activeRulesFromRegistry([]contracts.RuleRegistryEntry{
			entryA,
			entryB,
			entryC,
			registryRolledBackForEntries(t, "", []contracts.RuleRegistryEntry{entryA, entryB, entryC}, shared),
		})
		require.NoError(t, err)

		assert.True(t, active["rule-a"])
		assert.True(t, active["rule-b"])
		assert.False(t, active["rule-c"])
	})
}
