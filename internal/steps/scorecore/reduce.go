package scorecore

import (
	"fmt"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
)

// CollapseFinalScores keeps only the latest final score row for each
// (agent, dimension) key.
func CollapseFinalScores(rows []contracts.ScoreEntry) []contracts.ScoreEntry {
	return internalio.CollapseByKey(rows, func(e contracts.ScoreEntry) [2]string {
		return [2]string{string(e.Agent), string(e.Dimension)}
	})
}

// CollapseFinalCompliance keeps only the latest final compliance row for each
// (agent, rule_id) key.
func CollapseFinalCompliance(rows []contracts.ComplianceEntry) []contracts.ComplianceEntry {
	return internalio.CollapseByKey(rows, func(e contracts.ComplianceEntry) [2]string {
		return [2]string{string(e.Agent), e.RuleID}
	})
}

// CollapseRawScores reduces raw score rows and drops stale arbiter rows whose
// refs no longer point at the latest primary/secondary rows.
func CollapseRawScores(rows []contracts.RawScoreEntry) []contracts.RawScoreEntry {
	collapsed := internalio.CollapseByKey(rows, func(e contracts.RawScoreEntry) [3]string {
		return [3]string{string(e.Agent), string(e.JudgeRole), string(e.Dimension)}
	})
	return keepFreshArbiterScores(collapsed)
}

// CollapseRawCompliance reduces raw compliance rows and drops stale arbiter
// rows whose refs no longer point at the latest primary/secondary rows.
func CollapseRawCompliance(rows []contracts.RawComplianceEntry) []contracts.RawComplianceEntry {
	collapsed := internalio.CollapseByKey(rows, func(e contracts.RawComplianceEntry) [3]string {
		return [3]string{string(e.Agent), string(e.JudgeRole), e.RuleID}
	})
	return keepFreshArbiterCompliance(collapsed)
}

// PanelDisagrees reports whether the primary/secondary panel requires an
// arbiter. Either score disagreement beyond threshold or compliance verdict
// disagreement is sufficient.
func PanelDisagrees(
	primaryScores, secondaryScores []contracts.RawScoreEntry,
	primaryCompliance, secondaryCompliance []contracts.RawComplianceEntry,
	threshold int,
) (bool, error) {
	scoreDisagree, err := anyDimensionDisagrees(primaryScores, secondaryScores, threshold)
	if err != nil {
		return false, err
	}
	if scoreDisagree {
		return true, nil
	}
	return anyComplianceVerdictDisagrees(primaryCompliance, secondaryCompliance)
}

func anyComplianceVerdictDisagrees(primary, secondary []contracts.RawComplianceEntry) (bool, error) {
	if len(primary) != len(secondary) {
		return false, fmt.Errorf("%w: primary compliance=%d secondary compliance=%d", ErrPanelDimensionMatch, len(primary), len(secondary))
	}
	secondaryByRule := make(map[string]contracts.ComplianceVerdict, len(secondary))
	for _, row := range secondary {
		secondaryByRule[row.RuleID] = row.Verdict
	}
	for _, row := range primary {
		verdict, ok := secondaryByRule[row.RuleID]
		if !ok {
			return false, fmt.Errorf("%w: secondary missing compliance rule_id=%s", ErrPanelDimensionMatch, row.RuleID)
		}
		if verdict != row.Verdict {
			return true, nil
		}
	}
	return false, nil
}
