package step40_classify

import (
	"fmt"
	"strings"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/lessons"
)

type candidateEvidence struct {
	Compliance []string
	Scores     []string
	Issues     []string
	Guidance   []string
	Checklist  string
}

type issueEvidence struct {
	Severity      lessons.Severity
	Category      string
	ChecklistItem string
	Issues        []string
	Guidance      []string
}

func collectViolations(entries []contracts.ComplianceEntry) map[string]int {
	violations := make(map[string]int)
	for _, entry := range entries {
		if !isViolationVerdict(entry.Verdict) {
			continue
		}
		violations[entry.RuleID]++
	}
	return violations
}

func isViolationVerdict(verdict contracts.ComplianceVerdict) bool {
	switch verdict {
	case contracts.ComplianceVerdictViolated, contracts.ComplianceVerdictInvalidException, contracts.ComplianceVerdictMissed:
		return true
	default:
		return false
	}
}

func collectViolatingAgents(entries []contracts.ComplianceEntry) map[contracts.AgentID]struct{} {
	agents := make(map[contracts.AgentID]struct{})
	for _, entry := range entries {
		if !isViolationVerdict(entry.Verdict) {
			continue
		}
		agents[entry.Agent] = struct{}{}
	}
	return agents
}

func candidateRationale(ruleID string, evidence candidateEvidence) string {
	parts := make([]string, 0, 2)
	if len(evidence.Compliance) > 0 {
		parts = append(parts, fmt.Sprintf("%d compliance violation rationale(s)", len(evidence.Compliance)))
	}
	if len(evidence.Scores) > 0 {
		parts = append(parts, fmt.Sprintf("%d score reason(s)", len(evidence.Scores)))
	}
	if len(evidence.Issues) > 0 {
		parts = append(parts, fmt.Sprintf("%d explicit issue finding(s)", len(evidence.Issues)))
	}
	if len(parts) == 0 {
		return ""
	}
	return fmt.Sprintf("Derived from %s for %s.", strings.Join(parts, " and "), ruleID)
}

func guidanceLines(ruleID string, evidence candidateEvidence) []string {
	lines := make([]string, 0, len(evidence.Compliance)+len(evidence.Scores))
	for _, line := range evidence.Guidance {
		lines = append(lines, fmt.Sprintf("Apply this proposed lesson: %s", line))
	}
	for _, line := range evidence.Compliance {
		lines = append(lines, fmt.Sprintf("When handling %s, prevent the behavior implied by this violation evidence: %s", ruleID, line))
	}
	for _, line := range evidence.Scores {
		lines = append(lines, fmt.Sprintf("Address this pass1 scoring concern while implementing the task: %s", line))
	}
	for _, line := range evidence.Issues {
		lines = append(lines, fmt.Sprintf("Address this explicit pass1 issue while implementing the task: %s", line))
	}
	if len(lines) == 0 {
		lines = append(lines, fmt.Sprintf("Use this lesson as the concrete failure mode to avoid for %s.", ruleID))
	}
	return lines
}
