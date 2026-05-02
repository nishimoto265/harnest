package harnessinstall

import (
	"fmt"
	"strings"
)

func MergeManagedMarkdown(existing, block string) (string, error) {
	return upsertTextBlock(existing, markdownBegin, markdownEnd, block)
}

func blockBegin(block string) string {
	if strings.HasPrefix(block, markdownBegin) {
		return markdownBegin
	}
	return commentBegin
}

func blockEnd(block string) string {
	if strings.HasPrefix(block, markdownBegin) {
		return markdownEnd
	}
	return commentEnd
}

func upsertTextBlock(existing, begin, end, block string) (string, error) {
	text := strings.ReplaceAll(existing, "\r\n", "\n")
	beginIndex := strings.Index(text, begin)
	endIndex := strings.Index(text, end)
	switch {
	case beginIndex >= 0 && endIndex >= beginIndex:
		endIndex += len(end)
		next := text[endIndex:]
		if strings.HasPrefix(next, "\n") {
			next = strings.TrimPrefix(next, "\n")
		}
		prefix := strings.TrimRight(text[:beginIndex], "\n")
		if prefix == "" {
			text = strings.TrimRight(block, "\n") + "\n"
		} else {
			text = prefix + "\n\n" + strings.TrimRight(block, "\n") + "\n"
		}
		if strings.TrimSpace(next) != "" {
			text += "\n" + next
		}
	case beginIndex >= 0 || endIndex >= 0:
		return "", fmt.Errorf("malformed managed block")
	default:
		if strings.TrimSpace(text) == "" {
			text = block
		} else {
			text = strings.TrimRight(text, "\n") + "\n\n" + block
		}
	}
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return text, nil
}

func mergeCodexHooksFeature(existing string) string {
	text := strings.ReplaceAll(existing, "\r\n", "\n")
	if strings.TrimSpace(text) == "" {
		return "# BEGIN AUTO-IMPROVE CODEX HOOKS\n[features]\ncodex_hooks = true\n# END AUTO-IMPROVE CODEX HOOKS\n"
	}
	lines := strings.Split(text, "\n")
	featuresStart := -1
	insertAt := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if isTOMLSection(trimmed) {
			if featuresStart >= 0 {
				break
			}
			if trimmed == "[features]" {
				featuresStart = i
				insertAt = i + 1
			}
			continue
		}
		if featuresStart < 0 {
			continue
		}
		if key, ok := tomlAssignmentKey(trimmed); ok && key == "codex_hooks" {
			if tomlBoolAssignmentIsTrue(trimmed) {
				return ensureTrailingNewline(text)
			}
			lines[i] = line[:len(line)-len(strings.TrimLeft(line, " \t"))] + "codex_hooks = true"
			return ensureTrailingNewline(strings.Join(lines, "\n"))
		}
	}
	if featuresStart >= 0 {
		lines = append(lines[:insertAt], append([]string{"codex_hooks = true"}, lines[insertAt:]...)...)
		return ensureTrailingNewline(strings.Join(lines, "\n"))
	}
	block := "\n# BEGIN AUTO-IMPROVE CODEX HOOKS\n[features]\ncodex_hooks = true\n# END AUTO-IMPROVE CODEX HOOKS\n"
	return strings.TrimRight(text, "\n") + block
}

func isTOMLSection(trimmed string) bool {
	return strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]")
}

func tomlAssignmentKey(trimmed string) (string, bool) {
	if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
		return "", false
	}
	left, _, ok := strings.Cut(trimmed, "=")
	if !ok {
		return "", false
	}
	return strings.TrimSpace(left), true
}

func tomlBoolAssignmentIsTrue(trimmed string) bool {
	_, right, ok := strings.Cut(trimmed, "=")
	if !ok {
		return false
	}
	value := strings.TrimSpace(stripTOMLInlineComment(right))
	return value == "true"
}

func stripTOMLInlineComment(value string) string {
	inString := false
	escaped := false
	quote := rune(0)
	for i, r := range value {
		if inString {
			switch {
			case escaped:
				escaped = false
			case r == '\\':
				escaped = true
			case r == quote:
				inString = false
			}
			continue
		}
		switch r {
		case '\'', '"':
			inString = true
			quote = r
		case '#', ';':
			return value[:i]
		}
	}
	return value
}

func ensureTrailingNewline(value string) string {
	if strings.HasSuffix(value, "\n") {
		return value
	}
	return value + "\n"
}

func ensureTrailingNewlineBytes(value []byte) []byte {
	if len(value) == 0 || value[len(value)-1] == '\n' {
		return value
	}
	return append(value, '\n')
}
