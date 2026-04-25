package step60_scorepairwise

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/judges"
	"github.com/nishimoto265/auto-improve/internal/steps/scorecore"
)

type expectedComplianceAgentState struct {
	Agent   contracts.AgentID `json:"agent"`
	RuleIDs []string          `json:"rule_ids"`
}

type pass2OutputHashState struct {
	Agent        contracts.AgentID `json:"agent"`
	OutputSha256 string            `json:"output_sha256"`
}

func step60InputHashes(
	pass1ScoresByAgent map[contracts.AgentID][]contracts.ScoreEntry,
	pass1ComplianceRows []contracts.ComplianceEntry,
	pass2OutputHashes map[contracts.AgentID]string,
	candidateRules []judges.CandidateRule,
	expectedComplianceByAgent map[contracts.AgentID]map[string]struct{},
) (contracts.Step60DoneInputHashes, error) {
	pass1ScoresHash, err := hashPass1Scores(pass1ScoresByAgent)
	if err != nil {
		return contracts.Step60DoneInputHashes{}, err
	}
	pass1ComplianceHash, err := hashFinalCompliance(pass1ComplianceRows)
	if err != nil {
		return contracts.Step60DoneInputHashes{}, err
	}
	pass2OutputsHash, err := hashPass2OutputHashes(pass2OutputHashes)
	if err != nil {
		return contracts.Step60DoneInputHashes{}, err
	}
	candidateRulesHash, err := hashCandidateRules(candidateRules)
	if err != nil {
		return contracts.Step60DoneInputHashes{}, err
	}
	expectedComplianceHash, err := hashExpectedCompliance(expectedComplianceByAgent)
	if err != nil {
		return contracts.Step60DoneInputHashes{}, err
	}
	return contracts.Step60DoneInputHashes{
		Pass1Scores:        pass1ScoresHash,
		Pass1Compliance:    pass1ComplianceHash,
		Pass2Outputs:       pass2OutputsHash,
		CandidateRules:     candidateRulesHash,
		ExpectedCompliance: expectedComplianceHash,
	}, nil
}

func hashPass1Scores(byAgent map[contracts.AgentID][]contracts.ScoreEntry) (string, error) {
	rows := make([]contracts.ScoreEntry, 0)
	for _, scores := range byAgent {
		rows = append(rows, scores...)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Agent != rows[j].Agent {
			return rows[i].Agent < rows[j].Agent
		}
		return rows[i].Dimension < rows[j].Dimension
	})
	return hashCanonicalRows(rows)
}

func hashPass2OutputHashes(hashes map[contracts.AgentID]string) (string, error) {
	rows := make([]pass2OutputHashState, 0, len(hashes))
	for agent, outputHash := range hashes {
		rows = append(rows, pass2OutputHashState{Agent: agent, OutputSha256: outputHash})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Agent < rows[j].Agent })
	return hashCanonicalRows(rows)
}

func hashCandidateRules(rules []judges.CandidateRule) (string, error) {
	copied := append([]judges.CandidateRule(nil), rules...)
	sort.Slice(copied, func(i, j int) bool {
		if copied[i].ID != copied[j].ID {
			return copied[i].ID < copied[j].ID
		}
		if copied[i].Kind != copied[j].Kind {
			return copied[i].Kind < copied[j].Kind
		}
		if copied[i].TargetRuleID != copied[j].TargetRuleID {
			return copied[i].TargetRuleID < copied[j].TargetRuleID
		}
		if copied[i].Title != copied[j].Title {
			return copied[i].Title < copied[j].Title
		}
		return copied[i].Body < copied[j].Body
	})
	return hashCanonicalRows(copied)
}

func hashExpectedCompliance(byAgent map[contracts.AgentID]map[string]struct{}) (string, error) {
	rows := make([]expectedComplianceAgentState, 0, len(byAgent))
	for agent, rules := range byAgent {
		rows = append(rows, expectedComplianceAgentState{
			Agent:   agent,
			RuleIDs: sortedExpectedComplianceRuleIDs(rules),
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Agent < rows[j].Agent })
	return hashCanonicalRows(rows)
}

func rawScoreEntryHash(entry contracts.RawScoreEntry) (string, error) {
	return canonicalSHA256(entry)
}

func rawComplianceEntryHash(entry contracts.RawComplianceEntry) (string, error) {
	return canonicalSHA256(entry)
}

func hashFinalScores(entries []contracts.ScoreEntry) (string, error) {
	collapsed := internalio.CollapseByKey(entries, func(entry contracts.ScoreEntry) scoreKey {
		return scoreKey{Agent: entry.Agent, Dimension: entry.Dimension}
	})
	sort.Slice(collapsed, func(i, j int) bool {
		if collapsed[i].Agent != collapsed[j].Agent {
			return collapsed[i].Agent < collapsed[j].Agent
		}
		return collapsed[i].Dimension < collapsed[j].Dimension
	})
	return hashCanonicalRows(collapsed)
}

func hashFinalCompliance(entries []contracts.ComplianceEntry) (string, error) {
	collapsed := internalio.CollapseByKey(entries, func(entry contracts.ComplianceEntry) complianceKey {
		return complianceKey{Agent: entry.Agent, RuleID: entry.RuleID}
	})
	sort.Slice(collapsed, func(i, j int) bool {
		if collapsed[i].Agent != collapsed[j].Agent {
			return collapsed[i].Agent < collapsed[j].Agent
		}
		return collapsed[i].RuleID < collapsed[j].RuleID
	})
	return hashCanonicalRows(collapsed)
}

func hashFinalPairwise(entries []contracts.PairwiseEntry) (string, error) {
	collapsed := internalio.CollapseByKey(entries, func(entry contracts.PairwiseEntry) complianceKey {
		return complianceKey{Agent: entry.AgentA, RuleID: string(entry.AgentB)}
	})
	sort.Slice(collapsed, func(i, j int) bool {
		if collapsed[i].AgentA != collapsed[j].AgentA {
			return collapsed[i].AgentA < collapsed[j].AgentA
		}
		return collapsed[i].AgentB < collapsed[j].AgentB
	})
	return hashCanonicalRows(collapsed)
}

func hashReducedRawScoresFile(runIO internalio.RunContext, path string) (string, error) {
	rows, err := internalio.ReadJSONL[contracts.RawScoreEntry](path)
	if err != nil {
		return "", fmt.Errorf("read raw scores: %w", err)
	}
	reduced := reduceRawScores(rows)
	if err := validateRawScoreOverflowRefs(runIO, reduced); err != nil {
		return "", err
	}
	return hashCanonicalRows(reduced)
}

func hashReducedRawComplianceFile(runIO internalio.RunContext, path string) (string, error) {
	rows, err := internalio.ReadJSONL[contracts.RawComplianceEntry](path)
	if err != nil {
		return "", fmt.Errorf("read raw compliance: %w", err)
	}
	reduced := reduceRawCompliance(rows)
	if err := validateRawComplianceOverflowRefs(runIO, reduced); err != nil {
		return "", err
	}
	return hashCanonicalRows(reduced)
}

func validateScoreOverflowRefs(runIO internalio.RunContext, rows []contracts.ScoreEntry) error {
	for _, row := range rows {
		if row.ReasonsOverflowRef == nil {
			continue
		}
		if _, err := internalio.ReadSidecar(runIO, *row.ReasonsOverflowRef); err != nil {
			return err
		}
	}
	return nil
}

func validateComplianceOverflowRefs(runIO internalio.RunContext, rows []contracts.ComplianceEntry) error {
	for _, row := range rows {
		if row.RationaleOverflowRef == nil {
			continue
		}
		if _, err := internalio.ReadSidecar(runIO, *row.RationaleOverflowRef); err != nil {
			return err
		}
	}
	return nil
}

func validateRawScoreOverflowRefs(runIO internalio.RunContext, rows []contracts.RawScoreEntry) error {
	for _, row := range rows {
		if row.ReasonsOverflowRef == nil {
			continue
		}
		if _, err := internalio.ReadSidecar(runIO, *row.ReasonsOverflowRef); err != nil {
			return err
		}
	}
	return nil
}

func validateRawComplianceOverflowRefs(runIO internalio.RunContext, rows []contracts.RawComplianceEntry) error {
	for _, row := range rows {
		if row.RationaleOverflowRef == nil {
			continue
		}
		if _, err := internalio.ReadSidecar(runIO, *row.RationaleOverflowRef); err != nil {
			return err
		}
	}
	return nil
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
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return sha256Hex(data), nil
}

func reduceRawScores(rows []contracts.RawScoreEntry) []contracts.RawScoreEntry {
	return scorecore.CollapseRawScores(rows)
}

func reduceRawCompliance(rows []contracts.RawComplianceEntry) []contracts.RawComplianceEntry {
	return scorecore.CollapseRawCompliance(rows)
}

func hashCanonicalRows[T any](rows []T) (string, error) {
	joined := make([]byte, 0)
	for i, row := range rows {
		payload, err := contracts.CanonicalMarshal(row)
		if err != nil {
			return "", err
		}
		if i > 0 {
			joined = append(joined, 0x00)
		}
		joined = append(joined, payload...)
	}
	return sha256Hex(joined), nil
}

func canonicalSHA256(v any) (string, error) {
	data, err := contracts.CanonicalMarshal(v)
	if err != nil {
		return "", err
	}
	return sha256Hex(data), nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
