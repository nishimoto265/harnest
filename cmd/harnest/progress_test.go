package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nishimoto265/harnest/internal/contracts"
	"github.com/nishimoto265/harnest/internal/orchestrator"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCLIProgressReporterJSONWritesEventsToStdout(t *testing.T) {
	var stdout bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&stdout)
	reporter := newCLIProgressReporter(cmd, cliOutputOptions{JSON: true})

	reporter.OnProgress(context.Background(), orchestrator.ProgressEvent{
		Event: orchestrator.ProgressRunStart,
		RunID: contracts.RunID("2026-04-21-PR42-abcdef0"),
		PR:    42,
	})

	var event orchestrator.ProgressEvent
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &event))
	assert.Equal(t, orchestrator.ProgressRunStart, event.Event)
	assert.Equal(t, 42, event.PR)
}

func TestCLIProgressReporterQuietSuppressesProgress(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	reporter := newCLIProgressReporter(cmd, cliOutputOptions{Quiet: true})

	assert.Nil(t, reporter)
	assert.Empty(t, stdout.String())
	assert.Empty(t, stderr.String())
}

func TestCLIProgressReporterHumanUsesStderr(t *testing.T) {
	t.Setenv("TERM", "dumb")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	runDir := filepath.Join(t.TempDir(), "2026-04-21-PR42-abcdef0")
	reporter := newCLIProgressReporter(cmd, cliOutputOptions{})
	defer reporter.Close()

	reporter.OnProgress(context.Background(), orchestrator.ProgressEvent{
		Event:  orchestrator.ProgressStepStart,
		RunID:  contracts.RunID("2026-04-21-PR42-abcdef0"),
		PR:     42,
		Step:   contracts.FailedStep20,
		Pass:   1,
		RunDir: runDir,
	})

	assert.Empty(t, stdout.String())
	assert.Contains(t, stderr.String(), "[20] pass1")
	assert.Contains(t, stderr.String(), "agent  status")
}

func TestTaskSummaryDisplaysConciseTaskBrief(t *testing.T) {
	runDir := t.TempDir()
	pkg := progressTestTaskPackage(t, runDir)
	pkg.PR = 74
	pkg.Title = "Add About page"
	pkg.ReconstructedTaskPrompt = "# Task\n\n/about ページを追加し、既存のデザイン規則に沿ってプロフィール・導線・レスポンシブ表示を実装する。\n\n" +
		"## Background\nThe original issue text is unavailable, so infer the task from the merged PR evidence.\n\n" +
		"## Task Content\n- About ページから主要導線へ遷移できるようにする。\n- Avoid unrelated refactors or behavior changes.\n\n" +
		"## Source Context\n### Changed Files\n- app/about/page.tsx\n"
	writeProgressTestJSON(t, filepath.Join(runDir, "task-package.json"), pkg)

	lines := taskSummaryLines(runDir)
	output := strings.Join(lines, "\n")

	assert.Contains(t, output, "target PR: PR74 / Add About page")
	assert.Contains(t, output, "task:")
	assert.Contains(t, output, "/about ページを追加")
	assert.Contains(t, output, "About ページから主要導線")
	assert.NotContains(t, output, "Background")
	assert.NotContains(t, output, "Changed Files")
	assert.NotContains(t, output, "Avoid unrelated")
}

func TestDecisionSummaryDisplaysAcceptedAdopt(t *testing.T) {
	runDir := t.TempDir()
	targetSHA := strings.Repeat("a", 40)
	bestSHA := strings.Repeat("b", 40)
	candidatesHash := strings.Repeat("c", 64)
	writeProgressTestJSON(t, filepath.Join(runDir, "70", "decision.json"), contracts.Decision{
		Action: contracts.DecisionActionAdopt,
		Value: contracts.DecisionAdopt{
			Action:         contracts.DecisionActionAdopt,
			SchemaVersion:  "1",
			RunID:          contracts.RunID("2026-04-21-PR42-abcdef0"),
			IdempotencyKey: contracts.ComputeAdoptIdempotencyKey("2026-04-21-PR42-abcdef0", targetSHA, bestSHA, candidatesHash),
			BestShaBefore:  bestSHA,
			TargetSha:      targetSHA,
			CandidatesHash: candidatesHash,
			RegistryAppendResult: contracts.RegistryAppendResult{
				Offset: 0,
				Sha256: strings.Repeat("d", 64),
			},
			DecidedAt: time.Now().UTC(),
		},
	})

	lines := decisionSummaryLines(runDir)
	output := strings.Join(lines, "\n")

	assert.Contains(t, output, "result: accepted")
	assert.Contains(t, output, "decision: improved harness adopted")
}

func TestRunningStepSummaryShowsStep30ScoreProgress(t *testing.T) {
	runDir := t.TempDir()
	for _, dimension := range scoreTestDimensions()[:2] {
		appendProgressTestJSONL(t, filepath.Join(runDir, "30", "scores-A.jsonl"), contracts.ScoreEntry{
			SchemaVersion: "1",
			RunID:         contracts.RunID("2026-04-21-PR42-abcdef0"),
			Pass:          1,
			Agent:         contracts.AgentID("a1"),
			Dimension:     dimension,
			Score:         80,
			VerdictPath:   contracts.VerdictPathSingle,
			RubricVersion: "test",
			PromptVersion: "test",
			ResolvedAt:    time.Now().UTC(),
		})
	}
	for _, dimension := range scoreTestDimensions() {
		appendProgressTestJSONL(t, filepath.Join(runDir, "30", "scores-A.jsonl"), contracts.ScoreEntry{
			SchemaVersion: "1",
			RunID:         contracts.RunID("2026-04-21-PR42-abcdef0"),
			Pass:          1,
			Agent:         contracts.AgentID("a2"),
			Dimension:     dimension,
			Score:         90,
			VerdictPath:   contracts.VerdictPathSingle,
			RubricVersion: "test",
			PromptVersion: "test",
			ResolvedAt:    time.Now().UTC(),
		})
	}
	appendProgressTestJSONL(t, filepath.Join(runDir, "30", "issues-A.jsonl"), contracts.IssueEntry{
		SchemaVersion:  "1",
		RunID:          contracts.RunID("2026-04-21-PR42-abcdef0"),
		Pass:           1,
		Agent:          contracts.AgentID("a1"),
		JudgeRole:      contracts.JudgeRolePrimary,
		IssueID:        "issue-a1",
		Severity:       contracts.IssueSeverityHigh,
		Category:       "test",
		Title:          "test issue",
		Evidence:       "evidence",
		ProposedLesson: "lesson",
		ChecklistItem:  "check item",
		OutputSha256:   strings.Repeat("a", 64),
		RubricVersion:  "test",
		PromptVersion:  "test",
		ResolvedAt:     time.Now().UTC(),
	})

	lines := runningStepSummaryLines(orchestrator.ProgressEvent{
		Step:   contracts.FailedStep30,
		RunDir: runDir,
	})
	output := strings.Join(lines, "\n")

	assert.Contains(t, output, "agent  status")
	assert.Contains(t, output, "a1")
	assert.Contains(t, output, "scoring")
	assert.Contains(t, output, "2/5")
	assert.Contains(t, output, "a2")
	assert.Contains(t, output, "scored")
	assert.Contains(t, output, "5/5")
	assert.Contains(t, output, "1")
}

func TestRunningStepSummaryShowsStep60PairwiseProgress(t *testing.T) {
	runDir := t.TempDir()
	for _, dimension := range scoreTestDimensions() {
		appendProgressTestJSONL(t, filepath.Join(runDir, "30", "scores-A.jsonl"), contracts.ScoreEntry{
			SchemaVersion: "1",
			RunID:         contracts.RunID("2026-04-21-PR42-abcdef0"),
			Pass:          1,
			Agent:         contracts.AgentID("a1"),
			Dimension:     dimension,
			Score:         70,
			VerdictPath:   contracts.VerdictPathSingle,
			RubricVersion: "test",
			PromptVersion: "test",
			ResolvedAt:    time.Now().UTC(),
		})
		appendProgressTestJSONL(t, filepath.Join(runDir, "60", "scores-B.jsonl"), contracts.ScoreEntry{
			SchemaVersion: "1",
			RunID:         contracts.RunID("2026-04-21-PR42-abcdef0"),
			Pass:          2,
			Agent:         contracts.AgentID("a1"),
			Dimension:     dimension,
			Score:         85,
			VerdictPath:   contracts.VerdictPathSingle,
			RubricVersion: "test",
			PromptVersion: "test",
			ResolvedAt:    time.Now().UTC(),
		})
	}
	appendProgressTestJSONL(t, filepath.Join(runDir, "60", "pairwise.jsonl"), contracts.PairwiseEntry{
		SchemaVersion: "1",
		RunID:         contracts.RunID("2026-04-21-PR42-abcdef0"),
		AgentA:        contracts.AgentID("a1"),
		AgentB:        contracts.AgentID("a1"),
		Winner:        contracts.PairwiseWinnerB,
		Margin:        contracts.PairwiseMarginClear,
		VerdictPath:   contracts.VerdictPathSingle,
		RubricVersion: "test",
		PromptVersion: "test",
		ResolvedAt:    time.Now().UTC(),
	})

	lines := runningStepSummaryLines(orchestrator.ProgressEvent{
		Step:   contracts.FailedStep60,
		RunDir: runDir,
	})
	output := strings.Join(lines, "\n")

	assert.Contains(t, output, "agent  status")
	assert.Contains(t, output, "a1")
	assert.Contains(t, output, "compared")
	assert.Contains(t, output, "70.0")
	assert.Contains(t, output, "85.0")
	assert.Contains(t, output, "pass2")
}

func appendProgressTestJSONL(t *testing.T, path string, value any) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	data, err := json.Marshal(value)
	require.NoError(t, err)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	defer file.Close()
	_, err = file.Write(append(data, '\n'))
	require.NoError(t, err)
}

func writeProgressTestJSON(t *testing.T, path string, value any) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	data, err := json.Marshal(value)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o644))
}

func progressTestTaskPackage(t *testing.T, runDir string) contracts.TaskPackage {
	t.Helper()
	worktreeRoot := filepath.Join(runDir, "worktrees")
	sha := strings.Repeat("a", 40)
	worktrees := make([]contracts.WorktreeAllocation, 0, 6)
	for _, pass := range []int{1, 2} {
		for _, agent := range []contracts.AgentID{"a1", "a2", "a3"} {
			worktrees = append(worktrees, contracts.WorktreeAllocation{
				Agent:   agent,
				Pass:    pass,
				Path:    filepath.Join(worktreeRoot, "pass"+strconv.Itoa(pass), string(agent)),
				Branch:  "harnest/test/pass" + strconv.Itoa(pass) + "/" + string(agent),
				BaseSHA: sha,
				HeadSHA: sha,
			})
		}
	}
	return contracts.TaskPackage{
		SchemaVersion:           "1",
		RunID:                   contracts.RunID("2026-04-21-PR42-abcdef0"),
		PR:                      42,
		Title:                   "test task",
		BaseSHA:                 sha,
		BestBranch:              "harnest/best",
		ReconstructedTaskPrompt: "# Task\n\nTest task.\n",
		Worktrees:               worktrees,
		CreatedAt:               time.Now().UTC(),
	}
}

func scoreTestDimensions() []contracts.Dimension {
	return []contracts.Dimension{
		contracts.DimensionFidelity,
		contracts.DimensionCorrectness,
		contracts.DimensionMaintainability,
		contracts.DimensionDiscipline,
		contracts.DimensionCommunication,
	}
}
