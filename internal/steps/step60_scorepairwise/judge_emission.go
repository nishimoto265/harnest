package step60_scorepairwise

import (
	"context"
	"fmt"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/judges"
	"github.com/nishimoto265/auto-improve/internal/steps/scorecore"
)

func scoreJudgeOutput(ctx context.Context, label string, judge judges.Judge, input judges.JudgeInput) (judges.JudgeOutput, error) {
	if err := ctx.Err(); err != nil {
		return judges.JudgeOutput{}, fmt.Errorf("step60: %s judge score output for agent=%s: %w", label, input.Agent, err)
	}
	output, err := judge.ScoreOutput(ctx, input)
	if err != nil {
		return judges.JudgeOutput{}, fmt.Errorf("step60: %s judge score output for agent=%s: %w", label, input.Agent, err)
	}
	if err := output.ValidateFor(input); err != nil {
		return judges.JudgeOutput{}, fmt.Errorf("step60: validate %s judge output for agent=%s: %w", label, input.Agent, err)
	}
	return output, nil
}

func normalizeScores(runIO internalio.RunContext, scores []contracts.ScoreEntry, rubricVersion, promptVersion string) (map[contracts.Dimension]contracts.ScoreEntry, error) {
	out := make(map[contracts.Dimension]contracts.ScoreEntry, len(scores))
	for _, score := range scores {
		score.RubricVersion = rubricVersion
		score.PromptVersion = promptVersion
		var err error
		score, err = canonicalizeScoreOverflow(runIO, score)
		if err != nil {
			return nil, err
		}
		out[score.Dimension] = score
	}
	return out, nil
}

func normalizeCompliance(runIO internalio.RunContext, entries []contracts.ComplianceEntry, rubricVersion, promptVersion string) (map[string]contracts.ComplianceEntry, error) {
	out := make(map[string]contracts.ComplianceEntry, len(entries))
	for _, entry := range entries {
		entry.RubricVersion = rubricVersion
		entry.PromptVersion = promptVersion
		var err error
		entry, err = canonicalizeComplianceOverflow(runIO, entry)
		if err != nil {
			return nil, err
		}
		if _, exists := out[entry.RuleID]; exists {
			return nil, fmt.Errorf("%w: rule_id=%s", ErrDuplicateComplianceRuleID, entry.RuleID)
		}
		out[entry.RuleID] = entry
	}
	return out, nil
}

func canonicalizeScoreOverflow(runIO internalio.RunContext, score contracts.ScoreEntry) (contracts.ScoreEntry, error) {
	score.ReasonsOverflowRef = nil
	if len([]rune(score.Reasons)) <= scorecore.ReasonsMaxChars {
		return score, nil
	}
	ref, err := scorecore.WriteOverflowSidecar(runIO, "60", score.Reasons)
	if err != nil {
		return contracts.ScoreEntry{}, err
	}
	score.Reasons = ""
	score.ReasonsOverflowRef = &ref
	return score, nil
}

func canonicalizeComplianceOverflow(runIO internalio.RunContext, entry contracts.ComplianceEntry) (contracts.ComplianceEntry, error) {
	entry.RationaleOverflowRef = nil
	if len([]rune(entry.Rationale)) <= scorecore.RationaleMaxChars {
		return entry, nil
	}
	ref, err := scorecore.WriteOverflowSidecar(runIO, "60", entry.Rationale)
	if err != nil {
		return contracts.ComplianceEntry{}, err
	}
	entry.Rationale = ""
	entry.RationaleOverflowRef = &ref
	return entry, nil
}

func complianceRuleSetsMatch(primary, secondary map[string]contracts.ComplianceEntry) bool {
	if len(primary) != len(secondary) {
		return false
	}
	for ruleID := range primary {
		if _, ok := secondary[ruleID]; !ok {
			return false
		}
	}
	return true
}

func emitScores(
	paths step60Paths,
	meta finalMetadata,
	agent contracts.AgentID,
	outputHash string,
	primary map[contracts.Dimension]contracts.ScoreEntry,
	secondary map[contracts.Dimension]contracts.ScoreEntry,
	arbiter map[contracts.Dimension]contracts.ScoreEntry,
	primaryRaw []contracts.RawScoreEntry,
	secondaryRaw []contracts.RawScoreEntry,
	primaryRefs map[contracts.Dimension]*contracts.RawJudgeRef,
	secondaryRefs map[contracts.Dimension]*contracts.RawJudgeRef,
) ([]contracts.ScoreEntry, error) {
	arbiterRaw := make([]contracts.RawScoreEntry, 0, len(canonicalDimensions))
	if len(arbiter) > 0 {
		var err error
		arbiterRaw, _, err = buildRawScores(arbiter, outputHash, contracts.JudgeRoleArbiter, primaryRefs, secondaryRefs, meta.ResolvedAt)
		if err != nil {
			return nil, err
		}
	}
	result, err := scorecore.BuildFinalResultFromRaw(
		primaryRaw,
		secondaryRaw,
		arbiterRaw,
		nil,
		nil,
		nil,
		defaultDisagreementThreshold,
		true,
		len(arbiterRaw) > 0,
	)
	if err != nil {
		return nil, err
	}
	rawRows := make([]contracts.RawScoreEntry, 0, len(primaryRaw)+len(secondaryRaw)+len(arbiterRaw))
	rawRows = append(rawRows, primaryRaw...)
	rawRows = append(rawRows, secondaryRaw...)
	rawRows = append(rawRows, arbiterRaw...)
	for _, row := range rawRows {
		if err := appendJSONLWithParentDirSync(paths.ScoresRaw, row); err != nil {
			return nil, fmt.Errorf("step60: append raw score agent=%s: %w", agent, err)
		}
	}
	finalScores := make([]contracts.ScoreEntry, 0, len(result.FinalScores))
	for _, row := range result.FinalScores {
		finalScore := finalizeScore(meta, row, row.VerdictPath)
		if err := appendJSONLWithParentDirSync(paths.ScoresFinal, finalScore); err != nil {
			return nil, fmt.Errorf("step60: append final score agent=%s: %w", agent, err)
		}
		finalScores = append(finalScores, finalScore)
	}
	return finalScores, nil
}

func emitCompliance(
	paths step60Paths,
	meta finalMetadata,
	agent contracts.AgentID,
	outputHash string,
	primary map[string]contracts.ComplianceEntry,
	secondary map[string]contracts.ComplianceEntry,
	arbiter map[string]contracts.ComplianceEntry,
) ([]contracts.ComplianceEntry, error) {
	if !complianceRuleSetsMatch(primary, secondary) {
		return nil, fmt.Errorf("step60: compliance rule-set mismatch agent=%s", agent)
	}
	if err := scorecore.ValidateArbiterComplianceRuleCoverage(
		disputedComplianceRuleIDs(primary, secondary),
		disputedComplianceRuleIDs(primary, secondary),
		sortedComplianceRuleIDs(arbiter),
	); err != nil && len(arbiter) > 0 {
		return nil, fmt.Errorf("step60: agent=%s: %w", agent, err)
	}
	ruleIDs := complianceRuleIDs(primary, secondary)
	finalEntries := make([]contracts.ComplianceEntry, 0, len(ruleIDs))
	for _, ruleID := range ruleIDs {
		primaryEntry, primaryOK := primary[ruleID]
		secondaryEntry, secondaryOK := secondary[ruleID]
		arbiterEntry, arbiterOK := arbiter[ruleID]

		var primaryHash string
		if primaryOK {
			rawPrimary := makeRawComplianceEntry(primaryEntry, contracts.JudgeRolePrimary, outputHash, nil, nil, meta.ResolvedAt)
			var err error
			primaryHash, err = rawComplianceEntryHash(rawPrimary)
			if err != nil {
				return nil, fmt.Errorf("step60: hash primary compliance rule=%s agent=%s: %w", ruleID, agent, err)
			}
			if err := appendJSONLWithParentDirSync(paths.ComplianceRaw, rawPrimary); err != nil {
				return nil, fmt.Errorf("step60: append primary raw compliance rule=%s agent=%s: %w", ruleID, agent, err)
			}
		}
		var secondaryHash string
		if secondaryOK {
			rawSecondary := makeRawComplianceEntry(secondaryEntry, contracts.JudgeRoleSecondary, outputHash, nil, nil, meta.ResolvedAt)
			var err error
			secondaryHash, err = rawComplianceEntryHash(rawSecondary)
			if err != nil {
				return nil, fmt.Errorf("step60: hash secondary compliance rule=%s agent=%s: %w", ruleID, agent, err)
			}
			if err := appendJSONLWithParentDirSync(paths.ComplianceRaw, rawSecondary); err != nil {
				return nil, fmt.Errorf("step60: append secondary raw compliance rule=%s agent=%s: %w", ruleID, agent, err)
			}
		}

		primaryDecision := complianceEntryOrMissed(meta, agent, ruleID, primaryEntry, primaryOK)
		secondaryDecision := complianceEntryOrMissed(meta, agent, ruleID, secondaryEntry, secondaryOK)

		var finalEntry contracts.ComplianceEntry
		switch {
		case primaryDecision.Verdict == secondaryDecision.Verdict:
			finalEntry = finalizeCompliance(meta, preferredComplianceAgreementSource(primaryDecision, secondaryDecision, primaryOK, secondaryOK), contracts.VerdictPathAgreement)
		case !primaryOK || !secondaryOK:
			// Single-side rules finalize directly from the observed side so the final verdict
			// remains fully traceable from compliance-B-raw.jsonl without synthetic arbiter input.
			finalEntry = finalizeCompliance(meta, preferredComplianceSingleSource(primaryDecision, secondaryDecision, primaryOK, secondaryOK), contracts.VerdictPathSingle)
		default:
			if !arbiterOK {
				return nil, fmt.Errorf("step60: arbiter compliance missing rule=%s agent=%s", ruleID, agent)
			}
			if err := appendJSONLWithParentDirSync(paths.ComplianceRaw, makeRawComplianceEntry(
				arbiterEntry,
				contracts.JudgeRoleArbiter,
				outputHash,
				&contracts.RawJudgeRef{Role: contracts.JudgeRolePrimary, Sha256: primaryHash},
				&contracts.RawJudgeRef{Role: contracts.JudgeRoleSecondary, Sha256: secondaryHash},
				meta.ResolvedAt,
			)); err != nil {
				return nil, fmt.Errorf("step60: append arbiter raw compliance rule=%s agent=%s: %w", ruleID, agent, err)
			}
			finalEntry = finalizeCompliance(meta, arbiterEntry, complianceVerdictPath(primaryDecision, secondaryDecision, arbiterEntry))
		}

		if err := appendJSONLWithParentDirSync(paths.ComplianceFinal, finalEntry); err != nil {
			return nil, fmt.Errorf("step60: append final compliance rule=%s agent=%s: %w", ruleID, agent, err)
		}
		finalEntries = append(finalEntries, finalEntry)
	}
	return finalEntries, nil
}
