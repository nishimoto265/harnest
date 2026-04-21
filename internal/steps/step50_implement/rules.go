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

var safeRuleIdentifierPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// RulePayload is the prompt-ready candidate rule proposal loaded from the run sidecar.
type RulePayload struct {
	ID           string
	Kind         contracts.CandidateKind
	TargetRuleID string
	Title        string
	ProposedBody string
}

// LoadRulePayloads reads the step40 candidate proposals referenced by candidates.json.
func LoadRulePayloads(runDir, candidatesPath string) ([]RulePayload, error) {
	if err := contracts.EnsureCleanAbsolutePath(runDir); err != nil {
		return nil, fmt.Errorf("invalid run dir: %w", err)
	}

	candidates, err := internalio.ReadJSON[contracts.Candidates](candidatesPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read candidates: %w", err)
	}

	payloads := make([]RulePayload, 0, len(candidates.Candidates))
	for _, candidate := range candidates.Candidates {
		if err := validateCandidateIdentifiers(candidate); err != nil {
			return nil, err
		}

		bodyPath, err := resolveCandidateBodyPath(runDir, candidate.ProposedBodyPath)
		if err != nil {
			return nil, fmt.Errorf("candidate %q: %w", candidate.CandidateID, err)
		}

		body, err := os.ReadFile(bodyPath)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, fmt.Errorf("candidate %q: proposed body missing: %s", candidate.CandidateID, candidate.ProposedBodyPath)
			}
			return nil, fmt.Errorf("candidate %q: read proposed body: %w", candidate.CandidateID, err)
		}

		if err := verifyCandidateBodyHash(candidate, body); err != nil {
			return nil, err
		}

		payloads = append(payloads, RulePayload{
			ID:           candidate.CandidateID,
			Kind:         candidate.Kind,
			TargetRuleID: candidate.TargetRuleID,
			Title:        candidate.Title,
			ProposedBody: string(body),
		})
	}
	return payloads, nil
}

func validateCandidateIdentifiers(candidate contracts.Candidate) error {
	if err := validateSafeRuleIdentifier("candidate_id", candidate.CandidateID, false); err != nil {
		return fmt.Errorf("candidate %q: %w", candidate.CandidateID, err)
	}
	if err := validateSafeRuleIdentifier("target_rule_id", candidate.TargetRuleID, candidate.TargetRuleID == ""); err != nil {
		return fmt.Errorf("candidate %q: %w", candidate.CandidateID, err)
	}
	return nil
}

func validateSafeRuleIdentifier(field, value string, allowEmpty bool) error {
	if value == "" {
		if allowEmpty {
			return nil
		}
		return fmt.Errorf("%s is required", field)
	}
	if !safeRuleIdentifierPattern.MatchString(value) {
		return fmt.Errorf("%s %q must match %s", field, value, safeRuleIdentifierPattern.String())
	}
	return nil
}

func resolveCandidateBodyPath(runDir, proposedBodyPath string) (string, error) {
	if err := contracts.EnsureCleanRelativePath(proposedBodyPath); err != nil {
		return "", fmt.Errorf("invalid proposed_body_path %q: %w", proposedBodyPath, err)
	}
	return filepath.Join(runDir, proposedBodyPath), nil
}

func verifyCandidateBodyHash(candidate contracts.Candidate, body []byte) error {
	sum := sha256.Sum256(body)
	got := hex.EncodeToString(sum[:])
	if got != candidate.ProposedBodySha256 {
		return fmt.Errorf("candidate %q: proposed body sha mismatch: got=%s want=%s", candidate.CandidateID, got, candidate.ProposedBodySha256)
	}
	return nil
}
