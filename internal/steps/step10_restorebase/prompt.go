package step10restorebase

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const reconstructedPromptMaxBytes = 64 * 1024
const diffExcerptMaxBytes = 24 * 1024

type TaskPromptSource string

const (
	TaskPromptSourceAuto      TaskPromptSource = "auto"
	TaskPromptSourceIssue     TaskPromptSource = "issue"
	TaskPromptSourcePR        TaskPromptSource = "pr"
	TaskPromptSourceDiffSynth TaskPromptSource = "diff_synth"
)

type TaskBriefInput struct {
	PR           int
	Title        string
	Body         string
	Issues       []LinkedIssue
	ChangedFiles []string
	Diff         string
}

// ReconstructTaskPrompt assembles the PR title, body and linked issue bodies
// into the raw task prompt that will be persisted in task-package.json.
//
// The returned string is NOT sanitized: downstream prompt builders must call
// internal/io.SanitizeForPromptEmbedding before embedding into an LLM prompt
// (see io-contracts.md §5).
//
// Shape:
//
//	# PR #<num>: <title>
//
//	<body>
//
//	## Linked issues
//
//	### #<n>: <title>
//	<body>
//
// Sections with empty body / zero linked issues are omitted. The returned
// string always terminates with a single trailing newline.
func ReconstructTaskPrompt(pr int, title, body string, issues []LinkedIssue) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# PR #%d: %s\n", pr, title)

	if trimmed := strings.TrimRight(body, "\n"); trimmed != "" {
		b.WriteString("\n")
		b.WriteString(trimmed)
		b.WriteString("\n")
	}

	if len(issues) > 0 {
		b.WriteString("\n## Linked issues\n")
		for _, issue := range issues {
			fmt.Fprintf(&b, "\n### #%d: %s\n", issue.Number, issue.Title)
			if trimmed := strings.TrimRight(issue.Body, "\n"); trimmed != "" {
				b.WriteString(trimmed)
				b.WriteString("\n")
			}
		}
	}
	return ensureTrailingNewlineWithinLimit(truncateUTF8Bytes(b.String(), reconstructedPromptMaxBytes), reconstructedPromptMaxBytes)
}

func truncateUTF8Bytes(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	truncated := value[:maxBytes]
	for len(truncated) > 0 && !utf8.ValidString(truncated) {
		_, size := utf8.DecodeLastRuneInString(truncated)
		if size <= 0 || size > len(truncated) {
			break
		}
		truncated = truncated[:len(truncated)-size]
	}
	return truncated
}

func ensureTrailingNewlineWithinLimit(value string, maxBytes int) string {
	if strings.HasSuffix(value, "\n") {
		return value
	}
	if len(value) >= maxBytes && maxBytes > 0 {
		value = truncateUTF8Bytes(value, maxBytes-1)
	}
	return value + "\n"
}

func SynthesizeTaskBrief(source string, input TaskBriefInput) string {
	mode := normalizeTaskPromptSource(source)
	usableIssues := usableLinkedIssues(input.Issues)
	changedTests, changedNonTests := splitChangedFiles(input.ChangedFiles)
	goal := synthesizeGoal(mode, input.Title, input.Body, usableIssues, changedTests, changedNonTests)

	var b strings.Builder
	b.WriteString("# Task Brief\n\n")
	b.WriteString("## Goal\n")
	fmt.Fprintf(&b, "- %s\n", goal)

	b.WriteString("\n## Required behavior\n")
	b.WriteString("- Implement the intended repository change described in the source context below.\n")
	if len(changedTests) > 0 {
		b.WriteString("- Treat the changed tests as the strongest signal for required behavior.\n")
	}
	if len(changedNonTests) > 0 {
		b.WriteString("- Update the affected implementation files as needed to satisfy the task brief.\n")
	}
	if includeIssues(mode, usableIssues) {
		b.WriteString("- Preserve the user-visible behavior and constraints described by the linked issues.\n")
	}
	if includeDiffContext(mode, usableIssues) {
		b.WriteString("- Use the changed file list and diff excerpt as evidence of intended behavior, not as step-by-step instructions.\n")
	}

	b.WriteString("\n## Constraints\n")
	b.WriteString("- Treat the PR title as supporting context only, not as the authoritative task definition.\n")
	b.WriteString("- Do not copy the exact diff mechanically if another valid implementation satisfies the same behavior.\n")
	b.WriteString("- Avoid unrelated refactors unless they are required to satisfy the task brief.\n")

	b.WriteString("\n## Acceptance\n")
	if len(changedTests) > 0 {
		b.WriteString("- The changed tests and test expectations listed below should be satisfied.\n")
	} else {
		b.WriteString("- The resulting implementation should satisfy the behavior implied by the task brief and source context.\n")
	}
	b.WriteString("- The implementation should address the requested change without introducing unrelated behavioral drift.\n")

	b.WriteString("\n## Non-goals\n")
	b.WriteString("- Do not broaden scope beyond the affected behavior suggested by the PR body, tests, and diff.\n")
	b.WriteString("- Do not treat the provided diff as the only acceptable solution shape.\n")

	b.WriteString("\n## Source Context\n")
	if includeIssues(mode, usableIssues) {
		b.WriteString("\n### Linked Issues\n")
		for _, issue := range usableIssues {
			fmt.Fprintf(&b, "- #%d: %s\n", issue.Number, issue.Title)
			if body := strings.TrimSpace(issue.Body); body != "" {
				fmt.Fprintf(&b, "  %s\n", strings.ReplaceAll(body, "\n", " "))
			}
		}
	}
	if mode == TaskPromptSourceDiffSynth {
		b.WriteString("\n### Weak Supporting PR Context\n")
		if title := strings.TrimSpace(input.Title); title != "" {
			fmt.Fprintf(&b, "- title: %s\n", title)
		}
		if trimmed := strings.TrimSpace(input.Body); trimmed != "" {
			fmt.Fprintf(&b, "- body: %s\n", strings.ReplaceAll(strings.TrimRight(trimmed, "\n"), "\n", " "))
		}
	} else {
		b.WriteString("\n### PR Title\n")
		fmt.Fprintf(&b, "%s\n", strings.TrimSpace(input.Title))
		if trimmed := strings.TrimSpace(input.Body); trimmed != "" {
			b.WriteString("\n### PR Body\n")
			b.WriteString(strings.TrimRight(trimmed, "\n"))
			b.WriteString("\n")
		}
	}
	if includeDiffContext(mode, usableIssues) {
		if len(changedTests) > 0 {
			b.WriteString("\n### Changed Tests\n")
			for _, file := range changedTests {
				fmt.Fprintf(&b, "- %s\n", file)
			}
		}
		if len(changedNonTests) > 0 {
			b.WriteString("\n### Changed Files\n")
			for _, file := range changedNonTests {
				fmt.Fprintf(&b, "- %s\n", file)
			}
		}
		if trimmed := strings.TrimSpace(input.Diff); trimmed != "" {
			b.WriteString(renderDiffExcerpt(trimmed, b.Len()))
		}
	}

	return ensureTrailingNewlineWithinLimit(truncateUTF8Bytes(b.String(), reconstructedPromptMaxBytes), reconstructedPromptMaxBytes)
}

func normalizeTaskPromptSource(source string) TaskPromptSource {
	switch TaskPromptSource(strings.TrimSpace(source)) {
	case TaskPromptSourceIssue, TaskPromptSourcePR, TaskPromptSourceDiffSynth:
		return TaskPromptSource(strings.TrimSpace(source))
	default:
		return TaskPromptSourceAuto
	}
}

func includeIssues(source TaskPromptSource, issues []LinkedIssue) bool {
	switch source {
	case TaskPromptSourceIssue:
		return len(issues) > 0
	case TaskPromptSourceAuto:
		return len(issues) > 0
	default:
		return false
	}
}

func includeDiffContext(source TaskPromptSource, issues []LinkedIssue) bool {
	switch source {
	case TaskPromptSourceDiffSynth:
		return true
	case TaskPromptSourceAuto:
		return true
	default:
		return false
	}
}

func synthesizeGoal(source TaskPromptSource, title, body string, issues []LinkedIssue, changedTests, changedNonTests []string) string {
	if source == TaskPromptSourceDiffSynth {
		return synthesizeDiffGoal(changedTests, changedNonTests)
	}
	if includeIssues(source, issues) && len(issues) > 0 {
		for _, issue := range issues {
			if goal := firstMeaningfulLine(issue.Body); goal != "" {
				return goal
			}
			if trimmed := strings.TrimSpace(issue.Title); trimmed != "" {
				return trimmed
			}
		}
	}
	return synthesizePRGoal(title, body)
}

func synthesizeDiffGoal(changedTests, changedNonTests []string) string {
	switch {
	case len(changedTests) > 0:
		return "Recreate the intended behavior covered by the changed tests and merged diff evidence."
	case len(changedNonTests) > 0:
		return "Recreate the intended behavior shown by the changed files and merged diff evidence."
	default:
		return "Infer and implement the intended behavior from the merged diff evidence."
	}
}

func synthesizePRGoal(title, body string) string {
	lines := strings.Split(body, "\n")
	for _, line := range lines {
		if goal := firstMeaningfulLine(line); goal != "" {
			return goal
		}
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return "Implement the requested repository change."
	}
	return title
}

func firstMeaningfulLine(value string) string {
	line := strings.TrimSpace(value)
	if line == "" {
		return ""
	}
	if strings.HasPrefix(line, "#") {
		return ""
	}
	return line
}

func usableLinkedIssues(issues []LinkedIssue) []LinkedIssue {
	filtered := make([]LinkedIssue, 0, len(issues))
	for _, issue := range issues {
		if strings.HasPrefix(strings.TrimSpace(issue.Body), "[issue #") && strings.HasSuffix(strings.TrimSpace(issue.Body), "fetch failed]") {
			continue
		}
		filtered = append(filtered, issue)
	}
	return filtered
}

func splitChangedFiles(files []string) (tests []string, nonTests []string) {
	for _, file := range files {
		file = strings.TrimSpace(file)
		if file == "" {
			continue
		}
		if looksLikeTestFile(file) {
			tests = append(tests, file)
			continue
		}
		nonTests = append(nonTests, file)
	}
	return tests, nonTests
}

func looksLikeTestFile(path string) bool {
	lower := strings.ToLower(path)
	switch {
	case strings.HasPrefix(lower, "test/"),
		strings.HasPrefix(lower, "tests/"),
		strings.HasPrefix(lower, "spec/"),
		strings.HasPrefix(lower, "specs/"),
		strings.Contains(lower, "/test/"),
		strings.Contains(lower, "/tests/"),
		strings.Contains(lower, "__tests__/"),
		strings.Contains(lower, "/spec/"),
		strings.Contains(lower, "/specs/"),
		strings.HasSuffix(lower, "_test.go"),
		strings.HasSuffix(lower, "_test.py"),
		strings.HasSuffix(lower, "_test.rb"),
		strings.HasSuffix(lower, "_spec.py"),
		strings.HasSuffix(lower, "_spec.rb"),
		strings.HasSuffix(lower, ".test.py"),
		strings.HasSuffix(lower, ".spec.py"),
		strings.HasSuffix(lower, ".test.rb"),
		strings.HasSuffix(lower, ".spec.rb"),
		strings.HasSuffix(lower, ".test.ts"),
		strings.HasSuffix(lower, ".test.tsx"),
		strings.HasSuffix(lower, ".test.js"),
		strings.HasSuffix(lower, ".test.jsx"),
		strings.HasSuffix(lower, ".spec.ts"),
		strings.HasSuffix(lower, ".spec.tsx"),
		strings.HasSuffix(lower, ".spec.js"),
		strings.HasSuffix(lower, ".spec.jsx"):
		return true
	default:
		return false
	}
}

func renderDiffExcerpt(diff string, currentLen int) string {
	const wrapperOverhead = len("\n### Diff Excerpt\n```diff\n\n```\n")
	remaining := reconstructedPromptMaxBytes - currentLen - wrapperOverhead
	if remaining <= 0 {
		return ""
	}
	if remaining > diffExcerptMaxBytes {
		remaining = diffExcerptMaxBytes
	}
	body := strings.TrimRight(truncateUTF8Bytes(diff, remaining), "\n")
	if body == "" {
		return ""
	}
	return "\n### Diff Excerpt\n```diff\n" + body + "\n```\n"
}
