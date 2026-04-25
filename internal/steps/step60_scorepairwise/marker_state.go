package step60_scorepairwise

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/steps/step60contract"
)

func doneMarkerMatchesCurrentState(runIO internalio.RunContext, paths step60Paths, expectedAgents []contracts.AgentID, inputHashes contracts.Step60DoneInputHashes) (bool, bool, error) {
	marker, err := internalio.ReadJSON[contracts.Step60DoneMarker](paths.Done)
	if err != nil {
		return false, false, nil
	}
	if err := marker.Validate(); err != nil {
		return false, false, nil
	}
	if !slices.Equal(marker.Dimensions, canonicalDimensions) {
		return false, judgeInputHashesMatch(marker.InputHashes, inputHashes), nil
	}
	normalizedExpectedAgents := append([]contracts.AgentID(nil), expectedAgents...)
	sort.Slice(normalizedExpectedAgents, func(i, j int) bool { return normalizedExpectedAgents[i] < normalizedExpectedAgents[j] })
	if !slices.Equal(marker.CompletedAgents, normalizedExpectedAgents) {
		return false, judgeInputHashesMatch(marker.InputHashes, inputHashes), nil
	}
	inputsMatch := marker.InputHashes == inputHashes

	scoresFinalCount, scoresFinalHash, err := currentFinalScoresState(runIO, paths.ScoresFinal)
	if err != nil {
		if overflowValidationFailure(err) {
			return false, inputsMatch, nil
		}
		return false, inputsMatch, fmt.Errorf("step60: inspect scores final: %w", err)
	}
	complianceFinalCount, complianceFinalHash, err := currentFinalComplianceState(runIO, paths.ComplianceFinal)
	if err != nil {
		if overflowValidationFailure(err) {
			return false, inputsMatch, nil
		}
		return false, inputsMatch, fmt.Errorf("step60: inspect compliance final: %w", err)
	}
	pairwiseCount, pairwiseHash, err := currentPairwiseState(paths.Pairwise)
	if err != nil {
		if os.IsNotExist(err) {
			return false, inputsMatch, nil
		}
		return false, inputsMatch, fmt.Errorf("step60: inspect pairwise final: %w", err)
	}
	scoresRawHash, err := hashReducedRawScoresFile(runIO, paths.ScoresRaw)
	if err != nil {
		if overflowValidationFailure(err) {
			return false, inputsMatch, nil
		}
		return false, inputsMatch, fmt.Errorf("step60: hash scores raw: %w", err)
	}
	complianceRawHash, err := hashReducedRawComplianceFile(runIO, paths.ComplianceRaw)
	if err != nil {
		if overflowValidationFailure(err) {
			return false, inputsMatch, nil
		}
		return false, inputsMatch, fmt.Errorf("step60: hash compliance raw: %w", err)
	}

	return marker.ExpectedCounts.Scores == int64(scoresFinalCount) &&
		marker.ExpectedCounts.Compliance == int64(complianceFinalCount) &&
		marker.ExpectedCounts.Pairwise == int64(pairwiseCount) &&
		inputsMatch &&
		marker.ContentHashes.ScoresFinal == scoresFinalHash &&
		marker.ContentHashes.ComplianceFinal == complianceFinalHash &&
		marker.ContentHashes.PairwiseFinal == pairwiseHash &&
		marker.RawHashes.ScoresRaw == scoresRawHash &&
		marker.RawHashes.ComplianceRaw == complianceRawHash, judgeInputHashesMatch(marker.InputHashes, inputHashes), nil
}

func rawReuseMarkerMatchesCurrentState(runIO internalio.RunContext, paths step60Paths, expectedAgents []contracts.AgentID, inputHashes contracts.Step60DoneInputHashes) (bool, error) {
	marker, err := internalio.ReadJSON[step60RawReuseMarker](paths.RawReuse)
	if err != nil {
		return false, nil
	}
	if !slices.Equal(marker.Dimensions, canonicalDimensions) {
		return false, nil
	}
	normalizedExpectedAgents := append([]contracts.AgentID(nil), expectedAgents...)
	sort.Slice(normalizedExpectedAgents, func(i, j int) bool { return normalizedExpectedAgents[i] < normalizedExpectedAgents[j] })
	if !slices.Equal(marker.CompletedAgents, normalizedExpectedAgents) {
		return false, nil
	}
	if !judgeInputHashesMatch(marker.InputHashes, inputHashes) {
		return false, nil
	}
	scoresRawHash, err := hashReducedRawScoresFile(runIO, paths.ScoresRaw)
	if err != nil {
		if overflowValidationFailure(err) {
			return false, nil
		}
		return false, fmt.Errorf("step60: hash scores raw for raw reuse marker: %w", err)
	}
	complianceRawHash, err := hashReducedRawComplianceFile(runIO, paths.ComplianceRaw)
	if err != nil {
		if overflowValidationFailure(err) {
			return false, nil
		}
		return false, fmt.Errorf("step60: hash compliance raw for raw reuse marker: %w", err)
	}
	return marker.RawHashes.ScoresRaw == scoresRawHash && marker.RawHashes.ComplianceRaw == complianceRawHash, nil
}

func judgeInputHashesMatch(left, right contracts.Step60DoneInputHashes) bool {
	return left == right
}

func overflowValidationFailure(err error) bool {
	return errors.Is(err, internalio.ErrSidecarDigestMismatch) || os.IsNotExist(err)
}

func currentFinalScoresState(runIO internalio.RunContext, path string) (int, string, error) {
	rows, err := internalio.ReadJSONL[contracts.ScoreEntry](path)
	if err != nil {
		return 0, "", err
	}
	return step60contract.FinalScoresStateWithOverflowRefs(runIO, rows)
}

func currentFinalComplianceState(runIO internalio.RunContext, path string) (int, string, error) {
	rows, err := internalio.ReadJSONL[contracts.ComplianceEntry](path)
	if err != nil {
		return 0, "", err
	}
	return step60contract.FinalComplianceStateWithOverflowRefs(runIO, rows)
}

func currentPairwiseState(path string) (int, string, error) {
	rows, err := internalio.ReadJSONL[contracts.PairwiseEntry](path)
	if err != nil {
		return 0, "", err
	}
	hash, err := hashFinalPairwise(rows)
	if err != nil {
		return 0, "", err
	}
	collapsed := internalio.CollapseByKey(rows, func(entry contracts.PairwiseEntry) complianceKey {
		return complianceKey{Agent: entry.AgentA, RuleID: string(entry.AgentB)}
	})
	return len(collapsed), hash, nil
}

func resetStep60Outputs(paths step60Paths) error {
	for _, path := range []string{
		paths.ScoresFinal,
		paths.ComplianceFinal,
		paths.Pairwise,
	} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("step60: reset output %s: %w", path, err)
		}
	}
	return nil
}
