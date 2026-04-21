package step10restorebase

import (
	"fmt"
	"strings"
)

// ReconstructTaskPrompt stores the raw PR/task context for downstream prompt
// embedding. Sanitization happens later in the pipeline.
func ReconstructTaskPrompt(title, body string, linkedIssues []LinkedIssue) string {
	blocks := []string{
		"# " + title,
	}
	if body != "" {
		blocks = append(blocks, body)
	}
	if len(linkedIssues) > 0 {
		var linked strings.Builder
		linked.WriteString("## Linked issues")
		for _, issue := range linkedIssues {
			linked.WriteString("\n\n")
			linked.WriteString(fmt.Sprintf("### #%d: %s", issue.Number, issue.Title))
			if issue.Body != "" {
				linked.WriteString("\n\n")
				linked.WriteString(issue.Body)
			}
		}
		blocks = append(blocks, linked.String())
	}
	return strings.Join(blocks, "\n\n") + "\n"
}
