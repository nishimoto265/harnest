package step60_scorepairwise

import (
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/judges"
	"github.com/nishimoto265/auto-improve/internal/steps/step60contract"
)

func step60InputHashes(
	pass1ScoresByAgent map[contracts.AgentID][]contracts.ScoreEntry,
	pass1ComplianceRows []contracts.ComplianceEntry,
	pass2OutputHashes map[contracts.AgentID]string,
	candidateRules []judges.CandidateRule,
	expectedComplianceByAgent map[contracts.AgentID]map[string]struct{},
) (contracts.Step60DoneInputHashes, error) {
	return step60contract.InputHashesByAgent(pass1ScoresByAgent, pass1ComplianceRows, pass2OutputHashes, candidateRules, expectedComplianceByAgent)
}

func hashPass1Scores(byAgent map[contracts.AgentID][]contracts.ScoreEntry) (string, error) {
	return step60contract.HashPass1ScoresByAgent(byAgent)
}

func hashPass2OutputHashes(hashes map[contracts.AgentID]string) (string, error) {
	return step60contract.HashPass2OutputHashes(hashes)
}

func hashCandidateRules(rules []judges.CandidateRule) (string, error) {
	return step60contract.HashCandidateRules(rules)
}

func hashExpectedCompliance(byAgent map[contracts.AgentID]map[string]struct{}) (string, error) {
	return step60contract.HashExpectedCompliance(byAgent)
}

func rawScoreEntryHash(entry contracts.RawScoreEntry) (string, error) {
	return step60contract.RawScoreEntryHash(entry)
}

func rawComplianceEntryHash(entry contracts.RawComplianceEntry) (string, error) {
	return step60contract.RawComplianceEntryHash(entry)
}

func hashFinalScores(entries []contracts.ScoreEntry) (string, error) {
	return step60contract.HashFinalScores(entries)
}

func hashFinalCompliance(entries []contracts.ComplianceEntry) (string, error) {
	return step60contract.HashFinalCompliance(entries)
}

func hashFinalPairwise(entries []contracts.PairwiseEntry) (string, error) {
	return step60contract.HashFinalPairwise(entries)
}

func hashReducedRawScoresFile(runIO internalio.RunContext, path string) (string, error) {
	return step60contract.HashReducedRawScoresFile(runIO, path)
}

func hashReducedRawComplianceFile(runIO internalio.RunContext, path string) (string, error) {
	return step60contract.HashReducedRawComplianceFile(runIO, path)
}

func validateScoreOverflowRefs(runIO internalio.RunContext, rows []contracts.ScoreEntry) error {
	return step60contract.ValidateScoreOverflowRefs(runIO, rows)
}

func validateComplianceOverflowRefs(runIO internalio.RunContext, rows []contracts.ComplianceEntry) error {
	return step60contract.ValidateComplianceOverflowRefs(runIO, rows)
}

func validateRawScoreOverflowRefs(runIO internalio.RunContext, rows []contracts.RawScoreEntry) error {
	return step60contract.ValidateRawScoreOverflowRefs(runIO, rows)
}

func validateRawComplianceOverflowRefs(runIO internalio.RunContext, rows []contracts.RawComplianceEntry) error {
	return step60contract.ValidateRawComplianceOverflowRefs(runIO, rows)
}

func buildRawScores(
	scores map[contracts.Dimension]contracts.ScoreEntry,
	outputHash string,
	role contracts.JudgeRole,
	primaryRefs map[contracts.Dimension]*contracts.RawJudgeRef,
	secondaryRefs map[contracts.Dimension]*contracts.RawJudgeRef,
	resolvedAt time.Time,
) ([]contracts.RawScoreEntry, map[contracts.Dimension]*contracts.RawJudgeRef, error) {
	rows := make([]contracts.RawScoreEntry, 0, len(scores))
	refs := make(map[contracts.Dimension]*contracts.RawJudgeRef, len(scores))
	for _, dimension := range canonicalDimensions {
		score, ok := scores[dimension]
		if !ok {
			continue
		}
		row := makeRawScoreEntry(score, role, outputHash, primaryRefs[dimension], secondaryRefs[dimension], resolvedAt)
		hash, err := rawScoreEntryHash(row)
		if err != nil {
			return nil, nil, err
		}
		refs[dimension] = &contracts.RawJudgeRef{Role: role, Sha256: hash}
		rows = append(rows, row)
	}
	return rows, refs, nil
}

func fileSHA256(path string) (string, error) {
	return step60contract.FileSHA256(path)
}

func reduceRawScores(rows []contracts.RawScoreEntry) []contracts.RawScoreEntry {
	return step60contract.ReduceRawScores(rows)
}

func reduceRawCompliance(rows []contracts.RawComplianceEntry) []contracts.RawComplianceEntry {
	return step60contract.ReduceRawCompliance(rows)
}

func hashCanonicalRows[T any](rows []T) (string, error) {
	return step60contract.HashCanonicalRows(rows)
}

func canonicalSHA256(v any) (string, error) {
	return step60contract.CanonicalSHA256(v)
}

func sha256Hex(data []byte) string {
	return step60contract.SHA256Hex(data)
}
