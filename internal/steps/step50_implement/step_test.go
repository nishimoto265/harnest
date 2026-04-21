package step50_implement

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/orchestrator"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStepRunTerminalVariants(t *testing.T) {
	t.Setenv("FAKE_RUN_ID", "2026-04-21-PR42-abcdef0")
	t.Setenv("FAKE_AGENT", "a1")

	tests := []struct {
		name           string
		script         string
		timeoutSeconds int
		wantKind       contracts.ManifestKind
		wantReason     string
		wantArtifacts  bool
	}{
		{
			name:           "success",
			script:         "fake-claude-success.sh",
			timeoutSeconds: 30,
			wantKind:       contracts.ManifestKindSuccess,
			wantArtifacts:  true,
		},
		{
			name:           "error",
			script:         "fake-claude-error.sh",
			timeoutSeconds: 30,
			wantKind:       contracts.ManifestKindError,
			wantReason:     "rate_limit",
			wantArtifacts:  false,
		},
		{
			name:           "timeout",
			script:         "fake-claude-timeout.sh",
			timeoutSeconds: 1,
			wantKind:       contracts.ManifestKindTimeout,
			wantArtifacts:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newStepTestEnv(t, tt.script, tt.timeoutSeconds)

			start := time.Now()
			err := (Step{}).Run(context.Background(), env.run)
			require.NoError(t, err)
			assert.Less(t, time.Since(start), 8*time.Second)

			manifest := readManifest(t, env.manifestPath)
			assert.Equal(t, tt.wantKind, manifest.Kind)

			switch tt.wantKind {
			case contracts.ManifestKindSuccess:
				success, ok := manifest.Value.(contracts.ManifestSuccess)
				require.True(t, ok)
				assert.Equal(t, filepath.Join("50-pass2", "a1", "diff.patch"), success.DiffPath)
				assert.Equal(t, filepath.Join("50-pass2", "a1", "session.jsonl"), success.SessionPath)
				assert.Equal(t, filepath.Join("50-pass2", "a1", "checklist-result.json"), success.ChecklistPath)

				diffPath := filepath.Join(env.run.IO.RunDir(), success.DiffPath)
				sessionPath := filepath.Join(env.run.IO.RunDir(), success.SessionPath)
				checklistPath := filepath.Join(env.run.IO.RunDir(), success.ChecklistPath)

				diffBytes, err := os.ReadFile(diffPath)
				require.NoError(t, err)
				assert.Contains(t, string(diffBytes), "implemented.txt")

				sessionBytes, err := os.ReadFile(sessionPath)
				require.NoError(t, err)
				assert.Contains(t, string(sessionBytes), `"event":"start"`)

				checklistBytes, err := os.ReadFile(checklistPath)
				require.NoError(t, err)
				assert.Contains(t, string(checklistBytes), `"run_id":"2026-04-21-PR42-abcdef0"`)
			case contracts.ManifestKindError:
				errorVariant, ok := manifest.Value.(contracts.ManifestError)
				require.True(t, ok)
				assert.Equal(t, tt.wantReason, errorVariant.Reason)
				assert.Contains(t, errorVariant.Detail, "rate_limit")
			case contracts.ManifestKindTimeout:
				timeoutVariant, ok := manifest.Value.(contracts.ManifestTimeout)
				require.True(t, ok)
				assert.Equal(t, tt.timeoutSeconds, timeoutVariant.TimeoutSeconds)
			}

			assertArtifactPresence(t, env.run.IO.RunDir(), tt.wantArtifacts)
		})
	}
}

func TestStepRunIncludesRulePayloadsInPrompt(t *testing.T) {
	t.Setenv("FAKE_RUN_ID", "2026-04-21-PR42-abcdef0")
	t.Setenv("FAKE_AGENT", "a1")

	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	promptCapturePath := filepath.Join(t.TempDir(), "prompt.txt")
	t.Setenv("PROMPT_CAPTURE_FILE", promptCapturePath)

	ruleText := "# rule-abc\nAlways preserve API compatibility.\n"
	writeCandidateFixture(t, env.run, contracts.Candidate{
		CandidateID:      "cand-1",
		Kind:             contracts.CandidateKindUpdate,
		TargetRuleID:     "rule-abc",
		Title:            "Refine existing compatibility rule",
		ProposedBodyPath: "40/candidates/cand-1.md",
	}, ruleText)

	err := (Step{}).Run(context.Background(), env.run)
	require.NoError(t, err)

	promptBytes, err := os.ReadFile(promptCapturePath)
	require.NoError(t, err)
	assert.Contains(t, string(promptBytes), "cand-1 (update)")
	assert.Contains(t, string(promptBytes), "target_rule_id: rule-abc")
	assert.Contains(t, string(promptBytes), "Always preserve API compatibility.")
}

func TestStepRunFailsClosedWhenChecklistMissing(t *testing.T) {
	t.Setenv("FAKE_RUN_ID", "2026-04-21-PR42-abcdef0")
	t.Setenv("FAKE_AGENT", "a1")
	t.Setenv("FAKE_SKIP_CHECKLIST", "1")

	env := newStepTestEnv(t, "fake-claude-success.sh", 30)

	err := (Step{}).Run(context.Background(), env.run)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read checklist artifact")
	assertManifestMissing(t, env.manifestPath)
	assertArtifactPresence(t, env.run.IO.RunDir(), false)
}

func TestStepRunReturnsParentContextErrorWithoutManifest(t *testing.T) {
	t.Setenv("FAKE_RUN_ID", "2026-04-21-PR42-abcdef0")
	t.Setenv("FAKE_AGENT", "a1")

	env := newStepTestEnv(t, "fake-claude-timeout.sh", 30)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	err := (Step{}).Run(ctx, env.run)
	require.ErrorIs(t, err, context.Canceled)
	assertManifestMissing(t, env.manifestPath)
	assertArtifactPresence(t, env.run.IO.RunDir(), false)
}

func TestStepRunCleansStaleArtifactsOnNonSuccess(t *testing.T) {
	t.Setenv("FAKE_RUN_ID", "2026-04-21-PR42-abcdef0")
	t.Setenv("FAKE_AGENT", "a1")

	tests := []struct {
		name           string
		script         string
		timeoutSeconds int
		wantKind       contracts.ManifestKind
	}{
		{name: "error", script: "fake-claude-error.sh", timeoutSeconds: 30, wantKind: contracts.ManifestKindError},
		{name: "timeout", script: "fake-claude-timeout.sh", timeoutSeconds: 1, wantKind: contracts.ManifestKindTimeout},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newStepTestEnv(t, tt.script, tt.timeoutSeconds)
			writeStaleArtifacts(t, env.run.IO.RunDir())

			err := (Step{}).Run(context.Background(), env.run)
			require.NoError(t, err)

			manifest := readManifest(t, env.manifestPath)
			assert.Equal(t, tt.wantKind, manifest.Kind)
			assertArtifactPresence(t, env.run.IO.RunDir(), false)
		})
	}
}

func TestStepRunSignalExitWritesValidManifest(t *testing.T) {
	t.Setenv("FAKE_RUN_ID", "2026-04-21-PR42-abcdef0")
	t.Setenv("FAKE_AGENT", "a1")

	env := newStepTestEnv(t, "fake-claude-signal.sh", 30)

	err := (Step{}).Run(context.Background(), env.run)
	require.NoError(t, err)

	manifest := readManifest(t, env.manifestPath)
	require.Equal(t, contracts.ManifestKindError, manifest.Kind)
	errorVariant, ok := manifest.Value.(contracts.ManifestError)
	require.True(t, ok)
	assert.Equal(t, 143, errorVariant.ExitCode)
	assert.Equal(t, "signal", errorVariant.Reason)
	assert.NoError(t, errorVariant.Validate())
}

func TestStepRunTimeoutKillsProcessGroup(t *testing.T) {
	t.Setenv("FAKE_RUN_ID", "2026-04-21-PR42-abcdef0")
	t.Setenv("FAKE_AGENT", "a1")

	env := newStepTestEnv(t, "fake-claude-timeout-child-tree.sh", 1)
	markerPath := filepath.Join(t.TempDir(), "child-alive.txt")
	t.Setenv("FAKE_CHILD_MARKER", markerPath)
	t.Setenv("FAKE_CHILD_DELAY", "2")
	t.Setenv("FAKE_SLEEP_SECONDS", "10")

	err := (Step{}).Run(context.Background(), env.run)
	require.NoError(t, err)

	manifest := readManifest(t, env.manifestPath)
	assert.Equal(t, contracts.ManifestKindTimeout, manifest.Kind)

	time.Sleep(3 * time.Second)
	_, statErr := os.Stat(markerPath)
	require.Error(t, statErr)
	assert.True(t, os.IsNotExist(statErr))
}

func TestStepRunFailsWhenCandidateBodyMissing(t *testing.T) {
	t.Setenv("FAKE_RUN_ID", "2026-04-21-PR42-abcdef0")
	t.Setenv("FAKE_AGENT", "a1")

	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	candidate := contracts.Candidate{
		CandidateID:      "cand-1",
		Kind:             contracts.CandidateKindNew,
		Title:            "Add missing rule",
		ProposedBodyPath: "40/candidates/cand-1.md",
	}
	writeCandidatesDocument(t, env.run, []contracts.Candidate{withCandidateBodyHash(candidate, "# missing\n")})

	err := (Step{}).Run(context.Background(), env.run)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "proposed body missing")
	assertManifestMissing(t, env.manifestPath)
}

func TestLoadRulePayloadsRejectsPathTraversal(t *testing.T) {
	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	candidate := contracts.Candidate{
		CandidateID:      "../cand",
		Kind:             contracts.CandidateKindUpdate,
		TargetRuleID:     "rule-abc",
		Title:            "Bad candidate id",
		ProposedBodyPath: "40/candidates/cand-1.md",
	}
	writeCandidateBody(t, env.run.IO.RunDir(), candidate.ProposedBodyPath, "# body\n")
	candidate = withCandidateBodyHash(candidate, "# body\n")
	candidatesPath := writeCandidatesDocument(t, env.run, []contracts.Candidate{candidate})

	_, err := LoadRulePayloads(env.run.IO.RunDir(), candidatesPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "candidate_id")
}

func TestLoadRulePayloadsRejectsHashMismatch(t *testing.T) {
	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	candidate := contracts.Candidate{
		CandidateID:        "cand-1",
		Kind:               contracts.CandidateKindNew,
		Title:              "Hash mismatch",
		ProposedBodyPath:   "40/candidates/cand-1.md",
		ProposedBodySha256: strings.Repeat("a", 64),
	}
	writeCandidateBody(t, env.run.IO.RunDir(), candidate.ProposedBodyPath, "# body\n")
	candidatesPath := writeCandidatesDocument(t, env.run, []contracts.Candidate{candidate})

	_, err := LoadRulePayloads(env.run.IO.RunDir(), candidatesPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sha mismatch")
}

func TestLocalTimeoutRequiresCommandError(t *testing.T) {
	assert.False(t, isLocalTimeout(nil, context.DeadlineExceeded))
	assert.True(t, isLocalTimeout(context.DeadlineExceeded, context.DeadlineExceeded))
}

func TestTailBufferBoundary(t *testing.T) {
	buf := newTailBuffer(4)

	n, err := buf.Write([]byte("abcd"))
	require.NoError(t, err)
	assert.Equal(t, 4, n)
	assert.Equal(t, "abcd", buf.String())

	n, err = buf.Write([]byte("ef"))
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	assert.Equal(t, "cdef", buf.String())
}

type stepTestEnv struct {
	run          *orchestrator.StepRunContext
	manifestPath string
}

func newStepTestEnv(t *testing.T, script string, timeoutSeconds int) stepTestEnv {
	t.Helper()

	runsBase := t.TempDir()
	worktreeBase := t.TempDir()
	repoDir := t.TempDir()

	baseSHA := initGitRepoWithWorktree(t, repoDir, filepath.Join(worktreeBase, "pass2-a1"))
	runID := contracts.RunID("2026-04-21-PR42-abcdef0")

	taskPackage := contracts.TaskPackage{
		SchemaVersion:           "1",
		RunID:                   runID,
		PR:                      42,
		Title:                   "step50 test",
		BaseSHA:                 baseSHA,
		BestBranch:              "best/main",
		ReconstructedTaskPrompt: "Implement the requested change safely.",
		CreatedAt:               time.Now().UTC(),
		Worktrees: []contracts.WorktreeAllocation{
			{Agent: "a1", Pass: 1, Path: filepath.Join(worktreeBase, "pass1-a1"), Branch: "test/pass1/a1", BaseSHA: baseSHA, HeadSHA: baseSHA},
			{Agent: "a2", Pass: 1, Path: filepath.Join(worktreeBase, "pass1-a2"), Branch: "test/pass1/a2", BaseSHA: baseSHA, HeadSHA: baseSHA},
			{Agent: "a3", Pass: 1, Path: filepath.Join(worktreeBase, "pass1-a3"), Branch: "test/pass1/a3", BaseSHA: baseSHA, HeadSHA: baseSHA},
			{Agent: "a1", Pass: 2, Path: filepath.Join(worktreeBase, "pass2-a1"), Branch: "test/pass2/a1", BaseSHA: baseSHA, HeadSHA: baseSHA},
			{Agent: "a2", Pass: 2, Path: filepath.Join(worktreeBase, "pass2-a2"), Branch: "test/pass2/a2", BaseSHA: baseSHA, HeadSHA: baseSHA},
			{Agent: "a3", Pass: 2, Path: filepath.Join(worktreeBase, "pass2-a3"), Branch: "test/pass2/a3", BaseSHA: baseSHA, HeadSHA: baseSHA},
		},
	}

	runIO, err := internalio.RunContextFromTaskPackage(taskPackage, runsBase, worktreeBase)
	require.NoError(t, err)

	scriptPath := testScriptPath(t, script)
	cfg := &config.Config{
		RunsBasePath:     runsBase,
		WorktreeBasePath: worktreeBase,
		ClaudeCLIPath:    scriptPath,
		StepTimeouts: map[string]int{
			"step50": timeoutSeconds,
		},
	}
	run := &orchestrator.StepRunContext{
		Config:      cfg,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		PR:          42,
		Pass:        2,
		Agent:       "a1",
		IO:          runIO,
		TaskPackage: &taskPackage,
	}
	manifestPath, err := runIO.ManifestPath(2, "a1")
	require.NoError(t, err)

	return stepTestEnv{
		run:          run,
		manifestPath: manifestPath,
	}
}

func initGitRepoWithWorktree(t *testing.T, repoDir, worktreePath string) string {
	t.Helper()

	runCommand(t, "", "git", "init", "-b", "main", repoDir)
	runCommand(t, repoDir, "git", "config", "user.email", "test@example.com")
	runCommand(t, repoDir, "git", "config", "user.name", "Step50 Test")

	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("base\n"), 0o644))
	runCommand(t, repoDir, "git", "add", "README.md")
	runCommand(t, repoDir, "git", "commit", "-m", "base commit")

	baseSHA := strings.TrimSpace(runCommand(t, repoDir, "git", "rev-parse", "HEAD"))
	runCommand(t, repoDir, "git", "worktree", "add", "-b", "test/pass2/a1", worktreePath, "HEAD")
	return baseSHA
}

func runCommand(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	output, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "%s %s failed: %s", name, strings.Join(args, " "), string(output))
	return string(output)
}

func readManifest(t *testing.T, path string) contracts.Manifest {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var manifest contracts.Manifest
	require.NoError(t, contracts.DecodeStrictJSON(data, &manifest))
	return manifest
}

func assertManifestMissing(t *testing.T, path string) {
	t.Helper()
	_, err := os.Stat(path)
	require.Error(t, err)
	assert.True(t, os.IsNotExist(err))
}

func assertArtifactPresence(t *testing.T, runDir string, shouldExist bool) {
	t.Helper()
	for _, rel := range []string{
		filepath.Join("50-pass2", "a1", "diff.patch"),
		filepath.Join("50-pass2", "a1", "session.jsonl"),
		filepath.Join("50-pass2", "a1", "checklist-result.json"),
	} {
		_, err := os.Stat(filepath.Join(runDir, rel))
		if shouldExist {
			require.NoError(t, err, rel)
			continue
		}
		require.Error(t, err, rel)
		assert.True(t, os.IsNotExist(err), rel)
	}
}

func writeStaleArtifacts(t *testing.T, runDir string) {
	t.Helper()
	agentDir := filepath.Join(runDir, "50-pass2", "a1")
	require.NoError(t, os.MkdirAll(agentDir, 0o755))
	for _, rel := range []string{"diff.patch", "session.jsonl", "checklist-result.json"} {
		require.NoError(t, os.WriteFile(filepath.Join(agentDir, rel), []byte("stale"), 0o644))
	}
}

func writeCandidateFixture(t *testing.T, run *orchestrator.StepRunContext, candidate contracts.Candidate, body string) string {
	t.Helper()
	candidate = withCandidateBodyHash(candidate, body)
	writeCandidateBody(t, run.IO.RunDir(), candidate.ProposedBodyPath, body)
	return writeCandidatesDocument(t, run, []contracts.Candidate{candidate})
}

func writeCandidateBody(t *testing.T, runDir, relPath, body string) {
	t.Helper()
	bodyPath := filepath.Join(runDir, relPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(bodyPath), 0o755))
	require.NoError(t, os.WriteFile(bodyPath, []byte(body), 0o644))
}

func writeCandidatesDocument(t *testing.T, run *orchestrator.StepRunContext, candidates []contracts.Candidate) string {
	t.Helper()
	candidatesDoc := contracts.Candidates{
		SchemaVersion:  "1",
		RunID:          run.IO.RunID,
		Candidates:     candidates,
		CandidatesHash: contracts.CanonicalCandidatesHash(candidates),
		CreatedAt:      time.Now().UTC(),
	}
	candidatesPath, err := run.IO.ResolveRunRelative(filepath.Join("40", "candidates.json"))
	require.NoError(t, err)
	require.NoError(t, internalio.WriteJSONAtomic(candidatesPath, candidatesDoc))
	return candidatesPath
}

func withCandidateBodyHash(candidate contracts.Candidate, body string) contracts.Candidate {
	sum := sha256.Sum256([]byte(body))
	candidate.ProposedBodySha256 = hex.EncodeToString(sum[:])
	return candidate
}

func testScriptPath(t *testing.T, name string) string {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	return filepath.Join(wd, "testdata", name)
}
