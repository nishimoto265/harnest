package judges

import "strings"

// PromptVersionedJudge lets provider-backed judges add provider/profile/prompt
// identity to persisted scoring versions so resume comparisons fail on changes.
type PromptVersionedJudge interface {
	JudgePromptVersion() string
}

// PanelPromptVersion combines the step prompt version with any judge-specific
// prompt/provider fingerprints. Stubs and tests that do not implement the hook
// keep the historical base version.
func PanelPromptVersion(base string, panel ...Judge) string {
	parts := make([]string, 0, len(panel)+1)
	if base != "" {
		parts = append(parts, base)
	}
	seen := map[string]struct{}{}
	for _, judge := range panel {
		versioned, ok := judge.(PromptVersionedJudge)
		if !ok {
			continue
		}
		version := strings.TrimSpace(versioned.JudgePromptVersion())
		if version == "" || version == base {
			continue
		}
		if _, ok := seen[version]; ok {
			continue
		}
		seen[version] = struct{}{}
		parts = append(parts, version)
	}
	return strings.Join(parts, "+")
}
