package step60_scorepairwise

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/nishimoto265/harnest/internal/judges"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun_Pass2JudgeInputIncludesCandidateRules(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score: true,
	})

	candidateRules := []judges.CandidateRule{{
		ID:    "cand-1",
		Kind:  "new",
		Title: "Candidate rule",
		Body:  "When message changes, details must change too.",
	}}
	primary := &echoExpectedComplianceJudge{score: 90}
	secondary := &echoExpectedComplianceJudge{score: 90}
	require.NoError(t, Run(context.Background(), Input{
		IO:             runIO,
		TaskPackage:    &pkg,
		Primary:        primary,
		Secondary:      secondary,
		Arbiter:        judges.NewArbiterStub(),
		CandidateRules: candidateRules,
		Now:            func() time.Time { return time.Date(2026, 4, 21, 15, 4, 5, 0, time.UTC) },
	}))

	require.NotEmpty(t, primary.inputs)
	require.NotEmpty(t, secondary.inputs)
	assert.Equal(t, candidateRules, primary.inputs[0].CandidateRules)
	assert.Contains(t, primary.inputs[0].ExpectedComplianceRuleIDs, "cand-1")
	assert.Contains(t, secondary.inputs[0].ExpectedComplianceRuleIDs, "cand-1")

	finalCompliance := mustReadJSONL[contracts.ComplianceEntry](t, runIO, "60/compliance-B.jsonl")
	finalRuleIDs := make([]string, 0, len(finalCompliance))
	var found bool
	for _, row := range finalCompliance {
		finalRuleIDs = append(finalRuleIDs, row.RuleID)
		if row.RuleID == "cand-1" {
			found = true
			assert.Equal(t, contracts.ComplianceVerdictCompliant, row.Verdict)
		}
	}
	sort.Strings(finalRuleIDs)
	assert.Equal(t, []string{"cand-1", "cand-1", "cand-1"}, finalRuleIDs)
	assert.True(t, found)
}

func TestRun_RejectsMissingExpectedCandidateComplianceRule(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score: true,
	})

	err := Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary: scriptedJudge{
			score:            80,
			reasonPrefix:     "primary",
			compliance:       map[string]contracts.ComplianceVerdict{},
			strictCompliance: true,
		},
		Secondary: judges.NewSecondaryStub(),
		Arbiter:   judges.NewArbiterStub(),
		CandidateRules: []judges.CandidateRule{{
			ID:    "cand-1",
			Kind:  "new",
			Title: "Candidate rule",
			Body:  "Rule body",
		}},
		Now: func() time.Time { return time.Date(2026, 4, 21, 15, 4, 5, 0, time.UTC) },
	})
	require.ErrorIs(t, err, judges.ErrJudgeOutputMissingCompliance)
	assert.ErrorContains(t, err, "cand-1")
}

func TestRun_RejectsMissingExpectedActiveAndPass1ComplianceRules(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score: true,
	})
	writePass1Compliance(t, runIO, pkg.RunID, "a1", map[string]contracts.ComplianceVerdict{
		"pass1-rule": contracts.ComplianceVerdictCompliant,
	})
	rubricPath := filepath.Join(t.TempDir(), "rubric.md")
	require.NoError(t, os.WriteFile(rubricPath, []byte("## Active Rule IDs\n- active-rule\n"), 0o644))

	err := Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		RubricPath:  rubricPath,
		Primary: scriptedJudge{
			score:        80,
			reasonPrefix: "primary",
			compliance: map[string]contracts.ComplianceVerdict{
				"active-rule": contracts.ComplianceVerdictCompliant,
			},
			strictCompliance: true,
		},
		Secondary: judges.NewSecondaryStub(),
		Arbiter:   judges.NewArbiterStub(),
		Now:       func() time.Time { return time.Date(2026, 4, 21, 15, 4, 5, 0, time.UTC) },
	})
	require.ErrorIs(t, err, judges.ErrJudgeOutputMissingCompliance)
	assert.ErrorContains(t, err, "pass1-rule")
}

// TestExpectedComplianceRuleIDsForAgent_IgnoresRawRuleIDs is the F18
// contract: raw rows may contain historical rule IDs that no longer appear
// in pass1. They MUST NOT authorize themselves during reuse — the expected
// set is derived purely from the current pass1 rules (falling back to the
// rubric default when pass1 is silent).
func TestExpectedComplianceRuleIDsForAgent_IgnoresRawRuleIDs(t *testing.T) {
	t.Run("pass1 drives coverage", func(t *testing.T) {
		got := expectedComplianceRuleIDsForAgent(
			"a1",
			map[contracts.AgentID]map[string]struct{}{
				"a1": {"pass1-rule": {}},
			},
			nil,
			[]string{"fallback-rule"},
			nil,
		)
		assert.Equal(t, map[string]struct{}{"pass1-rule": {}}, got)
	})

	t.Run("fallback used when pass1 silent", func(t *testing.T) {
		got := expectedComplianceRuleIDsForAgent(
			"a1",
			map[contracts.AgentID]map[string]struct{}{},
			nil,
			[]string{"fallback-rule"},
			nil,
		)
		assert.Equal(t, map[string]struct{}{"fallback-rule": {}}, got)
	})

	t.Run("empty pass1 and empty fallback yields nil", func(t *testing.T) {
		got := expectedComplianceRuleIDsForAgent(
			"a1",
			map[contracts.AgentID]map[string]struct{}{},
			nil,
			nil,
			nil,
		)
		assert.Nil(t, got)
	})

	t.Run("candidate rules are always included", func(t *testing.T) {
		got := expectedComplianceRuleIDsForAgent(
			"a1",
			map[contracts.AgentID]map[string]struct{}{
				"a1": {"pass1-rule": {}},
			},
			nil,
			[]string{"fallback-rule"},
			[]judges.CandidateRule{{ID: "cand-1"}},
		)
		assert.Equal(t, map[string]struct{}{"pass1-rule": {}, "cand-1": {}}, got)
	})

	t.Run("active rules are included with pass1 and candidate rules", func(t *testing.T) {
		got := expectedComplianceRuleIDsForAgent(
			"a1",
			map[contracts.AgentID]map[string]struct{}{
				"a1": {"pass1-rule": {}},
			},
			[]string{"active-rule"},
			[]string{"fallback-rule"},
			[]judges.CandidateRule{{ID: "cand-1"}},
		)
		assert.Equal(t, map[string]struct{}{"active-rule": {}, "pass1-rule": {}, "cand-1": {}}, got)
	})
}

// TestRun_RerunRejectsRawOnlyRuleIDsAfterPass1Shrink exercises F18 end-to-end:
// step60 first writes raw rows for rule "stale-rule", then pass1 no longer
// declares any compliance rule. On a marker-less resume the raw-only rule
// must not self-authorize the reuse path; judges must be re-invoked so the
// stale compliance evidence is refreshed.
func TestRun_RerunRejectsRawOnlyRuleIDsAfterPass1Shrink(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score:        true,
		nonScorablePass2Agents: map[contracts.AgentID]bool{"a2": true, "a3": true},
	})
	writePass1Compliance(t, runIO, pkg.RunID, "a1", map[string]contracts.ComplianceVerdict{"stale-rule": contracts.ComplianceVerdictCompliant})
	now := time.Date(2026, 4, 21, 11, 45, 0, 0, time.UTC)

	// First run: emit raw rows for "stale-rule".
	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     scriptedJudge{score: 80, reasonPrefix: "primary", compliance: map[string]contracts.ComplianceVerdict{"stale-rule": contracts.ComplianceVerdictCompliant}},
		Secondary:   scriptedJudge{score: 79, reasonPrefix: "secondary", compliance: map[string]contracts.ComplianceVerdict{"stale-rule": contracts.ComplianceVerdictCompliant}},
		Arbiter:     scriptedJudge{score: 78, reasonPrefix: "arbiter", compliance: map[string]contracts.ComplianceVerdict{"stale-rule": contracts.ComplianceVerdictCompliant}},
		Now:         func() time.Time { return now },
	}))

	// Simulate pass1 shrinking: delete the pass1 compliance file entirely.
	// The raw rows for "stale-rule" still sit under 60/.
	require.NoError(t, os.Remove(mustResolve(t, runIO, "30/compliance-A.jsonl")))
	require.NoError(t, os.Remove(mustResolve(t, runIO, "60/done.marker")))

	// A rubric fallback provides coverage for stubRuleID, which is not in
	// the raw rows. So the rerun must re-invoke the judges to regenerate
	// coverage for the new expected rule set rather than trusting
	// "stale-rule" raw rows.
	var called bool
	callTracker := unexpectedCallJudge{called: &called}
	err := Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     callTracker,
		Secondary:   callTracker,
		Arbiter:     callTracker,
		Now:         func() time.Time { return now.Add(time.Minute) },
	})
	// The test asserts the judges are re-invoked — unexpectedCallJudge
	// fails the run, which confirms F18 forced a rejudge instead of
	// silently reusing stale raw compliance.
	require.Error(t, err)
	assert.True(t, called, "F18: raw-only rule IDs must not authorize themselves; judges must run")
}

func TestRun_RerunWithoutMarker_IgnoresStaleFinalComplianceRows(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score: true,
	})
	now := time.Date(2026, 4, 21, 11, 45, 0, 0, time.UTC)

	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     judges.NewPrimaryStub(),
		Secondary:   judges.NewSecondaryStub(),
		Arbiter:     judges.NewArbiterStub(),
		Now:         func() time.Time { return now },
	}))
	require.NoError(t, os.Remove(mustResolve(t, runIO, "60/done.marker")))
	require.NoError(t, internalio.AppendJSONL(mustResolve(t, runIO, "60/compliance-B.jsonl"), contracts.ComplianceEntry{
		SchemaVersion: "1",
		RunID:         pkg.RunID,
		Pass:          2,
		Agent:         "a1",
		RuleID:        "stale-only",
		Verdict:       contracts.ComplianceVerdictViolated,
		VerdictPath:   contracts.VerdictPathSingle,
		RubricVersion: "default",
		PromptVersion: "phase0-stub",
		ResolvedAt:    now,
	}))

	var called bool
	noJudge := unexpectedCallJudge{called: &called}
	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     noJudge,
		Secondary:   noJudge,
		Arbiter:     noJudge,
		Now:         func() time.Time { return now },
	}))
	assert.False(t, called)
}

func TestRun_RebuildsWhenRawComplianceCoverageIsMissing(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score:        true,
		nonScorablePass2Agents: map[contracts.AgentID]bool{"a2": true, "a3": true},
	})
	now := time.Date(2026, 4, 21, 11, 30, 0, 0, time.UTC)
	primary := scriptedJudge{score: 80, reasonPrefix: "primary", compliance: map[string]contracts.ComplianceVerdict{"shared": contracts.ComplianceVerdictCompliant}}
	secondary := scriptedJudge{score: 79, reasonPrefix: "secondary", compliance: map[string]contracts.ComplianceVerdict{"shared": contracts.ComplianceVerdictCompliant}}
	arbiter := scriptedJudge{score: 78, reasonPrefix: "arbiter", compliance: map[string]contracts.ComplianceVerdict{"shared": contracts.ComplianceVerdictCompliant}}
	writePass1Compliance(t, runIO, pkg.RunID, "a1", primary.compliance)

	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     primary,
		Secondary:   secondary,
		Arbiter:     arbiter,
		Now:         func() time.Time { return now },
	}))
	require.NoError(t, os.Remove(mustResolve(t, runIO, "60/compliance-B-raw.jsonl")))
	require.NoError(t, os.Remove(mustResolve(t, runIO, "60/done.marker")))

	counter := &countingJudge{delegate: primary}
	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     counter,
		Secondary:   secondary,
		Arbiter:     arbiter,
		Now:         func() time.Time { return now },
	}))
	assert.Greater(t, counter.callCount(), int32(0))
}

func TestRun_RebuildsWhenRawComplianceCoverageIsPartial(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score:        true,
		nonScorablePass2Agents: map[contracts.AgentID]bool{"a2": true, "a3": true},
	})
	now := time.Date(2026, 4, 21, 11, 45, 0, 0, time.UTC)
	compliance := map[string]contracts.ComplianceVerdict{
		"rule-a": contracts.ComplianceVerdictCompliant,
		"rule-b": contracts.ComplianceVerdictCompliant,
		"rule-c": contracts.ComplianceVerdictCompliant,
		"rule-d": contracts.ComplianceVerdictCompliant,
		"rule-e": contracts.ComplianceVerdictCompliant,
	}
	primary := scriptedJudge{score: 80, reasonPrefix: "primary", compliance: compliance}
	secondary := scriptedJudge{score: 79, reasonPrefix: "secondary", compliance: compliance}
	arbiter := scriptedJudge{score: 78, reasonPrefix: "arbiter", compliance: compliance}
	writePass1Compliance(t, runIO, pkg.RunID, "a1", compliance)

	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     primary,
		Secondary:   secondary,
		Arbiter:     arbiter,
		Now:         func() time.Time { return now },
	}))

	rawCompliancePath := mustResolve(t, runIO, "60/compliance-B-raw.jsonl")
	rawCompliance := mustReadJSONL[contracts.RawComplianceEntry](t, runIO, "60/compliance-B-raw.jsonl")
	truncated := make([]contracts.RawComplianceEntry, 0, 4)
	for _, row := range rawCompliance {
		if row.RuleID == "rule-a" || row.RuleID == "rule-b" {
			truncated = append(truncated, row)
		}
	}
	require.Len(t, truncated, 4)
	rewriteRawCompliance(t, rawCompliancePath, truncated)
	require.NoError(t, os.Remove(mustResolve(t, runIO, "60/done.marker")))

	counter := &countingJudge{delegate: primary}
	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     counter,
		Secondary:   secondary,
		Arbiter:     arbiter,
		Now:         func() time.Time { return now },
	}))

	assert.Greater(t, counter.callCount(), int32(0))
	assert.Len(t, mustReadJSONL[contracts.ComplianceEntry](t, runIO, "60/compliance-B.jsonl"), 5)
}
