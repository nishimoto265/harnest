package scorecore

import (
	"fmt"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
)

// ReduceRawScoreEntries collapses append-only raw score rows to latest-wins
// entries and drops arbiter rows that point at stale primary/secondary refs.
func ReduceRawScoreEntries(rows []contracts.RawScoreEntry) []contracts.RawScoreEntry {
	collapsed := internalio.CollapseByKey(rows, func(e contracts.RawScoreEntry) [3]string {
		return [3]string{string(e.Agent), string(e.JudgeRole), string(e.Dimension)}
	})
	return keepFreshArbiterScores(collapsed)
}

// ReduceRawComplianceEntries collapses append-only raw compliance rows to
// latest-wins entries and drops arbiter rows that point at stale
// primary/secondary refs.
func ReduceRawComplianceEntries(rows []contracts.RawComplianceEntry) []contracts.RawComplianceEntry {
	collapsed := internalio.CollapseByKey(rows, func(e contracts.RawComplianceEntry) [3]string {
		return [3]string{string(e.Agent), string(e.JudgeRole), e.RuleID}
	})
	return keepFreshArbiterCompliance(collapsed)
}

// PanelDisagrees reports whether the primary and secondary panel outputs
// disagree in either per-dimension score or per-rule compliance verdict.
func PanelDisagrees(
	primaryScores []contracts.RawScoreEntry,
	secondaryScores []contracts.RawScoreEntry,
	primaryCompliance []contracts.RawComplianceEntry,
	secondaryCompliance []contracts.RawComplianceEntry,
	threshold int,
) (bool, error) {
	disagree, err := anyDimensionDisagrees(primaryScores, secondaryScores, threshold)
	if err != nil {
		return false, err
	}
	if disagree {
		return true, nil
	}
	return anyComplianceDisagrees(primaryCompliance, secondaryCompliance), nil
}

// AssemblePanelResultFromRaw reconstructs the final panel result from reduced
// raw rows. When arbiter rows are absent it falls back to the agreement path,
// which matches the runtime behavior when no arbiter judge is configured.
func AssemblePanelResultFromRaw(
	primaryScores []contracts.RawScoreEntry,
	secondaryScores []contracts.RawScoreEntry,
	arbiterScores []contracts.RawScoreEntry,
	primaryCompliance []contracts.RawComplianceEntry,
	secondaryCompliance []contracts.RawComplianceEntry,
	arbiterCompliance []contracts.RawComplianceEntry,
	threshold int,
) (PanelResult, error) {
	if len(primaryScores) == 0 {
		return PanelResult{}, fmt.Errorf("%w: primary=%d secondary=%d", ErrPanelDimensionMatch, 0, len(secondaryScores))
	}
	if len(secondaryScores) == 0 {
		return assembleFinalFromRaw(primaryScores, primaryCompliance, contracts.VerdictPathSingle), nil
	}

	disagree, err := PanelDisagrees(primaryScores, secondaryScores, primaryCompliance, secondaryCompliance, threshold)
	if err != nil {
		return PanelResult{}, err
	}
	if !disagree || len(arbiterScores) == 0 {
		result := PanelResult{
			RawScores:     append(append([]contracts.RawScoreEntry{}, primaryScores...), secondaryScores...),
			RawCompliance: append(append([]contracts.RawComplianceEntry{}, primaryCompliance...), secondaryCompliance...),
			VerdictPath:   contracts.VerdictPathAgreement,
		}
		result.FinalScores = finalScoresFromRaw(primaryScores, result.VerdictPath)
		result.FinalCompliance = finalComplianceFromRaw(primaryCompliance, result.VerdictPath)
		return result, nil
	}

	verdict := classifyArbiterVerdict(primaryScores, secondaryScores, arbiterScores, threshold)
	return PanelResult{
		RawScores:       concatRawScores(primaryScores, secondaryScores, arbiterScores),
		RawCompliance:   concatRawCompliance(primaryCompliance, secondaryCompliance, arbiterCompliance),
		FinalScores:     finalScoresFromRaw(arbiterScores, verdict),
		FinalCompliance: finalComplianceFromRaw(arbiterCompliance, verdict),
		VerdictPath:     verdict,
	}, nil
}

func anyComplianceDisagrees(primary, secondary []contracts.RawComplianceEntry) bool {
	if len(primary) != len(secondary) {
		return true
	}

	secondaryByRule := make(map[string]contracts.ComplianceVerdict, len(secondary))
	for _, row := range secondary {
		secondaryByRule[row.RuleID] = row.Verdict
	}
	for _, row := range primary {
		verdict, ok := secondaryByRule[row.RuleID]
		if !ok || verdict != row.Verdict {
			return true
		}
	}
	return false
}
