package io

import "strings"

// SanitizeForPromptEmbedding removes NUL bytes and normalizes line endings
// before external text is embedded into prompts.
func SanitizeForPromptEmbedding(s string) string {
	s = strings.ReplaceAll(s, "\x00", "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	// Break common prompt-control markers so embedded PR / issue / diff text
	// cannot accidentally terminate or reinterpret surrounding prompt blocks.
	s = strings.ReplaceAll(s, "```", "`\u200b``")
	for _, marker := range []string{"SYSTEM:", "ASSISTANT:", "USER:", "<system>", "</system>", "<assistant>", "</assistant>", "<user>", "</user>"} {
		if strings.Contains(marker, ":") {
			s = strings.ReplaceAll(s, marker, marker[:1]+"\u200b"+marker[1:])
			continue
		}
		s = strings.ReplaceAll(s, marker, "<\u200b"+marker[1:])
	}
	return s
}
