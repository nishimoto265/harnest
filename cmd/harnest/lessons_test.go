package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLessonsNewAndGenerateChecklist(t *testing.T) {
	root := t.TempDir()

	var stdout bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)
	cmd.SetArgs([]string{
		"lessons",
		"new",
		"no-temp-artifact-commit",
		"--root", root,
		"--checklist-item", "作業用ファイルや一時ファイルを実装差分に含めない",
		"--severity", "high",
		"--confidence", "high",
		"--category", "git-hygiene",
	})
	require.NoError(t, cmd.Execute())

	var created map[string]string
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &created))
	assert.Equal(t, "lesson_created", created["event"])
	assert.Equal(t, filepath.Join(root, ".harnest", "lessons", "no-temp-artifact-commit.md"), created["path"])

	stdout.Reset()
	cmd = newRootCmd()
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)
	cmd.SetArgs([]string{"lessons", "generate-checklist", "--root", root})
	require.NoError(t, cmd.Execute())

	var generated map[string]string
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &generated))
	assert.Equal(t, "checklist_generated", generated["event"])
	assert.Equal(t, filepath.Join(root, ".harnest", "checklist.md"), generated["path"])

	checklist, err := os.ReadFile(filepath.Join(root, ".harnest", "checklist.md"))
	require.NoError(t, err)
	assert.Equal(t, "# Checklist\n\n- [ ] `no-temp-artifact-commit` 作業用ファイルや一時ファイルを実装差分に含めない\n", string(checklist))
}

func TestLessonsGenerateChecklistCheckReportsStaleFile(t *testing.T) {
	root := t.TempDir()
	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"lessons",
		"new",
		"preserve-public-api",
		"--root", root,
		"--checklist-item", "既存 API の公開 contract を理由なく変えない",
	})
	require.NoError(t, cmd.Execute())

	checklistPath := filepath.Join(root, ".harnest", "checklist.md")
	require.NoError(t, os.WriteFile(checklistPath, []byte("stale\n"), 0o644))

	cmd = newRootCmd()
	cmd.SetArgs([]string{"lessons", "generate-checklist", "--root", root, "--check"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checklist is stale")
}

func TestLessonsPrepareAndVerifyChecklistResult(t *testing.T) {
	root := t.TempDir()
	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"lessons",
		"new",
		"no-temp-artifact-commit",
		"--root", root,
		"--checklist-item", "作業用ファイルや一時生成物を実装差分に含めない",
	})
	require.NoError(t, cmd.Execute())

	cmd = newRootCmd()
	cmd.SetArgs([]string{"lessons", "generate-checklist", "--root", root})
	require.NoError(t, cmd.Execute())

	var stdout bytes.Buffer
	cmd = newRootCmd()
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)
	cmd.SetArgs([]string{"lessons", "prepare-checklist-result", "--root", root})
	require.NoError(t, cmd.Execute())

	var prepared map[string]string
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &prepared))
	assert.Equal(t, "checklist_result_prepared", prepared["event"])
	resultPath := filepath.Join(root, ".harnest", "work", "checklist-result.md")
	assert.Equal(t, resultPath, prepared["path"])

	data, err := os.ReadFile(resultPath)
	require.NoError(t, err)
	body := strings.Replace(string(data), "- [ ] `no-temp-artifact-commit`", "- [x] `no-temp-artifact-commit`", 1)
	require.NoError(t, os.WriteFile(resultPath, []byte(body), 0o644))

	stdout.Reset()
	cmd = newRootCmd()
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)
	cmd.SetArgs([]string{"lessons", "verify-checklist-result", "--root", root})
	require.NoError(t, cmd.Execute())

	var verified struct {
		Event   string `json:"event"`
		Summary struct {
			Total     int `json:"total"`
			Compliant int `json:"compliant"`
		} `json:"summary"`
	}
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &verified))
	assert.Equal(t, "checklist_result_verified", verified.Event)
	assert.Equal(t, 1, verified.Summary.Total)
	assert.Equal(t, 1, verified.Summary.Compliant)
}

func TestLessonsVerifyChecklistResultRejectsUnresolvedItem(t *testing.T) {
	root := t.TempDir()
	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"lessons",
		"new",
		"no-temp-artifact-commit",
		"--root", root,
		"--checklist-item", "作業用ファイルや一時生成物を実装差分に含めない",
	})
	require.NoError(t, cmd.Execute())

	cmd = newRootCmd()
	cmd.SetArgs([]string{"lessons", "generate-checklist", "--root", root})
	require.NoError(t, cmd.Execute())

	cmd = newRootCmd()
	cmd.SetArgs([]string{"lessons", "prepare-checklist-result", "--root", root})
	require.NoError(t, cmd.Execute())

	cmd = newRootCmd()
	cmd.SetArgs([]string{"lessons", "verify-checklist-result", "--root", root})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is unresolved")
}

func TestLessonsInstallGuidance(t *testing.T) {
	root := t.TempDir()
	var stdout bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)
	cmd.SetArgs([]string{"lessons", "install-guidance", "--root", root, "--provider", "claude,codex"})

	require.NoError(t, cmd.Execute())

	var got struct {
		Event string   `json:"event"`
		Files []string `json:"files"`
	}
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &got))
	assert.Equal(t, "guidance_installed", got.Event)
	assert.Contains(t, got.Files, filepath.Join(root, "CLAUDE.md"))
	assert.Contains(t, got.Files, filepath.Join(root, "AGENTS.md"))
	assert.Contains(t, got.Files, filepath.Join(root, ".harnest", "hooks", "verify-checklist-result.sh"))

	agentsBody, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	require.NoError(t, err)
	assert.Contains(t, string(agentsBody), "@.harnest/checklist.md")
}

func TestLessonsInstallGuidanceDryRunDoesNotWrite(t *testing.T) {
	root := t.TempDir()
	var stdout bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)
	cmd.SetArgs([]string{"lessons", "install-guidance", "--root", root, "--provider", "claude", "--dry-run"})

	require.NoError(t, cmd.Execute())

	var got struct {
		Event string   `json:"event"`
		Files []string `json:"files"`
	}
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &got))
	assert.Equal(t, "guidance_install_planned", got.Event)
	assert.Contains(t, got.Files, filepath.Join(root, "CLAUDE.md"))
	_, err := os.Stat(filepath.Join(root, "CLAUDE.md"))
	assert.True(t, os.IsNotExist(err))
}

func TestLessonsInstallGuidanceCheckReportsStale(t *testing.T) {
	root := t.TempDir()
	cmd := newRootCmd()
	cmd.SetArgs([]string{"lessons", "install-guidance", "--root", root, "--provider", "claude", "--check"})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "guidance is stale")
}
