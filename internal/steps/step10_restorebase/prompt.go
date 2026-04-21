package step10restorebase

import (
	"fmt"
	"strings"
)

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
	return b.String()
}
