package step40_classify

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun_ClassifiesDuplicateWhenExistingRuleBodyMatchesCandidate(t *testing.T) {
	cfg := newTestConfig(t)
	writeScores(t, cfg.IO, testScoreEntries(cfg.IO.RunID)...)
	writeCompliance(t, cfg.IO, testComplianceEntry(cfg.IO.RunID, "rule-dup", contracts.ComplianceVerdictViolated))

	rulesDir := filepath.Join(filepath.Dir(cfg.registryPath()), "rules")
	require.NoError(t, os.MkdirAll(rulesDir, 0o755))
	body := candidateBodyMarkdownWithEvidence(contracts.Candidate{
		CandidateID:      "cand-existing",
		Kind:             contracts.CandidateKindNew,
		Title:            "Experiment lesson for rule-dup",
		Problem:          "Pass1 recorded 1 violation(s) for rule rule-dup.",
		Rationale:        "Derived from 1 compliance violation rationale(s) and 1 score reason(s) for rule-dup.",
		ProposedBodyPath: "40/experiment/lessons/rule-dup.md",
	}, candidateEvidence{
		Compliance: []string{"Rule rule-dup was skipped when the implementation touched the guarded path."},
		Scores:     []string{"a1/fidelity: Missing the guard lets regressions slip into the changed code path."},
	})
	require.NoError(t, os.WriteFile(filepath.Join(rulesDir, "rule-existing.md"), []byte(body), 0o644))
	writeRegistry(t, cfg.registryPath(), contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindAdded,
		Value: contracts.RuleRegistryAdded{
			Kind:           contracts.RegistryKindAdded,
			SchemaVersion:  "1",
			RuleID:         "rule-existing",
			RulePath:       "rules/rule-existing.md",
			Sha256:         sha256Hex([]byte(body)),
			IdempotencyKey: strings.Repeat("7", 64),
			VersionSeq:     1,
			ByRunID:        "2026-04-20-PR1-aaaaaaa",
			At:             time.Date(2026, 4, 20, 9, 0, 0, 0, time.UTC),
		},
	})

	got, err := Run(context.Background(), cfg)
	require.NoError(t, err)
	require.Len(t, got.Candidates, 1)
	assert.Equal(t, contracts.CandidateKindDuplicate, got.Candidates[0].Kind)
	assert.Equal(t, "rule-existing", got.Candidates[0].TargetRuleID)
}

func TestRun_DuplicateClassifierIgnoresRolledBackRuleBody(t *testing.T) {
	cfg := newTestConfig(t)
	writeScores(t, cfg.IO, testScoreEntries(cfg.IO.RunID)...)
	writeCompliance(t, cfg.IO, testComplianceEntry(cfg.IO.RunID, "rule-dup", contracts.ComplianceVerdictViolated))

	rulesDir := filepath.Join(filepath.Dir(cfg.registryPath()), "rules")
	require.NoError(t, os.MkdirAll(rulesDir, 0o755))
	body := candidateBodyMarkdownWithEvidence(contracts.Candidate{
		CandidateID:      "cand-existing",
		Kind:             contracts.CandidateKindUpdate,
		TargetRuleID:     "rule-dup",
		Title:            "Experiment lesson for rule-dup",
		Problem:          "problem",
		Rationale:        "rationale",
		ProposedBodyPath: "40/experiment/lessons/rule-dup.md",
	}, candidateEvidence{})
	require.NoError(t, os.WriteFile(filepath.Join(rulesDir, "rule-dup.md"), []byte(body), 0o644))
	added := registryAdded("rule-dup", strings.Repeat("9", 64))
	writeRegistry(t, cfg.registryPath(),
		added,
		registryRolledBackForEntries(t, cfg.registryPath(), []contracts.RuleRegistryEntry{added}, strings.Repeat("9", 64)),
	)

	got, err := Run(context.Background(), cfg)
	require.NoError(t, err)
	require.Len(t, got.Candidates, 1)
	assert.Equal(t, contracts.CandidateKindNew, got.Candidates[0].Kind)
}

func TestBestDuplicateMatch_IgnoresTemplateBoilerplate(t *testing.T) {
	candidateBody := candidateBodyMarkdown(contracts.Candidate{
		CandidateID:  "cand-1",
		Kind:         contracts.CandidateKindNew,
		Title:        "Experiment lesson for rule-a",
		Problem:      "Pass1 recorded 3 violation(s) for rule rule-a.",
		Rationale:    "Phase 0 deterministic classify generated one candidate from compliance-A.jsonl for rule-a.",
		TargetRuleID: "",
	})
	activeRuleBodies := map[string]string{
		"rule-a": candidateBody,
		"rule-b": "# Existing rule\n\n- source_rule_id: rule-b\n- classification: update\n\n## Problem\nPass1 recorded 3 violation(s) for rule rule-b.\n\n## Rationale\nPhase 0 deterministic classify generated one candidate from compliance-A.jsonl for rule-b.\n",
	}

	ruleID, score := bestDuplicateMatch(candidateBody, activeRuleBodies)
	assert.Equal(t, "rule-a", ruleID)
	assert.Greater(t, score, 0.9)
}

func TestBestDuplicateMatch_BreaksEqualScoreTiesLexicographically(t *testing.T) {
	candidateBody := "# Existing rule\n\n- source_rule_id: candidate\n- classification: new\n\n## Problem\nPass1 recorded 1 violation(s) for rule candidate.\n\n## Rationale\nPhase 0 deterministic classify generated one candidate from compliance-A.jsonl for candidate.\n\n## Proposed rule\n- Keep the same normalized body.\n"
	activeRuleBodies := map[string]string{
		"rule-b": "# Existing rule\n\n- source_rule_id: rule-b\n- classification: update\n\n## Problem\nPass1 recorded 1 violation(s) for rule rule-b.\n\n## Rationale\nPhase 0 deterministic classify generated one candidate from compliance-A.jsonl for rule-b.\n\n## Proposed rule\n- Keep the same normalized body.\n",
		"rule-a": "# Existing rule\n\n- source_rule_id: rule-a\n- classification: update\n\n## Problem\nPass1 recorded 1 violation(s) for rule rule-a.\n\n## Rationale\nPhase 0 deterministic classify generated one candidate from compliance-A.jsonl for rule-a.\n\n## Proposed rule\n- Keep the same normalized body.\n",
	}

	for i := 0; i < 32; i++ {
		ruleID, score := bestDuplicateMatch(candidateBody, activeRuleBodies)
		assert.Equal(t, "rule-a", ruleID)
		assert.Equal(t, 1.0, score)
	}
}
