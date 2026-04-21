package step50_implement

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
)

var promptIdentifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// RulePayload is the prompt-ready candidate rule body loaded from the run's 40/
// sidecars.
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
		body, err := os.ReadFile(bodyPath)
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

func validatePromptIdentifier(field, value string) error {
	if !promptIdentifierPattern.MatchString(value) {
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
