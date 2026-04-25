package step60_scorepairwise

import (
	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/steps/scorecore"
)

func step60VersionsMatch(paths step60Paths, rubricVersion, promptVersion string) (bool, error) {
	scoreRaw, err := readJSONLOrEmpty[contracts.RawScoreEntry](paths.ScoresRaw)
	if err != nil {
		return false, err
	}
	complianceRaw, err := readJSONLOrEmpty[contracts.RawComplianceEntry](paths.ComplianceRaw)
	if err != nil {
		return false, err
	}
	scoreFinal, err := readJSONLOrEmpty[contracts.ScoreEntry](paths.ScoresFinal)
	if err != nil {
		return false, err
	}
	complianceFinal, err := readJSONLOrEmpty[contracts.ComplianceEntry](paths.ComplianceFinal)
	if err != nil {
		return false, err
	}
	pairwiseRows, err := readJSONLOrEmpty[contracts.PairwiseEntry](paths.Pairwise)
	if err != nil {
		return false, err
	}
	return scorecore.RowsMatchVersion(reduceRawScores(scoreRaw), func(row contracts.RawScoreEntry) (string, string) {
		return row.RubricVersion, row.PromptVersion
	}, rubricVersion, promptVersion) &&
		scorecore.RowsMatchVersion(reduceRawCompliance(complianceRaw), func(row contracts.RawComplianceEntry) (string, string) {
			return row.RubricVersion, row.PromptVersion
		}, rubricVersion, promptVersion) &&
		scorecore.RowsMatchVersion(scoreFinal, func(row contracts.ScoreEntry) (string, string) {
			return row.RubricVersion, row.PromptVersion
		}, rubricVersion, promptVersion) &&
		scorecore.RowsMatchVersion(complianceFinal, func(row contracts.ComplianceEntry) (string, string) {
			return row.RubricVersion, row.PromptVersion
		}, rubricVersion, promptVersion) &&
		scorecore.RowsMatchVersion(pairwiseRows, func(row contracts.PairwiseEntry) (string, string) {
			return row.RubricVersion, row.PromptVersion
		}, rubricVersion, promptVersion), nil
}
