package step10restorebase

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"
)

const reconstructedPromptMaxBytes = 64 * 1024
const diffExcerptMaxBytes = 24 * 1024

type TaskPromptSource string

const (
	TaskPromptSourceAuto  TaskPromptSource = "auto"
	TaskPromptSourceIssue TaskPromptSource = "issue"
)

type TaskBriefInput struct {
	PR           int
	Title        string
	Body         string
	Issues       []LinkedIssue
	ChangedFiles []string
	Diff         string
}

func GenerateTaskPrompt(ctx context.Context, source string, input TaskBriefInput, generator TaskBriefGenerator) (string, error) {
	mode := normalizeTaskPromptSource(source)
	usableIssues := usableLinkedIssues(input.Issues)
	if mode == TaskPromptSourceIssue && len(usableIssues) > 0 {
		return issueTaskBrief(usableIssues), nil
	}
	if generator != nil {
		task, err := generator.GenerateTaskBrief(ctx, input)
		if err != nil {
			if ctx.Err() != nil {
				return "", fmt.Errorf("step10: generate task brief: %w", err)
			}
			return SynthesizeTaskBrief(string(TaskPromptSourceAuto), input), nil
		}
		if strings.TrimSpace(task) != "" {
			return ensureTrailingNewlineWithinLimit(truncateUTF8Bytes(strings.TrimSpace(task), reconstructedPromptMaxBytes), reconstructedPromptMaxBytes), nil
		}
	}
	return SynthesizeTaskBrief(string(TaskPromptSourceAuto), input), nil
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
	if mode == TaskPromptSourceIssue && len(usableIssues) > 0 {
		return issueTaskBrief(usableIssues)
	}
	changedTests, changedNonTests := splitChangedFiles(input.ChangedFiles)
	goal := synthesizeGoal(input.Title, input.Body, usableIssues, changedTests, changedNonTests)

	var b strings.Builder
	b.WriteString("# Task\n\n")
	fmt.Fprintf(&b, "%s\n", goal)

	b.WriteString("\n## Background\n")
	switch {
	case len(usableIssues) > 0:
		b.WriteString("Use the linked issue as the main task context, and supplement only the missing parts inferred from the PR evidence.\n")
	case strings.TrimSpace(input.Body) != "":
		b.WriteString("Use the PR body as the main task context, and supplement missing details inferred from the PR evidence.\n")
	default:
		b.WriteString("The original issue text is unavailable, so infer the task from the merged PR evidence.\n")
	}

	b.WriteString("\n## Task Content\n")
	if len(changedTests) > 0 {
		b.WriteString("- Implement the behavior expected by the changed tests.\n")
	}
	if len(changedNonTests) > 0 {
		b.WriteString("- Update the affected application code needed for that behavior.\n")
	}
	if len(changedTests) == 0 && len(changedNonTests) == 0 {
		b.WriteString("- Implement the requested repository change while preserving unrelated behavior.\n")
	}
	b.WriteString("- Avoid unrelated refactors or behavior changes.\n")

	b.WriteString("\n## Source Context\n")
	if len(usableIssues) > 0 {
		b.WriteString("\n### Linked Issues\n")
		for _, issue := range usableIssues {
			fmt.Fprintf(&b, "- #%d: %s\n", issue.Number, issue.Title)
			if body := strings.TrimSpace(issue.Body); body != "" {
				fmt.Fprintf(&b, "  %s\n", strings.ReplaceAll(body, "\n", " "))
			}
		}
	}
	b.WriteString("\n### PR Context\n")
	if title := strings.TrimSpace(input.Title); title != "" {
		fmt.Fprintf(&b, "- title: %s\n", title)
	}
	if trimmed := strings.TrimSpace(input.Body); trimmed != "" {
		fmt.Fprintf(&b, "- body: %s\n", strings.ReplaceAll(strings.TrimRight(trimmed, "\n"), "\n", " "))
	}
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

	return ensureTrailingNewlineWithinLimit(truncateUTF8Bytes(b.String(), reconstructedPromptMaxBytes), reconstructedPromptMaxBytes)
}

func issueTaskBrief(issues []LinkedIssue) string {
	var b strings.Builder
	if len(issues) == 1 {
		issue := issues[0]
		fmt.Fprintf(&b, "# Issue #%d: %s\n", issue.Number, strings.TrimSpace(issue.Title))
		if body := strings.TrimSpace(issue.Body); body != "" {
			b.WriteString("\n")
			b.WriteString(strings.TrimRight(body, "\n"))
			b.WriteString("\n")
		}
		return ensureTrailingNewlineWithinLimit(truncateUTF8Bytes(b.String(), reconstructedPromptMaxBytes), reconstructedPromptMaxBytes)
	}
	b.WriteString("# Linked Issues\n")
	for _, issue := range issues {
		fmt.Fprintf(&b, "\n## #%d: %s\n", issue.Number, strings.TrimSpace(issue.Title))
		if body := strings.TrimSpace(issue.Body); body != "" {
			b.WriteString(strings.TrimRight(body, "\n"))
			b.WriteString("\n")
		}
	}
	return ensureTrailingNewlineWithinLimit(truncateUTF8Bytes(b.String(), reconstructedPromptMaxBytes), reconstructedPromptMaxBytes)
}

func normalizeTaskPromptSource(source string) TaskPromptSource {
	switch TaskPromptSource(strings.TrimSpace(source)) {
	case TaskPromptSourceAuto, TaskPromptSourceIssue:
		return TaskPromptSource(strings.TrimSpace(source))
	default:
		return TaskPromptSourceAuto
	}
}

func includeDiffContext(source TaskPromptSource) bool {
	switch source {
	case TaskPromptSourceAuto:
		return true
	default:
		return false
	}
}

func synthesizeGoal(title, body string, issues []LinkedIssue, changedTests, changedNonTests []string) string {
	if len(issues) > 0 {
		for _, issue := range issues {
			if goal := firstMeaningfulBodyLine(issue.Body); goal != "" {
				return goal
			}
			if trimmed := strings.TrimSpace(issue.Title); trimmed != "" {
				return trimmed
			}
		}
	}
	if goal := firstMeaningfulBodyLine(body); goal != "" {
		return goal
	}
	if title = strings.TrimSpace(title); title != "" {
		return title
	}
	if len(changedTests) > 0 || len(changedNonTests) > 0 {
		return "Reconstruct the original task from the merged PR evidence."
	}
	return "Implement the requested repository change."
}

func firstMeaningfulBodyLine(body string) string {
	for _, line := range strings.Split(body, "\n") {
		if goal := firstMeaningfulLine(line); goal != "" {
			return goal
		}
	}
	return ""
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
