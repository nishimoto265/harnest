package io

import "strings"

// SanitizeForPromptEmbedding removes NUL bytes and normalizes line endings
// before external text is embedded into prompts.
func SanitizeForPromptEmbedding(s string) string {
	s = strings.ReplaceAll(s, "\x00", "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}
