package step40_classify

import (
	"fmt"
	"sort"
	"strings"

	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
)

func collectCandidateEvidence(runIO internalio.RunContext, ruleID string, compliance []contracts.ComplianceEntry, scores []contracts.ScoreEntry) (candidateEvidence, error) {
	evidence := candidateEvidence{
		Compliance: make([]string, 0, 3),
		Scores:     make([]string, 0, 2),
	}
	seenCompliance := map[string]struct{}{}
	matchingAgents := make(map[contracts.AgentID]struct{})
	for _, entry := range compliance {
		if entry.RuleID != ruleID || !isViolationVerdict(entry.Verdict) {
			continue
		}
		matchingAgents[entry.Agent] = struct{}{}
	}
	for _, entry := range compliance {
		if entry.RuleID != ruleID || !isViolationVerdict(entry.Verdict) {
			continue
		}
		text, ok, err := substantiveEvidenceText(runIO, entry.Rationale, entry.RationaleOverflowRef)
		if err != nil {
			return candidateEvidence{}, err
		}
		if !ok {
			continue
		}
		if _, exists := seenCompliance[text]; exists {
			continue
		}
		seenCompliance[text] = struct{}{}
		evidence.Compliance = append(evidence.Compliance, text)
		if len(evidence.Compliance) == 3 {
			break
		}
	}
	scoreEvidence, err := collectScoreEvidence(runIO, scores, matchingAgents)
	if err != nil {
		return candidateEvidence{}, err
	}
	for _, line := range scoreEvidence {
		evidence.Scores = append(evidence.Scores, line)
		if len(evidence.Scores) == 2 {
			break
		}
	}
	return evidence, nil
}

func collectScoreEvidence(runIO internalio.RunContext, scores []contracts.ScoreEntry, matchingAgents map[contracts.AgentID]struct{}) ([]string, error) {
	lines := make([]string, 0, len(scores))
	seen := map[string]struct{}{}
	for _, score := range scores {
		if len(matchingAgents) > 0 {
			if _, ok := matchingAgents[score.Agent]; !ok {
				continue
			}
		}
		text, ok, err := substantiveScoreConcernText(runIO, score.Reasons, score.ReasonsOverflowRef)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		line := fmt.Sprintf("%s/%s: %s", score.Agent, score.Dimension, text)
		if _, exists := seen[line]; exists {
			continue
		}
		seen[line] = struct{}{}
		lines = append(lines, line)
	}
	sort.Strings(lines)
	return lines, nil
}

func collectScoreConcernEvidence(runIO internalio.RunContext, scores []contracts.ScoreEntry, ignoredAgents map[contracts.AgentID]struct{}) (map[string]candidateEvidence, error) {
	concerns := make(map[string]candidateEvidence)
	seen := map[string]struct{}{}
	for _, score := range scores {
		if _, ignored := ignoredAgents[score.Agent]; ignored {
			continue
		}
		text, ok, err := substantiveScoreConcernText(runIO, score.Reasons, score.ReasonsOverflowRef)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		for _, concern := range scoreConcernSentences(text) {
			if isNonActionableScoreConcern(concern) {
				continue
			}
			ruleID := scoreConcernRuleID(concern)
			line := fmt.Sprintf("%s/%s score %d: %s", score.Agent, score.Dimension, score.Score, concern)
			seenKey := ruleID + "\x00" + line
			if _, exists := seen[seenKey]; exists {
				continue
			}
			seen[seenKey] = struct{}{}
			evidence := concerns[ruleID]
			evidence.Scores = append(evidence.Scores, line)
			concerns[ruleID] = evidence
		}
	}
	for ruleID, evidence := range concerns {
		sort.Strings(evidence.Scores)
		if len(evidence.Scores) > 3 {
			evidence.Scores = evidence.Scores[:3]
		}
		concerns[ruleID] = evidence
	}
	return concerns, nil
}

func scoreConcernRuleID(concern string) string {
	if category := categorizedScoreConcernRuleID(concern); category != "" {
		return category
	}
	return "score-" + lessonIDFromSource(concern)
}

func categorizedScoreConcernRuleID(concern string) string {
	lower := strings.ToLower(normalizeEvidenceText(concern))
	switch {
	case strings.Contains(lower, "meta tag") && strings.Contains(lower, "client component"):
		return "score-client-component-meta-tags"
	case (strings.Contains(lower, "nearly identical") || strings.Contains(lower, "nearly-identical") || strings.Contains(lower, "duplicate")) && (strings.Contains(lower, "error.tsx") || strings.Contains(lower, "error handlers")):
		return "score-deduplicate-route-group-error-handlers"
	case strings.Contains(lower, "header/footer") || strings.Contains(lower, "inlinedheader"):
		return "score-avoid-inline-layout-duplication"
	case strings.Contains(lower, "sentry setup") && strings.Contains(lower, "unified"):
		return "score-document-error-handler-strategy"
	case strings.Contains(lower, "three-group error handler strategy"):
		return "score-document-error-handler-strategy"
	case strings.Contains(lower, "component duplication across layouts"):
		return "score-document-error-handler-strategy"
	case strings.Contains(lower, "proxy") && (strings.Contains(lower, "not_found") || strings.Contains(lower, "404 rewrite")):
		return "score-extract-proxy-not-found-logic"
	case strings.Contains(lower, "design system tokens"):
		return "score-use-design-system-tokens"
	case strings.Contains(lower, "cutleryicon"):
		return "score-verify-errorstate-icon-dependencies"
	default:
		return ""
	}
}
