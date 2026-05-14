package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecoverInspectReportsRegistryIntegrityError(t *testing.T) {
	root := realTempDir(t)
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	require.NoError(t, os.MkdirAll(runsBase, 0o755))
	require.NoError(t, os.MkdirAll(worktreeBase, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(runsBase, "rules-registry.jsonl"), []byte("{\"kind\":\"added\"\n"), 0o644))

	writeTestConfig(t, root, runsBase, worktreeBase)
	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() {
		_ = os.Chdir(originalWD)
	})

	cmd := newRootCmd()
	cmd.SetArgs([]string{"recover", "--inspect"})
	err = cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rules-registry.jsonl integrity check failed")
}

type recoverInspectOutput struct {
	Event      string `json:"event"`
	RunsBase   string `json:"runs_base"`
	RunID      string `json:"run_id,omitempty"`
	RemoteHead string `json:"remote_head,omitempty"`
}

func TestRecoverInspectCreatesPromotionLockWhenAbsent(t *testing.T) {
	root := realTempDir(t)
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	require.NoError(t, os.MkdirAll(runsBase, 0o755))
	require.NoError(t, os.MkdirAll(worktreeBase, 0o755))

	writeTestConfig(t, root, runsBase, worktreeBase)
	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() {
		_ = os.Chdir(originalWD)
	})

	var stdout bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)
	cmd.SetArgs([]string{"recover", "--inspect"})
	require.NoError(t, cmd.Execute())

	assert.FileExists(t, filepath.Join(runsBase, "promotion.lock"))

	var payload recoverInspectOutput
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &payload))
	assert.Equal(t, "recover_inspect", payload.Event)
	assert.Equal(t, runsBase, payload.RunsBase)
}

func TestRecoverInspectWithRunIDUsesRunScopedPath(t *testing.T) {
	root := realTempDir(t)
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	require.NoError(t, os.MkdirAll(runsBase, 0o755))
	require.NoError(t, os.MkdirAll(worktreeBase, 0o755))

	writeTestConfig(t, root, runsBase, worktreeBase)
	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() {
		_ = os.Chdir(originalWD)
	})

	runID := "2026-04-21-PR42-abcdef0"
	runDir := filepath.Join(runsBase, runID)
	require.NoError(t, os.MkdirAll(filepath.Join(runDir, "70"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(runDir, "config.snapshot.yaml"), []byte(
		"repo:\n"+
			"  root: "+root+"\n"+
			"  default_branch: main\n"+
			"  best_branch: harnest/best\n"+
			"paths:\n"+
			"  runs: "+runsBase+"\n"+
			"worktree:\n"+
			"  base: "+worktreeBase+"\n",
	), 0o644))
	require.NoError(t, internalio.WriteJSONAtomic(filepath.Join(runDir, "task-package.json"), contracts.TaskPackage{
		SchemaVersion:           "1",
		RunID:                   contracts.RunID(runID),
		PR:                      42,
		Title:                   "inspect",
		BaseSHA:                 strings.Repeat("1", 40),
		BestBranch:              "harnest/best",
		ReconstructedTaskPrompt: "prompt",
		CreatedAt:               time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
		Worktrees: []contracts.WorktreeAllocation{
			{Agent: "a1", Pass: 1, Path: filepath.Join(worktreeBase, runID+"-pass1-a1"), Branch: "test/pass1/a1", BaseSHA: strings.Repeat("1", 40), HeadSHA: strings.Repeat("1", 40)},
			{Agent: "a2", Pass: 1, Path: filepath.Join(worktreeBase, runID+"-pass1-a2"), Branch: "test/pass1/a2", BaseSHA: strings.Repeat("1", 40), HeadSHA: strings.Repeat("1", 40)},
			{Agent: "a3", Pass: 1, Path: filepath.Join(worktreeBase, runID+"-pass1-a3"), Branch: "test/pass1/a3", BaseSHA: strings.Repeat("1", 40), HeadSHA: strings.Repeat("1", 40)},
			{Agent: "a1", Pass: 2, Path: filepath.Join(worktreeBase, runID+"-pass2-a1"), Branch: "test/pass2/a1", BaseSHA: strings.Repeat("1", 40), HeadSHA: strings.Repeat("1", 40)},
			{Agent: "a2", Pass: 2, Path: filepath.Join(worktreeBase, runID+"-pass2-a2"), Branch: "test/pass2/a2", BaseSHA: strings.Repeat("1", 40), HeadSHA: strings.Repeat("1", 40)},
			{Agent: "a3", Pass: 2, Path: filepath.Join(worktreeBase, runID+"-pass2-a3"), Branch: "test/pass2/a3", BaseSHA: strings.Repeat("1", 40), HeadSHA: strings.Repeat("1", 40)},
		},
	}))
	originalRemoteHead := recoverRemoteHead
	recoverRemoteHead = func(_ context.Context, repoRoot, branch string) (string, error) {
		assert.Equal(t, root, repoRoot)
		assert.Equal(t, "harnest/best", branch)
		return strings.Repeat("d", 40), nil
	}
	t.Cleanup(func() { recoverRemoteHead = originalRemoteHead })

	var stdout bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)
	cmd.SetArgs([]string{"recover", "--inspect", "--run", runID})
	require.NoError(t, cmd.Execute())

	var payload recoverInspectOutput
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &payload))
	assert.Equal(t, "recover_inspect", payload.Event)
	assert.Equal(t, runsBase, payload.RunsBase)
	assert.Equal(t, runID, payload.RunID)
	assert.Equal(t, strings.Repeat("d", 40), payload.RemoteHead)
}

func TestRecoverInspectWithRunIDTimesOutRemoteHeadLookup(t *testing.T) {
	root := realTempDir(t)
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	require.NoError(t, os.MkdirAll(runsBase, 0o755))
	require.NoError(t, os.MkdirAll(worktreeBase, 0o755))

	writeTestConfig(t, root, runsBase, worktreeBase)
	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() {
		_ = os.Chdir(originalWD)
	})

	runID := "2026-04-21-PR42-abcdef0"
	runDir := filepath.Join(runsBase, runID)
	require.NoError(t, os.MkdirAll(filepath.Join(runDir, "70"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(runDir, "config.snapshot.yaml"), []byte(
		"repo:\n"+
			"  root: "+root+"\n"+
			"  default_branch: main\n"+
			"  best_branch: harnest/best\n"+
			"paths:\n"+
			"  runs: "+runsBase+"\n"+
			"worktree:\n"+
			"  base: "+worktreeBase+"\n",
	), 0o644))
	require.NoError(t, internalio.WriteJSONAtomic(filepath.Join(runDir, "task-package.json"), contracts.TaskPackage{
		SchemaVersion:           "1",
		RunID:                   contracts.RunID(runID),
		PR:                      42,
		Title:                   "inspect-timeout",
		BaseSHA:                 strings.Repeat("1", 40),
		BestBranch:              "harnest/best",
		ReconstructedTaskPrompt: "prompt",
		CreatedAt:               time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
		Worktrees: []contracts.WorktreeAllocation{
			{Agent: "a1", Pass: 1, Path: filepath.Join(worktreeBase, runID+"-pass1-a1"), Branch: "test/pass1/a1", BaseSHA: strings.Repeat("1", 40), HeadSHA: strings.Repeat("1", 40)},
			{Agent: "a2", Pass: 1, Path: filepath.Join(worktreeBase, runID+"-pass1-a2"), Branch: "test/pass1/a2", BaseSHA: strings.Repeat("1", 40), HeadSHA: strings.Repeat("1", 40)},
			{Agent: "a3", Pass: 1, Path: filepath.Join(worktreeBase, runID+"-pass1-a3"), Branch: "test/pass1/a3", BaseSHA: strings.Repeat("1", 40), HeadSHA: strings.Repeat("1", 40)},
			{Agent: "a1", Pass: 2, Path: filepath.Join(worktreeBase, runID+"-pass2-a1"), Branch: "test/pass2/a1", BaseSHA: strings.Repeat("1", 40), HeadSHA: strings.Repeat("1", 40)},
			{Agent: "a2", Pass: 2, Path: filepath.Join(worktreeBase, runID+"-pass2-a2"), Branch: "test/pass2/a2", BaseSHA: strings.Repeat("1", 40), HeadSHA: strings.Repeat("1", 40)},
			{Agent: "a3", Pass: 2, Path: filepath.Join(worktreeBase, runID+"-pass2-a3"), Branch: "test/pass2/a3", BaseSHA: strings.Repeat("1", 40), HeadSHA: strings.Repeat("1", 40)},
		},
	}))
	originalRemoteHead := recoverRemoteHead
	originalTimeout := recoverInspectRemoteHeadTimeout
	recoverInspectRemoteHeadTimeout = 25 * time.Millisecond
	recoverRemoteHead = func(ctx context.Context, repoRoot, branch string) (string, error) {
		assert.Equal(t, root, repoRoot)
		assert.Equal(t, "harnest/best", branch)
		<-ctx.Done()
		return "", ctx.Err()
	}
	t.Cleanup(func() {
		recoverRemoteHead = originalRemoteHead
		recoverInspectRemoteHeadTimeout = originalTimeout
	})

	cmd := newRootCmd()
	cmd.SetArgs([]string{"recover", "--inspect", "--run", runID})
	err = cmd.Execute()
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}
