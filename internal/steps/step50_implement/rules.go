package step50_implement

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
)

// RulePayload is one candidate rule body loaded for the pass2 prompt.
type RulePayload struct {
	ID   string
	Text string
}

// LoadCandidateRulePayloads resolves pass2 candidate rule IDs from
// `<run>/40/candidates.json`. Missing candidates.json is treated as no rules.
// For each referenced rule ID, `<runs_base>/rules/<rule_id>.md` is loaded when
// present; missing sidecars produce an empty Text.
func LoadCandidateRulePayloads(runCtx internalio.RunContext) ([]RulePayload, error) {
	candidatesPath, err := runCtx.ResolveRunRelative(filepath.Join("40", "candidates.json"))
	if err != nil {
		return nil, fmt.Errorf("resolve candidates path: %w", err)
	}

	candidates, err := internalio.ReadJSON[contracts.Candidates](candidatesPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []RulePayload{}, nil
		}
		return nil, fmt.Errorf("read candidates: %w", err)
	}

	ruleIDs := make([]string, 0, len(candidates.Candidates))
	seen := make(map[string]struct{}, len(candidates.Candidates))
	for _, candidate := range candidates.Candidates {
		if candidate.TargetRuleID == "" {
			continue
		}
		if _, ok := seen[candidate.TargetRuleID]; ok {
			continue
		}
		seen[candidate.TargetRuleID] = struct{}{}
		ruleIDs = append(ruleIDs, candidate.TargetRuleID)
	}

	payloads := make([]RulePayload, 0, len(ruleIDs))
	for _, ruleID := range ruleIDs {
		sidecarPath, err := ruleSidecarPath(runCtx.RunsBase, ruleID)
		if err != nil {
			return nil, err
		}

		text := ""
		data, err := os.ReadFile(sidecarPath)
		if err != nil {
			if !os.IsNotExist(err) {
				return nil, fmt.Errorf("read rule sidecar %q: %w", ruleID, err)
			}
		} else {
			text = string(data)
		}

		payloads = append(payloads, RulePayload{ID: ruleID, Text: text})
	}

	return payloads, nil
}

func ruleSidecarPath(runsBase, ruleID string) (string, error) {
	switch {
	case strings.TrimSpace(ruleID) == "":
		return "", fmt.Errorf("rule sidecar path: empty rule id")
	case ruleID == "." || ruleID == "..":
		return "", fmt.Errorf("rule sidecar path: invalid rule id %q", ruleID)
	case strings.ContainsRune(ruleID, filepath.Separator):
		return "", fmt.Errorf("rule sidecar path: rule id must not contain path separators: %q", ruleID)
	}

	path := filepath.Join(runsBase, "rules", ruleID+".md")
	if err := contracts.EnsureCleanAbsolutePath(path); err != nil {
		return "", fmt.Errorf("rule sidecar path %q: %w", ruleID, err)
	}
	return path, nil
}
