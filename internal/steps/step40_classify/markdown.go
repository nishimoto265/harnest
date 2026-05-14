package step40_classify

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/nishimoto265/harnest/internal/contracts"
	"github.com/nishimoto265/harnest/internal/lessons"
)

func candidateBodyMarkdown(candidate contracts.Candidate) string {
	return candidateBodyMarkdownWithEvidence(candidate, candidateEvidence{})
}

func candidateBodyMarkdownWithEvidence(candidate contracts.Candidate, evidence candidateEvidence) string {
	sourceID := sourceIDFromCandidate(candidate)
	lessonID := lessonIDFromSource(sourceID)
	metadata := lessonMetadataForEvidence(sourceID, evidence)
	checklistItem := checklistItemForEvidence(sourceID, evidence)
	return experimentLessonBodyMarkdown(candidate, lessonID, metadata, checklistItem, evidence)
}

func experimentLessonBodyMarkdown(candidate contracts.Candidate, lessonID string, metadata lessons.Metadata, checklistItem string, evidence candidateEvidence) string {
	sourceID := sourceIDFromCandidate(candidate)
	var body strings.Builder
	fmt.Fprintf(&body, "---\nstatus: %s\nseverity: %s\nconfidence: %s\ncategory: %s\n---\n\n", metadata.Status, metadata.Severity, metadata.Confidence, metadata.Category)
	fmt.Fprintf(&body, "# %s\n\n", lessonID)
	fmt.Fprintf(&body, "- candidate_id: %s\n- source_id: %s\n- classification: %s\n", candidate.CandidateID, sourceID, candidate.Kind)
	if candidate.TargetRuleID != "" {
		fmt.Fprintf(&body, "- target_rule_id: %s\n", candidate.TargetRuleID)
	}
	fmt.Fprintf(&body, "\n## Checklist Item\n\n%s\n\n", checklistItem)
	fmt.Fprintf(&body, "## Problem\n\n%s\n\n", candidate.Problem)
	fmt.Fprintf(&body, "## Evidence\n\n")
	if len(evidence.Compliance) > 0 || len(evidence.Scores) > 0 || len(evidence.Issues) > 0 {
		for _, line := range evidence.Compliance {
			fmt.Fprintf(&body, "- compliance: %s\n", line)
		}
		for _, line := range evidence.Scores {
			fmt.Fprintf(&body, "- scoring: %s\n", line)
		}
		for _, line := range evidence.Issues {
			fmt.Fprintf(&body, "- issue: %s\n", line)
		}
	} else {
		body.WriteString("- no concrete evidence captured\n")
	}
	fmt.Fprintf(&body, "\n## Guidance\n\n")
	for _, line := range guidanceLines(sourceID, evidence) {
		fmt.Fprintf(&body, "- %s\n", line)
	}
	fmt.Fprintf(&body, "\n## Exceptions\n\nIf the task explicitly requires behavior that conflicts with this lesson, document the exception and rationale.\n")
	fmt.Fprintf(&body, "\n## Merge Notes\n\nBefore adding another lesson, compare existing lessons by source, checklist item, problem, guidance, and evidence. Prefer updating an existing lesson when it covers the same failure mode.\n")
	return body.String()
}

func sourceIDFromCandidate(candidate contracts.Candidate) string {
	if candidate.TargetRuleID != "" {
		return candidate.TargetRuleID
	}
	if sourceID := strings.TrimPrefix(candidate.Title, "Experiment lesson for "); sourceID != candidate.Title {
		return sourceID
	}
	if sourceID := strings.TrimPrefix(candidate.Title, "Rule candidate for "); sourceID != candidate.Title {
		return sourceID
	}
	if base := strings.TrimSuffix(filepath.Base(candidate.ProposedBodyPath), filepath.Ext(candidate.ProposedBodyPath)); base != "." && base != "" {
		return base
	}
	return candidate.CandidateID
}

func lessonIDFromSource(sourceID string) string {
	sourceID = strings.ToLower(strings.TrimSpace(sourceID))
	var out strings.Builder
	lastDash := false
	for _, r := range sourceID {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && out.Len() > 0 {
				out.WriteByte('-')
				lastDash = true
			}
		}
	}
	id := strings.Trim(out.String(), "-")
	if id == "" {
		id = "lesson"
	}
	if len(id) > 72 {
		sum := sha256.Sum256([]byte(sourceID))
		prefix := strings.Trim(id[:56], "-")
		id = fmt.Sprintf("%s-%s", prefix, hex.EncodeToString(sum[:])[:12])
	}
	if err := lessons.ValidateID(id); err != nil {
		sum := sha256.Sum256([]byte(sourceID))
		id = "lesson-" + hex.EncodeToString(sum[:])[:12]
	}
	return id
}

func experimentLessonPath(lessonID string) string {
	return filepath.Join(experimentLessonsDirPath, lessonID+".md")
}

func lessonMetadataForEvidence(sourceID string, evidence candidateEvidence) lessons.Metadata {
	return lessons.Metadata{
		Status:     lessons.StatusActive,
		Severity:   lessonSeverityForEvidence(sourceID, evidence),
		Confidence: lessons.ConfidenceMedium,
		Category:   lessonCategoryForSource(sourceID),
	}
}

func lessonSeverityForEvidence(sourceID string, evidence candidateEvidence) lessons.Severity {
	if len(evidence.Compliance) > 0 {
		return lessons.SeverityHigh
	}
	if len(evidence.Issues) > 0 {
		return lessons.SeverityMedium
	}
	for _, line := range evidence.Scores {
		switch {
		case strings.Contains(line, "/"+string(contracts.DimensionCorrectness)+" "),
			strings.Contains(line, "/"+string(contracts.DimensionFidelity)+" "),
			strings.Contains(line, "/"+string(contracts.DimensionDiscipline)+" "):
			return lessons.SeverityHigh
		case strings.Contains(line, "/"+string(contracts.DimensionMaintainability)+" "):
			return lessons.SeverityMedium
		}
	}
	return lessons.SeverityLow
}

func lessonCategoryForSource(sourceID string) string {
	sourceID = strings.TrimSpace(sourceID)
	if strings.HasPrefix(sourceID, "issue-") {
		return "judge-issue"
	}
	if strings.HasPrefix(sourceID, "score-") {
		return "score-feedback"
	}
	return "compliance"
}

func checklistItemForEvidence(sourceID string, evidence candidateEvidence) string {
	if strings.TrimSpace(evidence.Checklist) != "" {
		return shortenChecklistItem(evidence.Checklist)
	}
	if len(evidence.Compliance) > 0 {
		return shortenChecklistItem(fmt.Sprintf("Satisfy %s based on pass1 evidence: %s", sourceID, stripEvidencePrefix(evidence.Compliance[0])))
	}
	if len(evidence.Scores) > 0 {
		return shortenChecklistItem(fmt.Sprintf("Avoid repeating this pass1 issue: %s", stripEvidencePrefix(evidence.Scores[0])))
	}
	if len(evidence.Issues) > 0 {
		return shortenChecklistItem(fmt.Sprintf("Avoid repeating this pass1 issue: %s", stripEvidencePrefix(evidence.Issues[0])))
	}
	return shortenChecklistItem(fmt.Sprintf("Avoid repeating the pass1 finding for %s.", sourceID))
}

func stripEvidencePrefix(value string) string {
	if idx := strings.Index(value, ": "); idx >= 0 && idx+2 < len(value) {
		return value[idx+2:]
	}
	return value
}

func shortenChecklistItem(value string) string {
	value = normalizeEvidenceText(value)
	const maxLen = 180
	if len(value) <= maxLen {
		return value
	}
	value = strings.TrimSpace(value[:maxLen-3])
	return strings.TrimRight(value, " .,;:") + "..."
}
