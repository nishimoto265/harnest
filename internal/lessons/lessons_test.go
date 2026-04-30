package lessons

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateLessonAndGenerateChecklist(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)

	path, err := CreateLesson(NewLessonRequest{
		Root:          root,
		ID:            "no-temp-artifact-commit",
		ChecklistItem: "作業用ファイルや一時ファイルを実装差分に含めない",
		Severity:      SeverityHigh,
		Confidence:    ConfidenceHigh,
		Category:      "git-hygiene",
		Now:           func() time.Time { return now },
	})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, ".auto-improve", "lessons", "no-temp-artifact-commit.md"), path)

	body, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(body), "# no-temp-artifact-commit")
	assert.Contains(t, string(body), "created_at: 2026-04-26T12:00:00Z")

	checklist, err := GenerateChecklist(root)
	require.NoError(t, err)
	assert.Equal(t, "# Checklist\n\n- [ ] `no-temp-artifact-commit` 作業用ファイルや一時ファイルを実装差分に含めない\n", checklist)
}

func TestRenderChecklistSortsActiveLessonsBySeverity(t *testing.T) {
	checklist := RenderChecklist([]Lesson{
		{
			ID:            "low-one",
			Metadata:      Metadata{Status: StatusActive, Severity: SeverityLow},
			ChecklistItem: "low item",
		},
		{
			ID:            "high-one",
			Metadata:      Metadata{Status: StatusActive, Severity: SeverityHigh},
			ChecklistItem: "high item",
		},
		{
			ID:            "archived-one",
			Metadata:      Metadata{Status: StatusArchived, Severity: SeverityCritical},
			ChecklistItem: "archived item",
		},
	})

	assert.Equal(t, "# Checklist\n\n- [ ] `high-one` high item\n- [ ] `low-one` low item\n", checklist)
}

func TestValidateIDRejectsNonCanonicalSlugs(t *testing.T) {
	for _, id := range []string{"Upper", "trailing-", "-leading", "two--hyphens"} {
		t.Run(id, func(t *testing.T) {
			require.Error(t, ValidateID(id))
		})
	}
}

func TestLoadRejectsHeadingMismatch(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".auto-improve", "lessons", "lesson-id.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(`---
status: active
severity: medium
confidence: medium
category: general
---

# other-id

## Checklist Item

Do the thing.
`), 0o644))

	_, err := Load(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `heading "other-id" must match filename id "lesson-id"`)
}

func TestLoadRejectsMultipleFrontMatterDocuments(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".auto-improve", "lessons", "lesson-id.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(`---
status: active
severity: medium
confidence: medium
category: general
...
--- # second document
status: active
---

# lesson-id

## Checklist Item

Do the thing.
`), 0o644))

	_, err := Load(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "front matter must contain exactly one YAML document")
}

func TestWriteChecklistAllowsMissingLessonsDir(t *testing.T) {
	root := t.TempDir()
	path, err := WriteChecklist(root)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, ".auto-improve", "checklist.md"), path)

	body, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "# Checklist\n\nNo active lessons.\n", string(body))
}

func TestPrepareAndVerifyChecklistResult(t *testing.T) {
	root := t.TempDir()
	_, err := CreateLesson(NewLessonRequest{
		Root:          root,
		ID:            "no-temp-artifact-commit",
		ChecklistItem: "作業用ファイルや一時生成物を実装差分に含めない",
		Severity:      SeverityHigh,
	})
	require.NoError(t, err)
	_, err = CreateLesson(NewLessonRequest{
		Root:          root,
		ID:            "preserve-public-api",
		ChecklistItem: "既存 API の公開 contract を理由なく変えない",
		Severity:      SeverityMedium,
	})
	require.NoError(t, err)
	_, err = WriteChecklist(root)
	require.NoError(t, err)

	resultPath, err := PrepareChecklistResult(root, false)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, ".auto-improve", "work", "checklist-result.md"), resultPath)

	data, err := os.ReadFile(resultPath)
	require.NoError(t, err)
	body := strings.Replace(string(data), "- [ ] `no-temp-artifact-commit`", "- [x] `no-temp-artifact-commit`", 1)
	body = strings.Replace(body, "- [ ] `preserve-public-api`", "- [-] `preserve-public-api`", 1)
	require.NoError(t, os.WriteFile(resultPath, []byte(body), 0o644))

	summary, err := VerifyChecklistResult(root)
	require.NoError(t, err)
	assert.Equal(t, ChecklistResultSummary{
		Total:         2,
		Compliant:     1,
		NotApplicable: 1,
	}, summary)
}

func TestVerifyChecklistResultAllowsExceptionWithReason(t *testing.T) {
	root := t.TempDir()
	_, err := CreateLesson(NewLessonRequest{
		Root:          root,
		ID:            "preserve-public-api",
		ChecklistItem: "既存 API の公開 contract を理由なく変えない",
	})
	require.NoError(t, err)
	_, err = WriteChecklist(root)
	require.NoError(t, err)
	resultPath, err := PrepareChecklistResult(root, false)
	require.NoError(t, err)

	data, err := os.ReadFile(resultPath)
	require.NoError(t, err)
	body := strings.Replace(string(data), "- [ ] `preserve-public-api`", "- [!] `preserve-public-api`", 1)
	body += "  reason: このタスクは意図的に公開 API を変更するため\n"
	require.NoError(t, os.WriteFile(resultPath, []byte(body), 0o644))

	summary, err := VerifyChecklistResult(root)
	require.NoError(t, err)
	assert.Equal(t, ChecklistResultSummary{
		Total:     1,
		Exception: 1,
	}, summary)
}

func TestVerifyChecklistResultRejectsUnresolvedItem(t *testing.T) {
	root := t.TempDir()
	_, err := CreateLesson(NewLessonRequest{
		Root:          root,
		ID:            "no-temp-artifact-commit",
		ChecklistItem: "作業用ファイルや一時生成物を実装差分に含めない",
	})
	require.NoError(t, err)
	_, err = WriteChecklist(root)
	require.NoError(t, err)
	_, err = PrepareChecklistResult(root, false)
	require.NoError(t, err)

	_, err = VerifyChecklistResult(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is unresolved")
}

func TestVerifyChecklistResultRejectsExceptionWithoutReason(t *testing.T) {
	root := t.TempDir()
	_, err := CreateLesson(NewLessonRequest{
		Root:          root,
		ID:            "preserve-public-api",
		ChecklistItem: "既存 API の公開 contract を理由なく変えない",
	})
	require.NoError(t, err)
	_, err = WriteChecklist(root)
	require.NoError(t, err)
	resultPath, err := PrepareChecklistResult(root, false)
	require.NoError(t, err)

	data, err := os.ReadFile(resultPath)
	require.NoError(t, err)
	body := strings.Replace(string(data), "- [ ] `preserve-public-api`", "- [!] `preserve-public-api`", 1)
	require.NoError(t, os.WriteFile(resultPath, []byte(body), 0o644))

	_, err = VerifyChecklistResult(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires an indented reason")
}

func TestInstallGuidanceAddsManagedBlocksAndHooks(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("# Existing\n\nKeep me.\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".claude", "settings.json"), []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"echo existing"}]}]}}`), 0o644))

	result, err := InstallGuidance(InstallGuidanceOptions{Root: root})
	require.NoError(t, err)
	assert.Contains(t, result.Files, filepath.Join(root, "CLAUDE.md"))
	assert.Contains(t, result.Files, filepath.Join(root, "AGENTS.md"))
	assert.Contains(t, result.Files, filepath.Join(root, ".auto-improve", "hooks", "verify-checklist-result.sh"))
	assert.Contains(t, result.Files, filepath.Join(root, ".claude", "settings.json"))
	assert.Contains(t, result.Files, filepath.Join(root, ".codex", "hooks.json"))
	assert.Contains(t, result.Files, filepath.Join(root, ".codex", "config.toml"))
	assert.Contains(t, result.Files, filepath.Join(root, ".gitignore"))

	agentsBody, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	require.NoError(t, err)
	assert.Contains(t, string(agentsBody), "Keep me.")
	assert.Contains(t, string(agentsBody), "@.auto-improve/checklist.md")
	assert.Contains(t, string(agentsBody), "auto-improve lessons verify-checklist-result")

	claudeSettings, err := os.ReadFile(filepath.Join(root, ".claude", "settings.json"))
	require.NoError(t, err)
	assert.Contains(t, string(claudeSettings), "echo existing")
	assert.Contains(t, string(claudeSettings), ".auto-improve/hooks/verify-checklist-result.sh")

	codexHooks, err := os.ReadFile(filepath.Join(root, ".codex", "hooks.json"))
	require.NoError(t, err)
	assert.Contains(t, string(codexHooks), ".auto-improve/hooks/verify-checklist-result.sh")

	codexConfig, err := os.ReadFile(filepath.Join(root, ".codex", "config.toml"))
	require.NoError(t, err)
	assert.Contains(t, string(codexConfig), "codex_hooks = true")

	gitignore, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	require.NoError(t, err)
	assert.Contains(t, string(gitignore), ".auto-improve/work/")
}

func TestInstallGuidanceIsIdempotent(t *testing.T) {
	root := t.TempDir()
	_, err := InstallGuidance(InstallGuidanceOptions{Root: root, Providers: []string{"claude"}})
	require.NoError(t, err)
	_, err = InstallGuidance(InstallGuidanceOptions{Root: root, Providers: []string{"claude"}})
	require.NoError(t, err)

	body, err := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(body), "BEGIN AUTO-IMPROVE CHECKLIST"))

	settings, err := os.ReadFile(filepath.Join(root, ".claude", "settings.json"))
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(settings), ".auto-improve/hooks/verify-checklist-result.sh"))
}
