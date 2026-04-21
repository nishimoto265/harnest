package scorecore

import (
	"fmt"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/judges"
)

// buildRawScoreEntries converts a JudgeOutput into RawScoreEntry rows for the
// given role, attaching optional arbiter refs when role == arbiter. Reasons
// above ReasonsMaxChars are spilled to sidecar and replaced with a reference.
func buildRawScoreEntries(
	out judges.JudgeOutput,
	in PanelInput,
	role contracts.JudgeRole,
	primaryRefs map[contracts.Dimension]*contracts.RawJudgeRef,
	secondaryRefs map[contracts.Dimension]*contracts.RawJudgeRef,
) ([]contracts.RawScoreEntry, error) {
	rows := make([]contracts.RawScoreEntry, 0, len(out.Scores))
	for _, score := range out.Scores {
		reasons := score.Reasons
		var overflow *contracts.OverflowRef
		if len([]rune(reasons)) > ReasonsMaxChars {
			ref, err := WriteOverflowSidecar(in.RunContext, in.StepDir, reasons)
			if err != nil {
				return nil, fmt.Errorf("scorecore: reasons overflow: %w", err)
			}
			overflow = &ref
			reasons = ""
		}
		row := contracts.RawScoreEntry{
			SchemaVersion:      score.SchemaVersion,
			RunID:              score.RunID,
			Pass:               score.Pass,
			Agent:              score.Agent,
			JudgeRole:          role,
			Dimension:          score.Dimension,
			Score:              score.Score,
			Reasons:            reasons,
			ReasonsOverflowRef: overflow,
			OutputSha256:       in.OutputSha256,
			RubricVersion:      score.RubricVersion,
			PromptVersion:      score.PromptVersion,
			ResolvedAt:         score.ResolvedAt,
		}
		if in.RubricVersion != "" {
			row.RubricVersion = in.RubricVersion
		}
		if in.PromptVersion != "" {
			row.PromptVersion = in.PromptVersion
		}
		if role == contracts.JudgeRoleArbiter {
			row.PrimaryRef = primaryRefs[score.Dimension]
			row.SecondaryRef = secondaryRefs[score.Dimension]
		}
		if err := row.Validate(); err != nil {
			return nil, fmt.Errorf("scorecore: raw score row: %w", err)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func buildRawComplianceEntries(
	out judges.JudgeOutput,
	in PanelInput,
	role contracts.JudgeRole,
	primaryRefs map[string]*contracts.RawJudgeRef,
	secondaryRefs map[string]*contracts.RawJudgeRef,
) ([]contracts.RawComplianceEntry, error) {
	rows := make([]contracts.RawComplianceEntry, 0, len(out.Compliance))
	for _, compliance := range out.Compliance {
		rationale := compliance.Rationale
		var overflow *contracts.OverflowRef
		if len([]rune(rationale)) > RationaleMaxChars {
			ref, err := WriteOverflowSidecar(in.RunContext, in.StepDir, rationale)
			if err != nil {
				return nil, fmt.Errorf("scorecore: rationale overflow: %w", err)
			}
			overflow = &ref
			rationale = ""
		}
		row := contracts.RawComplianceEntry{
			SchemaVersion:        compliance.SchemaVersion,
			RunID:                compliance.RunID,
			Pass:                 compliance.Pass,
			Agent:                compliance.Agent,
			JudgeRole:            role,
			RuleID:               compliance.RuleID,
			Verdict:              compliance.Verdict,
			Rationale:            rationale,
			RationaleOverflowRef: overflow,
			OutputSha256:         in.OutputSha256,
			RubricVersion:        compliance.RubricVersion,
			PromptVersion:        compliance.PromptVersion,
			ResolvedAt:           compliance.ResolvedAt,
		}
		if in.RubricVersion != "" {
			row.RubricVersion = in.RubricVersion
		}
		if in.PromptVersion != "" {
			row.PromptVersion = in.PromptVersion
		}
		if role == contracts.JudgeRoleArbiter {
			row.PrimaryRef = primaryRefs[compliance.RuleID]
			row.SecondaryRef = secondaryRefs[compliance.RuleID]
		}
		if err := row.Validate(); err != nil {
			return nil, fmt.Errorf("scorecore: raw compliance row: %w", err)
		}
		rows = append(rows, row)
	}
	return rows, nil
}
