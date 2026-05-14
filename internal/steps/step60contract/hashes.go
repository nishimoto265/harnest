package step60contract

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"

	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/nishimoto265/harnest/internal/judges"
	"github.com/nishimoto265/harnest/internal/steps/scorecore"
)

type pairwiseKey struct {
	AgentA contracts.AgentID
	AgentB contracts.AgentID
}

type expectedComplianceAgentState struct {
	Agent   contracts.AgentID `json:"agent"`
	RuleIDs []string          `json:"rule_ids"`
}

type pass2OutputHashState struct {
	Agent        contracts.AgentID `json:"agent"`
	OutputSha256 string            `json:"output_sha256"`
}

func InputHashesByAgent(
	pass1ScoresByAgent map[contracts.AgentID][]contracts.ScoreEntry,
	pass1ComplianceRows []contracts.ComplianceEntry,
	pass2OutputHashes map[contracts.AgentID]string,
	candidateRules []judges.CandidateRule,
	expectedComplianceByAgent map[contracts.AgentID]map[string]struct{},
) (contracts.Step60DoneInputHashes, error) {
	rows := make([]contracts.ScoreEntry, 0)
	for _, scores := range pass1ScoresByAgent {
		rows = append(rows, scores...)
	}
	return InputHashes(rows, pass1ComplianceRows, pass2OutputHashes, candidateRules, expectedComplianceByAgent)
}

func InputHashes(
	pass1Scores []contracts.ScoreEntry,
	pass1ComplianceRows []contracts.ComplianceEntry,
	pass2OutputHashes map[contracts.AgentID]string,
	candidateRules []judges.CandidateRule,
	expectedComplianceByAgent map[contracts.AgentID]map[string]struct{},
) (contracts.Step60DoneInputHashes, error) {
	pass1ScoresHash, err := HashPass1Scores(pass1Scores)
	if err != nil {
		return contracts.Step60DoneInputHashes{}, err
	}
	pass1ComplianceHash, err := HashFinalCompliance(pass1ComplianceRows)
	if err != nil {
		return contracts.Step60DoneInputHashes{}, err
	}
	pass2OutputsHash, err := HashPass2OutputHashes(pass2OutputHashes)
	if err != nil {
		return contracts.Step60DoneInputHashes{}, err
	}
	candidateRulesHash, err := HashCandidateRules(candidateRules)
	if err != nil {
		return contracts.Step60DoneInputHashes{}, err
	}
	expectedComplianceHash, err := HashExpectedCompliance(expectedComplianceByAgent)
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

func HashPass1ScoresByAgent(byAgent map[contracts.AgentID][]contracts.ScoreEntry) (string, error) {
	rows := make([]contracts.ScoreEntry, 0)
	for _, scores := range byAgent {
		rows = append(rows, scores...)
	}
	return HashPass1Scores(rows)
}

func HashPass1Scores(rows []contracts.ScoreEntry) (string, error) {
	sorted := append([]contracts.ScoreEntry(nil), rows...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Agent != sorted[j].Agent {
			return sorted[i].Agent < sorted[j].Agent
		}
		return sorted[i].Dimension < sorted[j].Dimension
	})
	return HashCanonicalRows(sorted)
}

func HashPass2OutputHashes(hashes map[contracts.AgentID]string) (string, error) {
	rows := make([]pass2OutputHashState, 0, len(hashes))
	for agent, outputHash := range hashes {
		rows = append(rows, pass2OutputHashState{Agent: agent, OutputSha256: outputHash})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Agent < rows[j].Agent })
	return HashCanonicalRows(rows)
}

func HashCandidateRules(rules []judges.CandidateRule) (string, error) {
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
	return HashCanonicalRows(copied)
}

func HashExpectedCompliance(byAgent map[contracts.AgentID]map[string]struct{}) (string, error) {
	rows := make([]expectedComplianceAgentState, 0, len(byAgent))
	for agent, rules := range byAgent {
		rows = append(rows, expectedComplianceAgentState{
			Agent:   agent,
			RuleIDs: SortedRuleIDs(rules),
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Agent < rows[j].Agent })
	return HashCanonicalRows(rows)
}

func RawScoreEntryHash(entry contracts.RawScoreEntry) (string, error) {
	return CanonicalSHA256(entry)
}

func RawComplianceEntryHash(entry contracts.RawComplianceEntry) (string, error) {
	return CanonicalSHA256(entry)
}

func HashFinalScores(entries []contracts.ScoreEntry) (string, error) {
	_, hash, err := FinalScoresState(entries)
	return hash, err
}

func HashFinalCompliance(entries []contracts.ComplianceEntry) (string, error) {
	_, hash, err := FinalComplianceState(entries)
	return hash, err
}

func HashFinalPairwise(entries []contracts.PairwiseEntry) (string, error) {
	_, hash, err := FinalPairwiseState(entries)
	return hash, err
}

func FinalScoresState(entries []contracts.ScoreEntry) (int, string, error) {
	return finalScoresState(collapseFinalScores(entries))
}

func FinalScoresStateWithOverflowRefs(runIO internalio.RunContext, entries []contracts.ScoreEntry) (int, string, error) {
	collapsed := collapseFinalScores(entries)
	if err := ValidateScoreOverflowRefs(runIO, collapsed); err != nil {
		return 0, "", err
	}
	return finalScoresState(collapsed)
}

func FinalComplianceState(entries []contracts.ComplianceEntry) (int, string, error) {
	return finalComplianceState(collapseFinalCompliance(entries))
}

func FinalComplianceStateWithOverflowRefs(runIO internalio.RunContext, entries []contracts.ComplianceEntry) (int, string, error) {
	collapsed := collapseFinalCompliance(entries)
	if err := ValidateComplianceOverflowRefs(runIO, collapsed); err != nil {
		return 0, "", err
	}
	return finalComplianceState(collapsed)
}

func FinalPairwiseState(entries []contracts.PairwiseEntry) (int, string, error) {
	return finalPairwiseState(collapseFinalPairwise(entries))
}

func FinalPairwiseStateWithOverflowRefs(runIO internalio.RunContext, entries []contracts.PairwiseEntry) (int, string, error) {
	collapsed := collapseFinalPairwise(entries)
	if err := ValidatePairwiseOverflowRefs(runIO, collapsed); err != nil {
		return 0, "", err
	}
	return finalPairwiseState(collapsed)
}

func collapseFinalPairwise(entries []contracts.PairwiseEntry) []contracts.PairwiseEntry {
	collapsed := internalio.CollapseByKey(entries, func(entry contracts.PairwiseEntry) pairwiseKey {
		return pairwiseKey{AgentA: entry.AgentA, AgentB: entry.AgentB}
	})
	sort.Slice(collapsed, func(i, j int) bool {
		if collapsed[i].AgentA != collapsed[j].AgentA {
			return collapsed[i].AgentA < collapsed[j].AgentA
		}
		return collapsed[i].AgentB < collapsed[j].AgentB
	})
	return collapsed
}

func finalPairwiseState(collapsed []contracts.PairwiseEntry) (int, string, error) {
	hash, err := HashCanonicalRows(collapsed)
	return len(collapsed), hash, err
}

func collapseFinalScores(entries []contracts.ScoreEntry) []contracts.ScoreEntry {
	collapsed := scorecore.CollapseFinalScores(entries)
	sort.Slice(collapsed, func(i, j int) bool {
		if collapsed[i].Agent != collapsed[j].Agent {
			return collapsed[i].Agent < collapsed[j].Agent
		}
		return collapsed[i].Dimension < collapsed[j].Dimension
	})
	return collapsed
}

func finalScoresState(collapsed []contracts.ScoreEntry) (int, string, error) {
	hash, err := HashCanonicalRows(collapsed)
	return len(collapsed), hash, err
}

func collapseFinalCompliance(entries []contracts.ComplianceEntry) []contracts.ComplianceEntry {
	collapsed := scorecore.CollapseFinalCompliance(entries)
	sort.Slice(collapsed, func(i, j int) bool {
		if collapsed[i].Agent != collapsed[j].Agent {
			return collapsed[i].Agent < collapsed[j].Agent
		}
		return collapsed[i].RuleID < collapsed[j].RuleID
	})
	return collapsed
}

func finalComplianceState(collapsed []contracts.ComplianceEntry) (int, string, error) {
	hash, err := HashCanonicalRows(collapsed)
	return len(collapsed), hash, err
}

func HashReducedRawScoresFile(runIO internalio.RunContext, path string) (string, error) {
	rows, err := internalio.ReadJSONL[contracts.RawScoreEntry](path)
	if err != nil {
		return "", fmt.Errorf("read raw scores: %w", err)
	}
	reduced := ReduceRawScores(rows)
	if err := ValidateRawScoreOverflowRefs(runIO, reduced); err != nil {
		return "", err
	}
	return HashCanonicalRows(reduced)
}

func HashReducedRawComplianceFile(runIO internalio.RunContext, path string) (string, error) {
	rows, err := internalio.ReadJSONL[contracts.RawComplianceEntry](path)
	if err != nil {
		return "", fmt.Errorf("read raw compliance: %w", err)
	}
	reduced := ReduceRawCompliance(rows)
	if err := ValidateRawComplianceOverflowRefs(runIO, reduced); err != nil {
		return "", err
	}
	return HashCanonicalRows(reduced)
}

func ValidateScoreOverflowRefs(runIO internalio.RunContext, rows []contracts.ScoreEntry) error {
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

func ValidateComplianceOverflowRefs(runIO internalio.RunContext, rows []contracts.ComplianceEntry) error {
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

func ValidateRawScoreOverflowRefs(runIO internalio.RunContext, rows []contracts.RawScoreEntry) error {
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

func ValidateRawComplianceOverflowRefs(runIO internalio.RunContext, rows []contracts.RawComplianceEntry) error {
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

func ValidatePairwiseOverflowRefs(runIO internalio.RunContext, rows []contracts.PairwiseEntry) error {
	for _, row := range rows {
		if row.JustificationOverflowRef == nil {
			continue
		}
		if _, err := internalio.ReadSidecar(runIO, *row.JustificationOverflowRef); err != nil {
			return err
		}
	}
	return nil
}

func ReduceRawScores(rows []contracts.RawScoreEntry) []contracts.RawScoreEntry {
	return scorecore.CollapseRawScores(rows)
}

func ReduceRawCompliance(rows []contracts.RawComplianceEntry) []contracts.RawComplianceEntry {
	return scorecore.CollapseRawCompliance(rows)
}

func FileSHA256(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return SHA256Hex(data), nil
}

func HashCanonicalRows[T any](rows []T) (string, error) {
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
	return SHA256Hex(joined), nil
}

func CanonicalSHA256(v any) (string, error) {
	data, err := contracts.CanonicalMarshal(v)
	if err != nil {
		return "", err
	}
	return SHA256Hex(data), nil
}

func SHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func ExpectedComplianceRuleIDsForAgent(
	agent contracts.AgentID,
	pass1Rules map[contracts.AgentID]map[string]struct{},
	activeRules []string,
	fallbackRules []string,
	candidateRules []judges.CandidateRule,
) map[string]struct{} {
	rules := make(map[string]struct{})
	for _, ruleID := range activeRules {
		rules[ruleID] = struct{}{}
	}
	for ruleID := range pass1Rules[agent] {
		rules[ruleID] = struct{}{}
	}
	for _, rule := range candidateRules {
		rules[rule.ID] = struct{}{}
	}
	if len(rules) == 0 {
		for _, ruleID := range fallbackRules {
			rules[ruleID] = struct{}{}
		}
	}
	if len(rules) == 0 {
		return nil
	}
	return rules
}

func ExpectedComplianceRuleIDsByAgent(
	agents []contracts.AgentID,
	pass1Rules map[contracts.AgentID]map[string]struct{},
	activeRules []string,
	fallbackRules []string,
	candidateRules []judges.CandidateRule,
) map[contracts.AgentID]map[string]struct{} {
	byAgent := make(map[contracts.AgentID]map[string]struct{}, len(agents))
	for _, agent := range agents {
		byAgent[agent] = ExpectedComplianceRuleIDsForAgent(agent, pass1Rules, activeRules, fallbackRules, candidateRules)
	}
	return byAgent
}

func SortedRuleIDs(rules map[string]struct{}) []string {
	if len(rules) == 0 {
		return nil
	}
	ruleIDs := make([]string, 0, len(rules))
	for ruleID := range rules {
		ruleIDs = append(ruleIDs, ruleID)
	}
	sort.Strings(ruleIDs)
	return ruleIDs
}
