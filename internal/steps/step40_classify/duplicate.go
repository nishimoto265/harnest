package step40_classify

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/registryview"
)

func activeRulesFromRegistry(entries []contracts.RuleRegistryEntry) (map[string]bool, error) {
	states, err := registryview.Build(entries)
	if err != nil {
		return nil, err
	}
	active := make(map[string]bool, len(states))
	for ruleID, state := range registryview.Active(states) {
		active[ruleID] = state.Exists
	}
	return active, nil
}

func activeRuleBodiesFromRegistry(entries []contracts.RuleRegistryEntry, registryBase string, activeRules map[string]bool) (map[string]string, error) {
	states, err := registryview.Build(entries)
	if err != nil {
		return nil, err
	}
	bodies := make(map[string]string, len(activeRules))
	for ruleID := range activeRules {
		state, ok := states[ruleID]
		if !ok {
			continue
		}
		body, err := os.ReadFile(filepath.Join(registryBase, state.RulePath))
		if err != nil {
			return nil, fmt.Errorf("step40_classify: read rule sidecar rule_id=%s: %w", ruleID, err)
		}
		if got := sha256Hex(body); got != state.Sha256 {
			return nil, fmt.Errorf("step40_classify: rule sidecar sha mismatch: rule_id=%s got=%s want=%s", ruleID, got, state.Sha256)
		}
		bodies[ruleID] = string(body)
	}
	return bodies, nil
}

func bestDuplicateMatch(candidateBody string, activeRuleBodies map[string]string) (string, float64) {
	bestRuleID := ""
	bestScore := 0.0
	normalizedCandidate := normalizeRuleContent(candidateBody)
	if strings.TrimSpace(normalizedCandidate) == "" {
		return "", 0
	}
	ruleIDs := make([]string, 0, len(activeRuleBodies))
	for ruleID := range activeRuleBodies {
		ruleIDs = append(ruleIDs, ruleID)
	}
	sort.Strings(ruleIDs)
	for _, ruleID := range ruleIDs {
		body := activeRuleBodies[ruleID]
		normalizedBody := normalizeRuleContent(body)
		if strings.TrimSpace(normalizedBody) == "" {
			continue
		}
		score := tokenSetSimilarity(normalizedCandidate, normalizedBody)
		if score > bestScore || (score == bestScore && score > 0 && (bestRuleID == "" || ruleID < bestRuleID)) {
			bestRuleID = ruleID
			bestScore = score
		}
	}
	return bestRuleID, bestScore
}

func tokenSetSimilarity(left, right string) float64 {
	leftSet := normalizedTokenSet(left)
	rightSet := normalizedTokenSet(right)
	if len(leftSet) == 0 && len(rightSet) == 0 {
		return 1
	}
	intersection := 0
	union := make(map[string]struct{}, len(leftSet)+len(rightSet))
	for token := range leftSet {
		union[token] = struct{}{}
	}
	for token := range rightSet {
		if _, ok := leftSet[token]; ok {
			intersection++
		}
		union[token] = struct{}{}
	}
	return float64(intersection) / float64(len(union))
}

func normalizeRuleContent(value string) string {
	lines := strings.Split(value, "\n")
	normalized := make([]string, 0, len(lines))
	section := ""
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			continue
		case strings.HasPrefix(trimmed, "- source_rule_id:"):
			continue
		case strings.HasPrefix(trimmed, "- classification:"):
			continue
		case trimmed == "## Problem":
			section = "problem"
			continue
		case trimmed == "## Rationale":
			section = "rationale"
			continue
		}
		if section == "problem" && strings.HasPrefix(trimmed, "Pass1 recorded ") && strings.Contains(trimmed, " violation(s) for rule ") {
			continue
		}
		if section == "rationale" && strings.HasPrefix(trimmed, "Phase 0 deterministic classify generated one candidate from compliance-A.jsonl for ") {
			continue
		}
		normalized = append(normalized, trimmed)
	}
	return strings.Join(normalized, "\n")
}

func normalizedTokenSet(value string) map[string]struct{} {
	value = strings.ToLower(value)
	value = strings.ReplaceAll(value, "\n", " ")
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	})
	set := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		if field == "" {
			continue
		}
		set[field] = struct{}{}
	}
	return set
}
