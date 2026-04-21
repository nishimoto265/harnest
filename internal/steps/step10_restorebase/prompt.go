package step10restorebase

import (
	"fmt"
	"strings"
)

const reconstructedPromptMaxBytes = 64 * 1024

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
	for len(truncated) > 0 && (truncated[len(truncated)-1]&0xC0) == 0x80 {
		truncated = truncated[:len(truncated)-1]
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
