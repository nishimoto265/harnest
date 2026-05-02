package harnessinstall

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlanApplyMergesMarkdownIdempotently(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "AGENTS.md")
	require.NoError(t, os.WriteFile(path, []byte("# Existing\n\nKeep me.\n"), 0o644))

	plan, err := Plan(root, InstallOptions{Providers: []string{ProviderCodex}}, PlanOptions{})
	require.NoError(t, err)
	_, err = Apply(plan)
	require.NoError(t, err)

	plan, err = Plan(root, InstallOptions{Providers: []string{ProviderCodex}}, PlanOptions{})
	require.NoError(t, err)
	_, err = Apply(plan)
	require.NoError(t, err)

	body, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(body), "Keep me.")
	assert.Equal(t, 1, strings.Count(string(body), "BEGIN AUTO-IMPROVE CHECKLIST"))
	assert.Equal(t, 1, strings.Count(string(body), "END AUTO-IMPROVE CHECKLIST"))
}

func TestPlanApplyFullInstallIsIdempotent(t *testing.T) {
	root := t.TempDir()

	plan, err := Plan(root, InstallOptions{}, PlanOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, plan.Changes)
	_, err = Apply(plan)
	require.NoError(t, err)

	second, err := Plan(root, InstallOptions{}, PlanOptions{})
	require.NoError(t, err)
	assert.Empty(t, second.Changes)

	checkPlan, err := Plan(root, InstallOptions{}, PlanOptions{Check: true})
	require.NoError(t, err)
	_, err = Apply(checkPlan)
	require.NoError(t, err)
}

func TestMergeProviderHooksJSONReplacesStableHookID(t *testing.T) {
	first, err := MergeProviderHooksJSON([]byte(`{"hooks":{"Stop":[{"id":"auto-improve.checklist-gate","hooks":[{"type":"command","command":"old"}]},{"hooks":[{"type":"command","command":"echo keep"}]}]}}`))
	require.NoError(t, err)
	second, err := MergeProviderHooksJSON(first)
	require.NoError(t, err)

	var config struct {
		Hooks map[string][]struct {
			ID    string `json:"id"`
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	require.NoError(t, json.Unmarshal(second, &config))
	stopHooks := config.Hooks["Stop"]
	require.Len(t, stopHooks, 2)
	assert.Equal(t, "echo keep", stopHooks[0].Hooks[0].Command)
	assert.Equal(t, HookID, stopHooks[1].ID)
	assert.Equal(t, "sh .auto-improve/hooks/verify-checklist-result.sh", stopHooks[1].Hooks[0].Command)
	assert.Equal(t, 1, strings.Count(string(second), HookID))
}

func TestMergeProviderHooksJSONPreservesConfigOutsideStopHooks(t *testing.T) {
	existing := []byte(`{
  "keep_order": true,
  "hooks": {
    "PreToolUse": [
      {"matcher": "*", "hooks": [{"type": "command", "command": "echo pre"}]}
    ],
    "Stop": [
      {"hooks": [{"type": "command", "command": "echo keep"}]}
    ]
  },
  "tail": {"nested": ["value"]}
}
`)

	merged, err := MergeProviderHooksJSON(existing)
	require.NoError(t, err)

	assert.Contains(t, string(merged), `"keep_order": true`)
	assert.Contains(t, string(merged), `"PreToolUse": [
      {"matcher": "*", "hooks": [{"type": "command", "command": "echo pre"}]}
    ]`)
	assert.Contains(t, string(merged), `"tail": {"nested": ["value"]}`)
	assert.Contains(t, string(merged), HookID)
}

func TestMergeProviderHooksJSONUsesTemplateHook(t *testing.T) {
	template := []byte(`{"hooks":{"Stop":[{"id":"auto-improve.checklist-gate","hooks":[{"type":"command","command":"custom-check","timeout":9}]}]}}`)

	merged, err := MergeProviderHooksJSONWithTemplate([]byte(`{"hooks":{"Stop":[]}}`), template)
	require.NoError(t, err)

	assert.Contains(t, string(merged), "custom-check")
	assert.NotContains(t, string(merged), "verify-checklist-result.sh")
}

func TestPlanRejectsMalformedProviderHooksJSON(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".claude", "settings.json"), []byte(`{"hooks":`), 0o644))

	_, err := Plan(root, InstallOptions{Providers: []string{ProviderClaude}}, PlanOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse provider hook config")
}

func TestMergeCodexHooksFeatureEnablesFalseValueAndIgnoresComments(t *testing.T) {
	existing := "# codex_hooks mentioned in a comment\n[features]\ncodex_hooks = false\n"

	merged := mergeCodexHooksFeature(existing)

	assert.Contains(t, merged, "# codex_hooks mentioned in a comment")
	assert.Contains(t, merged, "[features]\ncodex_hooks = true\n")
	assert.NotContains(t, merged, "codex_hooks = false")
}

func TestRenderProviderHooksJSONIncludesStableHookID(t *testing.T) {
	body := RenderProviderHooksJSON()

	assert.Contains(t, body, HookID)
	assert.Contains(t, body, "verify-checklist-result.sh")
}

func TestPlanCheckReportsPendingChangesWithoutWriting(t *testing.T) {
	root := t.TempDir()
	plan, err := Plan(root, InstallOptions{Providers: []string{ProviderClaude}}, PlanOptions{Check: true})
	require.NoError(t, err)

	_, err = Apply(plan)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stale")
	_, statErr := os.Stat(filepath.Join(root, "CLAUDE.md"))
	assert.True(t, os.IsNotExist(statErr))
}
