package io

import (
	"regexp"
	"strings"
)

type SafeTextOptions struct {
	Label string
	Fence bool
}

var (
	promptRoleMarkerPattern = regexp.MustCompile(`(?i)\b(system|assistant|user)\s*:`)
	promptTagPattern        = regexp.MustCompile(`(?i)<\s*/?\s*(system|assistant|user)(?:\s+[^>]*)?>`)
)

// SanitizeForPromptEmbedding removes NUL bytes and normalizes line endings
// before external text is embedded into prompts.
func SanitizeForPromptEmbedding(s string, opts ...SafeTextOptions) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	s = strings.ReplaceAll(s, "\x00", "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	// Break common prompt-control markers so embedded PR / issue / diff text
	// cannot accidentally terminate or reinterpret surrounding prompt blocks.
	s = strings.ReplaceAll(s, "```", "`\u200b``")
	s = promptRoleMarkerPattern.ReplaceAllStringFunc(s, breakPromptRoleMarker)
	s = promptTagPattern.ReplaceAllStringFunc(s, func(marker string) string {
		return "<\u200b" + strings.TrimPrefix(marker, "<")
	})
	s = breakUntrustedTextTags(s)
	if len(opts) == 0 || !opts[0].Fence {
		return s
	}
	label := sanitizeSafeTextLabel(opts[0].Label)
	if label == "" {
		label = "external"
	}
	return `<untrusted-text source="` + label + `">` + "\n" + s + "\n</untrusted-text>"
}

func breakPromptRoleMarker(marker string) string {
	if marker == "" {
		return marker
	}
	return marker[:1] + "\u200b" + marker[1:]
}

func breakUntrustedTextTags(s string) string {
	for _, marker := range []string{"<untrusted-text", "</untrusted-text"} {
		s = replaceFold(s, marker, "<\u200b"+strings.TrimPrefix(marker, "<"))
	}
	return s
}

func replaceFold(s, old, replacement string) string {
	lowerS := strings.ToLower(s)
	lowerOld := strings.ToLower(old)
	var b strings.Builder
	for {
		i := strings.Index(lowerS, lowerOld)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		b.WriteString(replacement)
		s = s[i+len(old):]
		lowerS = lowerS[i+len(old):]
	}
}

func sanitizeSafeTextLabel(label string) string {
	label = strings.TrimSpace(label)
	var b strings.Builder
	lastSep := false
	for _, r := range label {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
			b.WriteRune(r)
			lastSep = false
		default:
			if !lastSep {
				b.WriteByte('-')
				lastSep = true
			}
		}
	}
	return strings.Trim(b.String(), "-._")
}
