package step10restorebase

import (
	"fmt"
	"strings"
)

func ReconstructTaskPrompt(args ...any) string {
	var (
		pr           int
		hasPR        bool
		title        string
		body         string
		linkedIssues []LinkedIssue
	)

	switch len(args) {
	case 3:
		title = args[0].(string)
		body = args[1].(string)
		linkedIssues = args[2].([]LinkedIssue)
	case 4:
		pr = args[0].(int)
		hasPR = true
		title = args[1].(string)
		body = args[2].(string)
		linkedIssues = args[3].([]LinkedIssue)
	default:
		panic("step10: ReconstructTaskPrompt expects (title, body, linkedIssues) or (pr, title, body, linkedIssues)")
	}

	return reconstructTaskPrompt(hasPR, pr, title, body, linkedIssues)
}

func reconstructTaskPrompt(hasPR bool, pr int, title, body string, linkedIssues []LinkedIssue) string {
	var b strings.Builder
	if hasPR {
		fmt.Fprintf(&b, "# PR #%d: %s\n", pr, title)
	} else {
		fmt.Fprintf(&b, "# %s\n", title)
	}

	if body != "" {
		b.WriteString("\n")
		b.WriteString(body)
		b.WriteString("\n")
	}

	if len(linkedIssues) > 0 {
		b.WriteString("\n## Linked issues\n")
		for _, issue := range linkedIssues {
			fmt.Fprintf(&b, "\n### #%d: %s\n", issue.Number, issue.Title)
			if issue.Body != "" {
				b.WriteString(issue.Body)
				b.WriteString("\n")
			}
		}
	}

	if b.Len() == 0 || !strings.HasSuffix(b.String(), "\n") {
		b.WriteString("\n")
	}
	return b.String()
}
