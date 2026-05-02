package harnessinstall

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

func MergeProviderHooksJSON(data []byte) ([]byte, error) {
	return MergeProviderHooksJSONWithTemplate(data, nil)
}

func MergeProviderHooksJSONWithTemplate(data, template []byte) ([]byte, error) {
	stopHook, err := stopHookJSONFromTemplate(template)
	if err != nil {
		return nil, err
	}
	return mergeProviderHooksJSONPreserving(data, stopHook)
}

func mergeProviderHooksJSONPreserving(data, stopHook []byte) ([]byte, error) {
	if len(strings.TrimSpace(string(data))) == 0 {
		return RenderProviderHooksJSONWithStopHook(stopHook), nil
	}
	if !json.Valid(data) {
		return nil, fmt.Errorf("invalid JSON")
	}
	rootStart := firstNonSpace(data, 0)
	if rootStart < 0 || data[rootStart] != '{' {
		return nil, fmt.Errorf("root must be an object")
	}
	hooksSpan, ok, err := findObjectKeyValueSpan(data, rootStart, "hooks")
	if err != nil {
		return nil, err
	}
	if !ok {
		return insertObjectMember(data, rootStart, "hooks", renderHooksObjectJSON(stopHook))
	}
	if data[firstNonSpace(data, hooksSpan.start)] != '{' {
		return nil, fmt.Errorf("hooks must be an object")
	}
	stopSpan, ok, err := findObjectKeyValueSpan(data, hooksSpan.start, "Stop")
	if err != nil {
		return nil, err
	}
	if !ok {
		return insertObjectMember(data, hooksSpan.start, "Stop", renderStopHooksArrayJSON(stopHook))
	}
	if data[firstNonSpace(data, stopSpan.start)] != '[' {
		return nil, fmt.Errorf("hooks.Stop must be an array")
	}
	mergedStop, err := mergeStopHooksArrayJSON(data[stopSpan.start:stopSpan.end], stopHook)
	if err != nil {
		return nil, err
	}
	if bytes.Equal(bytes.TrimSpace(data[stopSpan.start:stopSpan.end]), bytes.TrimSpace(mergedStop)) {
		return ensureTrailingNewlineBytes(data), nil
	}
	out := make([]byte, 0, len(data)-stopSpan.end+stopSpan.start+len(mergedStop)+1)
	out = append(out, data[:stopSpan.start]...)
	out = append(out, mergedStop...)
	out = append(out, data[stopSpan.end:]...)
	return ensureTrailingNewlineBytes(out), nil
}

func RenderProviderHooksJSONWithStopHook(stopHook []byte) []byte {
	if len(bytes.TrimSpace(stopHook)) == 0 {
		stopHook = renderDefaultStopHookJSON()
	}
	return append(renderHooksObjectAsRootJSON(stopHook), '\n')
}

type jsonSpan struct {
	start int
	end   int
}

func stopHookJSONFromTemplate(template []byte) ([]byte, error) {
	if len(bytes.TrimSpace(template)) == 0 {
		return renderDefaultStopHookJSON(), nil
	}
	var config struct {
		Hooks map[string][]json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(template, &config); err != nil {
		return nil, fmt.Errorf("provider hook template is invalid JSON: %w", err)
	}
	for _, raw := range config.Hooks["Stop"] {
		if rawStopHookIsAutoImprove(raw) {
			return bytes.TrimSpace(raw), nil
		}
	}
	return renderDefaultStopHookJSON(), nil
}

func renderDefaultStopHookJSON() []byte {
	out, err := json.MarshalIndent(renderStopHook(), "", "  ")
	if err != nil {
		return []byte(`{"id":"auto-improve.checklist-gate","hooks":[]}`)
	}
	return out
}

func renderHooksObjectAsRootJSON(stopHook []byte) []byte {
	return []byte("{\n  \"hooks\": " + indentJSONValue(renderHooksObjectJSON(stopHook), "  ") + "\n}")
}

func renderHooksObjectJSON(stopHook []byte) []byte {
	return []byte("{\n  \"Stop\": " + indentJSONValue(renderStopHooksArrayJSON(stopHook), "  ") + "\n}")
}

func renderStopHooksArrayJSON(stopHook []byte) []byte {
	return renderRawJSONArray(nil, stopHook)
}

func mergeStopHooksArrayJSON(data, stopHook []byte) ([]byte, error) {
	var rawItems []json.RawMessage
	if err := json.Unmarshal(data, &rawItems); err != nil {
		return nil, err
	}
	kept := make([][]byte, 0, len(rawItems))
	autoCount := 0
	currentAutoHook := false
	for _, raw := range rawItems {
		if rawStopHookIsAutoImprove(raw) {
			autoCount++
			if jsonSemanticallyEqual(raw, stopHook) {
				currentAutoHook = true
			}
			continue
		}
		kept = append(kept, bytes.TrimSpace(raw))
	}
	if autoCount == 1 && currentAutoHook {
		return bytes.TrimSpace(data), nil
	}
	return renderRawJSONArray(kept, stopHook), nil
}

func jsonSemanticallyEqual(left, right []byte) bool {
	var l any
	var r any
	if err := json.Unmarshal(left, &l); err != nil {
		return false
	}
	if err := json.Unmarshal(right, &r); err != nil {
		return false
	}
	return reflect.DeepEqual(l, r)
}

func renderRawJSONArray(items [][]byte, stopHook []byte) []byte {
	if len(bytes.TrimSpace(stopHook)) == 0 {
		stopHook = renderDefaultStopHookJSON()
	}
	items = append(append([][]byte{}, items...), bytes.TrimSpace(stopHook))
	var out strings.Builder
	out.WriteString("[")
	for i, item := range items {
		if i == 0 {
			out.WriteByte('\n')
		} else {
			out.WriteString(",\n")
		}
		out.WriteString(indentJSONValue(item, "  "))
	}
	if len(items) > 0 {
		out.WriteByte('\n')
	}
	out.WriteString("]")
	return []byte(out.String())
}

func rawStopHookIsAutoImprove(raw []byte) bool {
	var item map[string]any
	if err := json.Unmarshal(raw, &item); err != nil {
		return false
	}
	if id, _ := item["id"].(string); id == HookID {
		return true
	}
	return containsAutoImproveCommand(item)
}

func renderStopHook() map[string]any {
	return map[string]any{
		"id": HookID,
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": "sh .auto-improve/hooks/verify-checklist-result.sh",
				"timeout": float64(30),
			},
		},
	}
}

func containsAutoImproveCommand(itemMap map[string]any) bool {
	hookItems, ok := itemMap["hooks"].([]any)
	if !ok {
		return false
	}
	for _, hook := range hookItems {
		hookMap, ok := hook.(map[string]any)
		if !ok {
			continue
		}
		command, _ := hookMap["command"].(string)
		if strings.Contains(command, ".auto-improve/hooks/verify-checklist-result.sh") {
			return true
		}
	}
	return false
}
