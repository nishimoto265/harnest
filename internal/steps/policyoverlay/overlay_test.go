package policyoverlay

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nishimoto265/harnest/internal/candidaterules"
	"github.com/nishimoto265/harnest/internal/policyrepo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyWritesWorktreeChecklistAndLessons(t *testing.T) {
	root := t.TempDir()
	activeBody := `---
status: active
severity: high
confidence: high
category: test
---

# r-active

## Checklist Item

Keep existing public API behavior intact.

## Problem

api drift
`
	experimentBody := `---
status: active
severity: medium
confidence: medium
category: test
---

# experiment

## Checklist Item

Verify all supported locale files when adding translation keys.
`

	err := Apply(root, []policyrepo.ActiveRule{{
		RuleID:   "r-active",
		RulePath: "rules/r-active.md",
		Body:     activeBody,
	}}, []ExperimentLesson{{
		ID:   "cand-2026-04-21-pr42-abcdef0-001",
		Body: experimentBody,
	}})
	require.NoError(t, err)

	checklist, err := os.ReadFile(filepath.Join(root, ".harnest", "checklist.md"))
	require.NoError(t, err)
	assert.Equal(t, "# Checklist\n\n- [ ] `cand-2026-04-21-pr42-abcdef0-001` Verify all supported locale files when adding translation keys.\n- [ ] `r-active` Keep existing public API behavior intact.\n", string(checklist))

	active, err := os.ReadFile(filepath.Join(root, ".harnest", "lessons", "r-active.md"))
	require.NoError(t, err)
	assert.Equal(t, activeBody, string(active))
	experiment, err := os.ReadFile(filepath.Join(root, ".harnest", "lessons", "cand-2026-04-21-pr42-abcdef0-001.md"))
	require.NoError(t, err)
	assert.Equal(t, experimentBody, string(experiment))
	assert.FileExists(t, filepath.Join(root, "AGENTS.md"))
	assert.FileExists(t, filepath.Join(root, "CLAUDE.md"))
	assert.FileExists(t, filepath.Join(root, ".claude", "settings.json"))
	assert.FileExists(t, filepath.Join(root, ".codex", "hooks.json"))
}

func TestExperimentsFromRulePayloadsUsesCandidateIDs(t *testing.T) {
	got := ExperimentsFromRulePayloads([]candidaterules.RulePayload{{
		ID:           "cand-1",
		ProposedBody: "# lesson\n",
	}})

	require.Len(t, got, 1)
	assert.Equal(t, "cand-1", got[0].ID)
	assert.Equal(t, "# lesson\n", got[0].Body)
}

func TestApplyRemovesStaleOverlayLessons(t *testing.T) {
	root := t.TempDir()
	stalePath := filepath.Join(root, ".harnest", "lessons", "stale.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(stalePath), 0o755))
	require.NoError(t, os.WriteFile(stalePath, []byte("stale\n"), 0o644))

	err := Apply(root, []policyrepo.ActiveRule{{
		RuleID: "fresh",
		Body:   "# Fresh\n",
	}}, nil)
	require.NoError(t, err)

	assert.NoFileExists(t, stalePath)
	assert.FileExists(t, filepath.Join(root, ".harnest", "lessons", "fresh.md"))
}

func TestApplyWithSnapshotCopiesHarnessOverlayFiles(t *testing.T) {
	root := t.TempDir()
	snapshotDir := t.TempDir()
	hookPath := filepath.Join(snapshotDir, ".harnest", "hooks", "verify-checklist-result.sh")
	require.NoError(t, os.MkdirAll(filepath.Dir(hookPath), 0o755))
	require.NoError(t, os.WriteFile(hookPath, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(snapshotDir, ".harnest", "checklist.md"), []byte("snapshot checklist\n"), 0o644))

	err := ApplyWithSnapshot(root, snapshotDir, []policyrepo.ActiveRule{{
		RuleID: "fresh",
		Body:   "# Fresh\n",
	}}, nil)
	require.NoError(t, err)

	hook, err := os.ReadFile(filepath.Join(root, ".harnest", "hooks", "verify-checklist-result.sh"))
	require.NoError(t, err)
	assert.Equal(t, "#!/bin/sh\nexit 0\n", string(hook))
	checklist, err := os.ReadFile(filepath.Join(root, ".harnest", "checklist.md"))
	require.NoError(t, err)
	assert.Contains(t, string(checklist), "`fresh`")
	assert.NotContains(t, string(checklist), "snapshot checklist")
}

func TestApplyWithSnapshotUsesGuidanceTemplates(t *testing.T) {
	root := t.TempDir()
	snapshotDir := t.TempDir()
	guidanceDir := filepath.Join(snapshotDir, "harnest", "guidance")
	require.NoError(t, os.MkdirAll(guidanceDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(guidanceDir, "AGENTS.md.template"), []byte("<!-- BEGIN HARNEST CHECKLIST -->\nCodex custom checklist @.harnest/checklist.md\n<!-- END HARNEST CHECKLIST -->\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(guidanceDir, "CLAUDE.md.template"), []byte("<!-- BEGIN HARNEST CHECKLIST -->\nClaude custom checklist @.harnest/checklist.md\n<!-- END HARNEST CHECKLIST -->\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(guidanceDir, "provider-hooks.json.template"), []byte(`{"hooks":{"Stop":[{"id":"harnest.checklist-gate","hooks":[{"type":"command","command":"custom-check","timeout":7}]}]}}`), 0o644))

	err := ApplyWithSnapshot(root, snapshotDir, nil, nil)
	require.NoError(t, err)

	agents, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	require.NoError(t, err)
	assert.Contains(t, string(agents), "Codex custom checklist")
	claude, err := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	require.NoError(t, err)
	assert.Contains(t, string(claude), "Claude custom checklist")
	hooks, err := os.ReadFile(filepath.Join(root, ".codex", "hooks.json"))
	require.NoError(t, err)
	assert.Contains(t, string(hooks), "custom-check")
}

func TestApplyRejectsSymlinkedOverlayDir(t *testing.T) {
	root := t.TempDir()
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(root, ".harnest")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	err := Apply(root, nil, nil)

	require.Error(t, err)
	assert.ErrorContains(t, err, "path must be a real directory")
}
