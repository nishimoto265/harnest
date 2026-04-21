package step50_implement

import (
	"context"
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
	rulesDir := filepath.Join(env.run.IO.RunsBase, "rules")
	require.NoError(t, os.MkdirAll(rulesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(rulesDir, "rule-abc.md"), []byte(ruleText), 0o644))

	candidate := contracts.Candidate{
		CandidateID:        "cand-1",
		Kind:               contracts.CandidateKindUpdate,
		TargetRuleID:       "rule-abc",
		Title:              "Refine existing compatibility rule",
		ProposedBodyPath:   "40/candidates/cand-1.md",
		ProposedBodySha256: strings.Repeat("a", 64),
	}
	candidates := contracts.Candidates{
		SchemaVersion:  "1",
		RunID:          env.run.IO.RunID,
		Candidates:     []contracts.Candidate{candidate},
		CandidatesHash: contracts.CanonicalCandidatesHash([]contracts.Candidate{candidate}),
		CreatedAt:      time.Now().UTC(),
	}
	candidatesPath, err := env.run.IO.ResolveRunRelative(filepath.Join("40", "candidates.json"))
	require.NoError(t, err)
	require.NoError(t, internalio.WriteJSONAtomic(candidatesPath, candidates))

	err = (Step{}).Run(context.Background(), env.run)
	require.NoError(t, err)

	promptBytes, err := os.ReadFile(promptCapturePath)
	require.NoError(t, err)
	assert.Contains(t, string(promptBytes), "rule-abc")
	assert.Contains(t, string(promptBytes), "Always preserve API compatibility.")
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

func testScriptPath(t *testing.T, name string) string {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	return filepath.Join(wd, "testdata", name)
}
