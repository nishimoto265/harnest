package step50_implement

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
)

// maxCandidateBodyBytes caps individual candidate proposed-body reads at 8 MiB
// to prevent an attacker-controlled sidecar from exhausting memory.
const maxCandidateBodyBytes = 8 << 20

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
		body, err := internalio.ReadValidatedRegularFile(bodyPath, maxCandidateBodyBytes)
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
	switch {
	case value == "":
		return nil
	case value == "." || value == "..":
		return fmt.Errorf("invalid %s %q", field, value)
	case filepath.Clean(value) != value:
		return fmt.Errorf("invalid %s %q", field, value)
	case strings.Contains(value, "/"), strings.Contains(value, `\`):
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

func rulePayloadIDs(payloads []RulePayload) []string {
	if len(payloads) == 0 {
		return nil
	}
	ids := make([]string, 0, len(payloads))
	for _, payload := range payloads {
		ids = append(ids, payload.ID)
	}
	return ids
}
