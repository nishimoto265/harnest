package harnessinstall

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

func insertObjectMember(data []byte, objectStart int, key string, value []byte) ([]byte, error) {
	objectEnd, err := skipJSONValue(data, objectStart)
	if err != nil {
		return nil, err
	}
	closeIndex := objectEnd - 1
	closeIndent := lineIndent(data, closeIndex)
	memberIndent := closeIndent + "  "
	member := formatJSONMember(key, value, memberIndent)
	contentStart := firstNonSpace(data, objectStart+1)
	emptyObject := contentStart == closeIndex
	if emptyObject {
		out := make([]byte, 0, len(data)+len(member)+2)
		out = append(out, data[:objectStart+1]...)
		out = append(out, '\n')
		out = append(out, member...)
		out = append(out, '\n')
		out = append(out, []byte(closeIndent)...)
		out = append(out, data[closeIndex:]...)
		return ensureTrailingNewlineBytes(out), nil
	}
	prefix := bytes.TrimRight(data[:closeIndex], " \t\r\n")
	out := make([]byte, 0, len(prefix)+len(member)+len(data[closeIndex:])+4)
	out = append(out, prefix...)
	out = append(out, ',')
	out = append(out, '\n')
	out = append(out, member...)
	out = append(out, '\n')
	out = append(out, []byte(closeIndent)...)
	out = append(out, data[closeIndex:]...)
	return ensureTrailingNewlineBytes(out), nil
}

func formatJSONMember(key string, value []byte, indent string) []byte {
	return []byte(indent + strconv.Quote(key) + ": " + indentJSONValue(value, indent))
}

func indentJSONValue(value []byte, indent string) string {
	lines := strings.Split(strings.TrimRight(string(value), "\n"), "\n")
	if len(lines) == 0 {
		return ""
	}
	var out strings.Builder
	out.WriteString(lines[0])
	for _, line := range lines[1:] {
		out.WriteByte('\n')
		out.WriteString(indent)
		out.WriteString(line)
	}
	return out.String()
}

func findObjectKeyValueSpan(data []byte, objectStart int, key string) (jsonSpan, bool, error) {
	i := firstNonSpace(data, objectStart)
	if i < 0 || data[i] != '{' {
		return jsonSpan{}, false, fmt.Errorf("expected object")
	}
	i = firstNonSpace(data, i+1)
	if i < 0 {
		return jsonSpan{}, false, fmt.Errorf("unterminated object")
	}
	if data[i] == '}' {
		return jsonSpan{}, false, nil
	}
	for {
		foundKey, keyEnd, err := parseJSONString(data, i)
		if err != nil {
			return jsonSpan{}, false, err
		}
		i = firstNonSpace(data, keyEnd)
		if i < 0 || data[i] != ':' {
			return jsonSpan{}, false, fmt.Errorf("expected object key separator")
		}
		valueStart := firstNonSpace(data, i+1)
		if valueStart < 0 {
			return jsonSpan{}, false, fmt.Errorf("missing object value")
		}
		valueEnd, err := skipJSONValue(data, valueStart)
		if err != nil {
			return jsonSpan{}, false, err
		}
		if foundKey == key {
			return jsonSpan{start: valueStart, end: valueEnd}, true, nil
		}
		i = firstNonSpace(data, valueEnd)
		if i < 0 {
			return jsonSpan{}, false, fmt.Errorf("unterminated object")
		}
		switch data[i] {
		case ',':
			i = firstNonSpace(data, i+1)
			if i < 0 {
				return jsonSpan{}, false, fmt.Errorf("unterminated object")
			}
		case '}':
			return jsonSpan{}, false, nil
		default:
			return jsonSpan{}, false, fmt.Errorf("expected object delimiter")
		}
	}
}

func parseJSONString(data []byte, start int) (string, int, error) {
	if start < 0 || start >= len(data) || data[start] != '"' {
		return "", 0, fmt.Errorf("expected JSON string")
	}
	escaped := false
	for i := start + 1; i < len(data); i++ {
		switch {
		case escaped:
			escaped = false
		case data[i] == '\\':
			escaped = true
		case data[i] == '"':
			raw := string(data[start : i+1])
			value, err := strconv.Unquote(raw)
			if err != nil {
				return "", 0, err
			}
			return value, i + 1, nil
		}
	}
	return "", 0, fmt.Errorf("unterminated JSON string")
}

func skipJSONValue(data []byte, start int) (int, error) {
	i := firstNonSpace(data, start)
	if i < 0 {
		return 0, fmt.Errorf("missing JSON value")
	}
	switch data[i] {
	case '"':
		_, end, err := parseJSONString(data, i)
		return end, err
	case '{', '[':
		return skipCompositeJSON(data, i)
	default:
		end := i
		for end < len(data) {
			switch data[end] {
			case ',', '}', ']', ' ', '\t', '\r', '\n':
				if !json.Valid(bytes.TrimSpace(data[i:end])) {
					return 0, fmt.Errorf("invalid JSON scalar")
				}
				return end, nil
			default:
				end++
			}
		}
		if !json.Valid(bytes.TrimSpace(data[i:end])) {
			return 0, fmt.Errorf("invalid JSON scalar")
		}
		return end, nil
	}
}

func skipCompositeJSON(data []byte, start int) (int, error) {
	stack := []byte{}
	inString := false
	escaped := false
	for i := start; i < len(data); i++ {
		ch := data[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case ch == '\\':
				escaped = true
			case ch == '"':
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case '{':
			stack = append(stack, '}')
		case '[':
			stack = append(stack, ']')
		case '}', ']':
			if len(stack) == 0 || stack[len(stack)-1] != ch {
				return 0, fmt.Errorf("mismatched JSON delimiter")
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				return i + 1, nil
			}
		}
	}
	return 0, fmt.Errorf("unterminated JSON value")
}

func firstNonSpace(data []byte, start int) int {
	for i := start; i < len(data); i++ {
		switch data[i] {
		case ' ', '\t', '\r', '\n':
			continue
		default:
			return i
		}
	}
	return -1
}

func lineIndent(data []byte, pos int) string {
	lineStart := pos
	for lineStart > 0 && data[lineStart-1] != '\n' {
		lineStart--
	}
	lineEnd := lineStart
	for lineEnd < len(data) && (data[lineEnd] == ' ' || data[lineEnd] == '\t') {
		lineEnd++
	}
	return string(data[lineStart:lineEnd])
}
