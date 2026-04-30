package policyoverlay

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nishimoto265/auto-improve/internal/candidaterules"
	"github.com/nishimoto265/auto-improve/internal/policyrepo"
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
		ID:   "cand-2026-04-21-PR42-abcdef0-001",
		Body: experimentBody,
	}})
	require.NoError(t, err)

	checklist, err := os.ReadFile(filepath.Join(root, ".auto-improve", "checklist.md"))
	require.NoError(t, err)
	assert.Equal(t, "# Checklist\n\n- [ ] `cand-2026-04-21-PR42-abcdef0-001` Verify all supported locale files when adding translation keys.\n- [ ] `r-active` Keep existing public API behavior intact.\n", string(checklist))

	active, err := os.ReadFile(filepath.Join(root, ".auto-improve", "lessons", "r-active.md"))
	require.NoError(t, err)
	assert.Equal(t, activeBody, string(active))
	experiment, err := os.ReadFile(filepath.Join(root, ".auto-improve", "lessons", "cand-2026-04-21-PR42-abcdef0-001.md"))
	require.NoError(t, err)
	assert.Equal(t, experimentBody, string(experiment))
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
	stalePath := filepath.Join(root, ".auto-improve", "lessons", "stale.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(stalePath), 0o755))
	require.NoError(t, os.WriteFile(stalePath, []byte("stale\n"), 0o644))

	err := Apply(root, []policyrepo.ActiveRule{{
		RuleID: "fresh",
		Body:   "# Fresh\n",
	}}, nil)
	require.NoError(t, err)

	assert.NoFileExists(t, stalePath)
	assert.FileExists(t, filepath.Join(root, ".auto-improve", "lessons", "fresh.md"))
}

func TestApplyRejectsSymlinkedOverlayDir(t *testing.T) {
	root := t.TempDir()
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(root, ".auto-improve")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	err := Apply(root, nil, nil)

	require.Error(t, err)
	assert.ErrorContains(t, err, "path must be a real directory")
}
