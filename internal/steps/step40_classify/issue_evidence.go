package step40_classify

import (
	"fmt"
	"sort"
	"strings"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/lessons"
)

func collectIssueEvidence(entries []contracts.IssueEntry) map[string]issueEvidence {
	collected := make(map[string]issueEvidence)
	seen := make(map[string]struct{})
	for _, entry := range entries {
		sourceID := issueSourceID(entry)
		line := fmt.Sprintf("%s/%s/%s: %s", entry.Agent, entry.JudgeRole, entry.Severity, normalizeEvidenceText(entry.Evidence))
		seenKey := sourceID + "\x00" + line
		if _, exists := seen[seenKey]; exists {
			continue
		}
		seen[seenKey] = struct{}{}

		evidence := collected[sourceID]
		if evidence.Category == "" {
			evidence.Category = normalizeIssueCategory(entry.Category)
		}
		if evidence.Severity == "" || severityRankForIssue(entry.Severity) < severityRankForLesson(evidence.Severity) {
			evidence.Severity = issueSeverityToLesson(entry.Severity)
		}
		if evidence.ChecklistItem == "" {
			evidence.ChecklistItem = entry.ChecklistItem
		}
		evidence.Issues = append(evidence.Issues, line)
		if strings.TrimSpace(entry.ProposedLesson) != "" {
			evidence.Guidance = append(evidence.Guidance, normalizeEvidenceText(entry.ProposedLesson))
		}
		collected[sourceID] = evidence
	}
	for sourceID, evidence := range collected {
		collected[sourceID] = finalizeIssueEvidence(evidence)
	}
	return collected
}

func issueSourceID(entry contracts.IssueEntry) string {
	return "issue-" + lessonIDFromSource(entry.Category+"-"+entry.Title)
}

func finalizeIssueEvidence(evidence issueEvidence) issueEvidence {
	sort.Strings(evidence.Issues)
	evidence.Guidance = uniqueSortedStrings(evidence.Guidance)
	if len(evidence.Issues) > 5 {
		evidence.Issues = evidence.Issues[:5]
	}
	if len(evidence.Guidance) > 5 {
		evidence.Guidance = evidence.Guidance[:5]
	}
	if evidence.Severity == "" {
		evidence.Severity = lessons.SeverityMedium
	}
	if evidence.Category == "" {
		evidence.Category = "judge-issue"
	}
	return evidence
}

func normalizeIssueCategory(value string) string {
	value = normalizeEvidenceText(value)
	if value == "" {
		return "judge-issue"
	}
	return value
}

func issueSeverityToLesson(severity contracts.IssueSeverity) lessons.Severity {
	switch severity {
	case contracts.IssueSeverityCritical:
		return lessons.SeverityCritical
	case contracts.IssueSeverityHigh:
		return lessons.SeverityHigh
	case contracts.IssueSeverityMedium:
		return lessons.SeverityMedium
	case contracts.IssueSeverityLow:
		return lessons.SeverityLow
	default:
		return lessons.SeverityMedium
	}
}

func severityRankForIssue(severity contracts.IssueSeverity) int {
	return severityRankForLesson(issueSeverityToLesson(severity))
}

func severityRankForLesson(severity lessons.Severity) int {
	switch severity {
	case lessons.SeverityCritical:
		return 0
	case lessons.SeverityHigh:
		return 1
	case lessons.SeverityMedium:
		return 2
	case lessons.SeverityLow:
		return 3
	default:
		return 4
	}
}

func uniqueSortedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
