package candidaterules

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"unicode"

	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/nishimoto265/harnest/internal/judges"
)

// RulePayload is the prompt-ready experiment lesson body loaded from the run's
// step40 sidecars. The type name is kept for the existing step50/60 boundary.
type RulePayload struct {
	ID           string
	Kind         string
	TargetRuleID string
	Title        string
	ProposedBody string
}

// LoadRulePayloads loads prompt payloads from candidates.json and each
// candidate's proposed_body_path sidecar under the run's 40/ directory.
func LoadRulePayloads(candidatesPath string) ([]RulePayload, error) {
	candidates, err := internalio.ReadJSON[contracts.Candidates](candidatesPath)
	if err != nil {
		return nil, fmt.Errorf("read candidates: %w", err)
	}
	if len(candidates.Candidates) == 0 {
		return nil, nil
	}

	runDir := filepath.Clean(filepath.Join(filepath.Dir(candidatesPath), ".."))
	payloads := make([]RulePayload, 0, len(candidates.Candidates))
	for _, candidate := range candidates.Candidates {
		if candidate.Kind == contracts.CandidateKindDuplicate {
			continue
		}
		if err := validatePromptIdentifier("candidate_id", candidate.CandidateID); err != nil {
			return nil, err
		}
		if candidate.TargetRuleID != "" {
			if err := validatePromptIdentifier("target_rule_id", candidate.TargetRuleID); err != nil {
				return nil, err
			}
		}
		if err := contracts.EnsureRelativePathUnderPrefix(candidate.ProposedBodyPath, "40"); err != nil {
			return nil, fmt.Errorf("candidate %q proposed_body_path: %w", candidate.CandidateID, err)
		}

		bodyPath := filepath.Join(runDir, candidate.ProposedBodyPath)
		body, err := internalio.OpenValidatedRegularFile(bodyPath, runDir)
		if err != nil {
			return nil, fmt.Errorf("read candidate %q proposed body: %w", candidate.CandidateID, err)
		}
		if err := verifyCandidateBodySHA(body, candidate.ProposedBodySha256); err != nil {
			return nil, fmt.Errorf("candidate %q proposed body sha256: %w", candidate.CandidateID, err)
		}

		payloads = append(payloads, RulePayload{
			ID:           candidate.CandidateID,
			Kind:         string(candidate.Kind),
			TargetRuleID: candidate.TargetRuleID,
			Title:        candidate.Title,
			ProposedBody: string(body),
		})
	}
	return payloads, nil
}

func ToJudgeRules(payloads []RulePayload) []judges.CandidateRule {
	if len(payloads) == 0 {
		return nil
	}
	rules := make([]judges.CandidateRule, 0, len(payloads))
	for _, payload := range payloads {
		rules = append(rules, judges.CandidateRule{
			ID:           payload.ID,
			Kind:         payload.Kind,
			TargetRuleID: payload.TargetRuleID,
			Title:        payload.Title,
			Body:         payload.ProposedBody,
		})
	}
	return rules
}

func validatePromptIdentifier(field, value string) error {
	if value == "" {
		return nil
	}
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			continue
		}
		return fmt.Errorf("invalid %s %q", field, value)
	}
	return nil
}

func verifyCandidateBodySHA(body []byte, want string) error {
	sum := sha256.Sum256(body)
	got := hex.EncodeToString(sum[:])
	if got != want {
		return fmt.Errorf("sha256 mismatch: got=%s want=%s", got, want)
	}
	return nil
}
