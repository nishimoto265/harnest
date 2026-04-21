package step50_implement

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
)

// RulePayload is the prompt-ready rule body loaded from <runs_base>/rules/<id>.md.
type RulePayload struct {
	ID   string
	Text string
}

// LoadRulePayloads resolves candidate rule ids and reads their sidecar bodies.
//
// Phase 1-C note: the current frozen contracts.Candidate schema does not expose
// a RuleID field, so candidates.json derivation falls back to TargetRuleID.
func LoadRulePayloads(candidateRuleIDs []string, runsBase, candidatesPath string) ([]RulePayload, error) {
	ruleIDs := dedupeRuleIDs(candidateRuleIDs)
	if len(ruleIDs) == 0 {
		var err error
		ruleIDs, err = loadCandidateRuleIDsFromFile(candidatesPath)
		if err != nil {
			return nil, err
		}
	}

	payloads := make([]RulePayload, 0, len(ruleIDs))
	for _, ruleID := range ruleIDs {
		rulePath := filepath.Join(runsBase, "rules", ruleID+".md")
		body, err := os.ReadFile(rulePath)
		switch {
		case err == nil:
			payloads = append(payloads, RulePayload{ID: ruleID, Text: string(body)})
		case os.IsNotExist(err):
			payloads = append(payloads, RulePayload{ID: ruleID, Text: ""})
		default:
			return nil, fmt.Errorf("read rule payload %q: %w", ruleID, err)
		}
	}
	return payloads, nil
}

func loadCandidateRuleIDsFromFile(candidatesPath string) ([]string, error) {
	candidates, err := internalio.ReadJSON[contracts.Candidates](candidatesPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read candidates: %w", err)
	}

	ids := make([]string, 0, len(candidates.Candidates))
	for _, candidate := range candidates.Candidates {
		if candidate.TargetRuleID == "" {
			continue
		}
		ids = append(ids, candidate.TargetRuleID)
	}
	return dedupeRuleIDs(ids), nil
}

func dedupeRuleIDs(ruleIDs []string) []string {
	if len(ruleIDs) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(ruleIDs))
	out := make([]string, 0, len(ruleIDs))
	for _, ruleID := range ruleIDs {
		if ruleID == "" {
			continue
		}
		if _, exists := seen[ruleID]; exists {
			continue
		}
		seen[ruleID] = struct{}{}
		out = append(out, ruleID)
	}
	return out
}
