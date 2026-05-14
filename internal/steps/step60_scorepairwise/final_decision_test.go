package step60_scorepairwise

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nishimoto265/harnest/internal/contracts"
	"github.com/nishimoto265/harnest/internal/judges"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun_ArbiterVerdictPaths(t *testing.T) {
	t.Run("arbitrated", func(t *testing.T) {
		runIO, pkg := seedStep60Fixture(t, fixtureOptions{
			writePass1Score:        true,
			nonScorablePass2Agents: map[contracts.AgentID]bool{"a2": true, "a3": true},
		})

		primary := scriptedJudge{score: 80, reasonPrefix: "primary", compliance: map[string]contracts.ComplianceVerdict{"shared": contracts.ComplianceVerdictCompliant}}
		secondary := scriptedJudge{score: 70, reasonPrefix: "secondary", compliance: map[string]contracts.ComplianceVerdict{"shared": contracts.ComplianceVerdictViolated}}
		arbiter := scriptedJudge{score: 80, reasonPrefix: "primary", compliance: map[string]contracts.ComplianceVerdict{"shared": contracts.ComplianceVerdictCompliant}}

		require.NoError(t, Run(context.Background(), Input{
			IO:          runIO,
			TaskPackage: &pkg,
			Primary:     primary,
			Secondary:   secondary,
			Arbiter:     arbiter,
			Now:         func() time.Time { return time.Date(2026, 4, 21, 13, 0, 0, 0, time.UTC) },
		}))

		scores := mustReadJSONL[contracts.ScoreEntry](t, runIO, "60/scores-B.jsonl")
		require.NotEmpty(t, scores)
		assert.Equal(t, contracts.VerdictPathArbitrated, scores[0].VerdictPath)
	})

	t.Run("arbiter_overruled", func(t *testing.T) {
		runIO, pkg := seedStep60Fixture(t, fixtureOptions{
			writePass1Score:        true,
			nonScorablePass2Agents: map[contracts.AgentID]bool{"a2": true, "a3": true},
		})

		primary := scriptedJudge{score: 80, reasonPrefix: "primary", compliance: map[string]contracts.ComplianceVerdict{"shared": contracts.ComplianceVerdictCompliant}}
		secondary := scriptedJudge{score: 70, reasonPrefix: "secondary", compliance: map[string]contracts.ComplianceVerdict{"shared": contracts.ComplianceVerdictViolated}}
		arbiter := scriptedJudge{score: 60, reasonPrefix: "arbiter", compliance: map[string]contracts.ComplianceVerdict{"shared": contracts.ComplianceVerdictNA}}

		require.NoError(t, Run(context.Background(), Input{
			IO:          runIO,
			TaskPackage: &pkg,
			Primary:     primary,
			Secondary:   secondary,
			Arbiter:     arbiter,
			Now:         func() time.Time { return time.Date(2026, 4, 21, 14, 0, 0, 0, time.UTC) },
		}))

		scores := mustReadJSONL[contracts.ScoreEntry](t, runIO, "60/scores-B.jsonl")
		require.NotEmpty(t, scores)
		assert.Equal(t, contracts.VerdictPathArbitrated, scores[0].VerdictPath)
	})
}

func TestRun_ComplianceSingleSideRuleKeepsRawProvenance(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score:        true,
		nonScorablePass2Agents: map[contracts.AgentID]bool{"a2": true, "a3": true},
	})
	rubricPath := writeEmptyRubric(t)

	primary := scriptedJudge{
		score:            80,
		reasonPrefix:     "primary",
		strictCompliance: true,
		compliance: map[string]contracts.ComplianceVerdict{
			"shared": contracts.ComplianceVerdictCompliant,
		},
	}
	secondary := scriptedJudge{
		score:        70,
		reasonPrefix: "secondary",
		compliance: map[string]contracts.ComplianceVerdict{
			"shared":         contracts.ComplianceVerdictViolated,
			"secondary-only": contracts.ComplianceVerdictViolated,
		},
	}
	arbiter := scriptedJudge{
		score:        80,
		reasonPrefix: "primary",
		compliance: map[string]contracts.ComplianceVerdict{
			"shared":         contracts.ComplianceVerdictCompliant,
			"secondary-only": contracts.ComplianceVerdictValidException,
		},
	}

	err := Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		RubricPath:  rubricPath,
		Primary:     primary,
		Secondary:   secondary,
		Arbiter:     arbiter,
		Now:         func() time.Time { return time.Date(2026, 4, 21, 16, 0, 0, 0, time.UTC) },
	})
	require.ErrorIs(t, err, judges.ErrJudgeOutputUnexpectedCompliance)
	assert.NoFileExists(t, mustResolve(t, runIO, "60/done.marker"))
}

func TestRun_ComplianceArbiterMayCoverOnlyDisputedRules(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score:        true,
		nonScorablePass2Agents: map[contracts.AgentID]bool{"a2": true, "a3": true},
	})
	writePass1Compliance(t, runIO, pkg.RunID, "a1", map[string]contracts.ComplianceVerdict{
		"agreed":   contracts.ComplianceVerdictCompliant,
		"disputed": contracts.ComplianceVerdictCompliant,
	})

	primary := scriptedJudge{
		score:        80,
		reasonPrefix: "primary",
		compliance: map[string]contracts.ComplianceVerdict{
			"agreed":   contracts.ComplianceVerdictCompliant,
			"disputed": contracts.ComplianceVerdictViolated,
		},
	}
	secondary := scriptedJudge{
		score:        80,
		reasonPrefix: "secondary",
		compliance: map[string]contracts.ComplianceVerdict{
			"agreed":   contracts.ComplianceVerdictCompliant,
			"disputed": contracts.ComplianceVerdictValidException,
		},
	}
	arbiter := scriptedJudge{
		score:        80,
		reasonPrefix: "arbiter",
		compliance: map[string]contracts.ComplianceVerdict{
			"disputed": contracts.ComplianceVerdictCompliant,
		},
	}

	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     primary,
		Secondary:   secondary,
		Arbiter:     arbiter,
		Now:         func() time.Time { return time.Date(2026, 4, 21, 16, 15, 0, 0, time.UTC) },
	}))

	rows := mustReadJSONL[contracts.RawComplianceEntry](t, runIO, "60/compliance-B-raw.jsonl")
	var arbiterRuleIDs []string
	for _, row := range rows {
		if row.JudgeRole == contracts.JudgeRoleArbiter {
			arbiterRuleIDs = append(arbiterRuleIDs, row.RuleID)
		}
	}
	assert.Equal(t, []string{"disputed"}, arbiterRuleIDs)
}

func TestRun_ComplianceArbiterOnlyRuleFinalizesAsSingleSource(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score:        true,
		nonScorablePass2Agents: map[contracts.AgentID]bool{"a2": true, "a3": true},
	})
	rubricPath := writeEmptyRubric(t)

	primary := scriptedJudge{
		score:            80,
		reasonPrefix:     "primary",
		strictCompliance: true,
		compliance: map[string]contracts.ComplianceVerdict{
			"only-primary": contracts.ComplianceVerdictViolated,
		},
	}
	secondary := scriptedJudge{
		score:        70,
		reasonPrefix: "secondary",
		compliance: map[string]contracts.ComplianceVerdict{
			"only-secondary": contracts.ComplianceVerdictCompliant,
		},
	}
	arbiter := scriptedJudge{
		score:        80,
		reasonPrefix: "arbiter",
		compliance: map[string]contracts.ComplianceVerdict{
			"only-arbiter": contracts.ComplianceVerdictValidException,
		},
	}

	err := Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		RubricPath:  rubricPath,
		Primary:     primary,
		Secondary:   secondary,
		Arbiter:     arbiter,
		Now:         func() time.Time { return time.Date(2026, 4, 21, 16, 30, 0, 0, time.UTC) },
	})
	require.ErrorIs(t, err, judges.ErrJudgeOutputUnexpectedCompliance)
	assert.NoFileExists(t, mustResolve(t, runIO, "60/done.marker"))
}

func TestRun_FailsClosedOnComplianceRuleSetMismatch(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score:        true,
		nonScorablePass2Agents: map[contracts.AgentID]bool{"a2": true, "a3": true},
	})
	rubricPath := writeEmptyRubric(t)

	err := Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		RubricPath:  rubricPath,
		Primary: scriptedJudge{
			score:            80,
			reasonPrefix:     "primary",
			compliance:       map[string]contracts.ComplianceVerdict{"shared": contracts.ComplianceVerdictViolated},
			strictCompliance: true,
		},
		Secondary: scriptedJudge{
			score:        80,
			reasonPrefix: "secondary",
			compliance:   map[string]contracts.ComplianceVerdict{},
		},
		Arbiter: scriptedJudge{
			score:        80,
			reasonPrefix: "arbiter",
			compliance:   map[string]contracts.ComplianceVerdict{"shared": contracts.ComplianceVerdictCompliant},
		},
		Now: func() time.Time { return time.Date(2026, 4, 21, 17, 45, 0, 0, time.UTC) },
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, judges.ErrJudgeOutputUnexpectedCompliance)
	assert.NoFileExists(t, mustResolve(t, runIO, "60/done.marker"))
}

func TestRun_RejectsDuplicateComplianceRuleIDsFromJudgeOutput(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score:        true,
		nonScorablePass2Agents: map[contracts.AgentID]bool{"a2": true, "a3": true},
	})
	err := Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     duplicateComplianceJudge{},
		Secondary:   judges.NewSecondaryStub(),
		Arbiter:     judges.NewArbiterStub(),
		Now:         func() time.Time { return time.Date(2026, 4, 21, 17, 50, 0, 0, time.UTC) },
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, judges.ErrJudgeOutputDuplicateCompliance)
}

func TestNormalizeCompliance_RejectsDuplicateRuleIDs(t *testing.T) {
	runIO, _ := seedStep60Fixture(t, fixtureOptions{
		writePass1Score: true,
	})
	_, err := normalizeCompliance(runIO, []contracts.ComplianceEntry{
		{
			SchemaVersion: "1",
			RunID:         runIO.RunID,
			Pass:          2,
			Agent:         "a1",
			RuleID:        "rule-x",
			Verdict:       contracts.ComplianceVerdictViolated,
			Rationale:     "first",
			VerdictPath:   contracts.VerdictPathSingle,
			RubricVersion: "r1",
			PromptVersion: "p1",
			ResolvedAt:    time.Date(2026, 4, 21, 17, 51, 0, 0, time.UTC),
		},
		{
			SchemaVersion: "1",
			RunID:         runIO.RunID,
			Pass:          2,
			Agent:         "a1",
			RuleID:        "rule-x",
			Verdict:       contracts.ComplianceVerdictCompliant,
			Rationale:     "second",
			VerdictPath:   contracts.VerdictPathSingle,
			RubricVersion: "r1",
			PromptVersion: "p1",
			ResolvedAt:    time.Date(2026, 4, 21, 17, 52, 0, 0, time.UTC),
		},
	}, "rubric-v1", "prompt-v1")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDuplicateComplianceRuleID)
}

func TestRun_RejectsArbiterOnlyComplianceRowsOutsideDisputedSet(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score:        true,
		nonScorablePass2Agents: map[contracts.AgentID]bool{"a2": true, "a3": true},
	})
	err := Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary: scriptedJudge{
			score:        80,
			reasonPrefix: "primary",
			compliance:   map[string]contracts.ComplianceVerdict{},
		},
		Secondary: scriptedJudge{
			score:        70,
			reasonPrefix: "secondary",
			compliance:   map[string]contracts.ComplianceVerdict{},
		},
		Arbiter: scriptedJudge{
			score:            75,
			reasonPrefix:     "arbiter",
			strictCompliance: true,
			compliance: map[string]contracts.ComplianceVerdict{
				"rule-x": contracts.ComplianceVerdictViolated,
			},
		},
		Now: func() time.Time { return time.Date(2026, 4, 21, 17, 55, 0, 0, time.UTC) },
	})
	require.ErrorIs(t, err, judges.ErrJudgeOutputUnexpectedCompliance)
	assert.NoFileExists(t, mustResolve(t, runIO, "60/done.marker"))
}

func TestReduceRawScores_KeepsArbiterWhenRefsMatchRawEntryHashes(t *testing.T) {
	resolvedAt := time.Date(2026, 4, 21, 21, 0, 0, 0, time.UTC)
	primary := contracts.RawScoreEntry{
		SchemaVersion: "1",
		RunID:         "2026-04-21-PR42-abcdef0",
		Pass:          2,
		Agent:         "a1",
		JudgeRole:     contracts.JudgeRolePrimary,
		Dimension:     contracts.DimensionFidelity,
		Score:         80,
		Reasons:       "primary",
		OutputSha256:  strings.Repeat("a", 64),
		RubricVersion: "r",
		PromptVersion: "p",
		ResolvedAt:    resolvedAt,
	}
	secondary := contracts.RawScoreEntry{
		SchemaVersion: "1",
		RunID:         primary.RunID,
		Pass:          2,
		Agent:         "a1",
		JudgeRole:     contracts.JudgeRoleSecondary,
		Dimension:     contracts.DimensionFidelity,
		Score:         79,
		Reasons:       "secondary",
		OutputSha256:  strings.Repeat("b", 64),
		RubricVersion: "r",
		PromptVersion: "p",
		ResolvedAt:    resolvedAt,
	}
	primaryHash, err := rawScoreEntryHash(primary)
	require.NoError(t, err)
	secondaryHash, err := rawScoreEntryHash(secondary)
	require.NoError(t, err)
	arbiter := contracts.RawScoreEntry{
		SchemaVersion: "1",
		RunID:         primary.RunID,
		Pass:          2,
		Agent:         "a1",
		JudgeRole:     contracts.JudgeRoleArbiter,
		Dimension:     contracts.DimensionFidelity,
		Score:         80,
		Reasons:       "arbiter",
		OutputSha256:  strings.Repeat("c", 64),
		PrimaryRef:    &contracts.RawJudgeRef{Role: contracts.JudgeRolePrimary, Sha256: primaryHash},
		SecondaryRef:  &contracts.RawJudgeRef{Role: contracts.JudgeRoleSecondary, Sha256: secondaryHash},
		RubricVersion: "r",
		PromptVersion: "p",
		ResolvedAt:    resolvedAt,
	}

	reduced := reduceRawScores([]contracts.RawScoreEntry{primary, secondary, arbiter})
	require.Len(t, reduced, 3)
	assert.Equal(t, contracts.JudgeRoleArbiter, reduced[2].JudgeRole)
}

func TestReduceRawCompliance_KeepsArbiterWhenRefsMatchRawEntryHashes(t *testing.T) {
	resolvedAt := time.Date(2026, 4, 21, 21, 0, 0, 0, time.UTC)
	primary := contracts.RawComplianceEntry{
		SchemaVersion: "1",
		RunID:         "2026-04-21-PR42-abcdef0",
		Pass:          2,
		Agent:         "a1",
		JudgeRole:     contracts.JudgeRolePrimary,
		RuleID:        "rule-1",
		Verdict:       contracts.ComplianceVerdictViolated,
		Rationale:     "primary",
		OutputSha256:  strings.Repeat("a", 64),
		RubricVersion: "r",
		PromptVersion: "p",
		ResolvedAt:    resolvedAt,
	}
	secondary := contracts.RawComplianceEntry{
		SchemaVersion: "1",
		RunID:         primary.RunID,
		Pass:          2,
		Agent:         "a1",
		JudgeRole:     contracts.JudgeRoleSecondary,
		RuleID:        "rule-1",
		Verdict:       contracts.ComplianceVerdictCompliant,
		Rationale:     "secondary",
		OutputSha256:  strings.Repeat("b", 64),
		RubricVersion: "r",
		PromptVersion: "p",
		ResolvedAt:    resolvedAt,
	}
	primaryHash, err := rawComplianceEntryHash(primary)
	require.NoError(t, err)
	secondaryHash, err := rawComplianceEntryHash(secondary)
	require.NoError(t, err)
	arbiter := contracts.RawComplianceEntry{
		SchemaVersion: "1",
		RunID:         primary.RunID,
		Pass:          2,
		Agent:         "a1",
		JudgeRole:     contracts.JudgeRoleArbiter,
		RuleID:        "rule-1",
		Verdict:       contracts.ComplianceVerdictViolated,
		Rationale:     "arbiter",
		OutputSha256:  strings.Repeat("c", 64),
		PrimaryRef:    &contracts.RawJudgeRef{Role: contracts.JudgeRolePrimary, Sha256: primaryHash},
		SecondaryRef:  &contracts.RawJudgeRef{Role: contracts.JudgeRoleSecondary, Sha256: secondaryHash},
		RubricVersion: "r",
		PromptVersion: "p",
		ResolvedAt:    resolvedAt,
	}

	reduced := reduceRawCompliance([]contracts.RawComplianceEntry{primary, secondary, arbiter})
	require.Len(t, reduced, 3)
	assert.Equal(t, contracts.JudgeRoleArbiter, reduced[2].JudgeRole)
}
