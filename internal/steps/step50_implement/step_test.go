package step50_implement

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

func TestStepRun(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		fixture := newStepFixture(t, "fake-claude-success.sh", 30)

		err := Step{}.Run(context.Background(), fixture.runContext())
		require.NoError(t, err)

		manifest := fixture.readManifest(t)
		require.Equal(t, contracts.ManifestKindSuccess, manifest.Kind)
		success, ok := manifest.Value.(contracts.ManifestSuccess)
		require.True(t, ok)
		require.NoError(t, success.Validate())
		assert.Equal(t, "step50-implement-pass2@v1", success.PromptVersion)

		diffPath := fixture.runArtifactPath(t, "diff.patch")
		sessionPath := fixture.runArtifactPath(t, "session.jsonl")
		checklistPath := fixture.runArtifactPath(t, "checklist-result.json")

		assert.FileExists(t, diffPath)
		assert.FileExists(t, sessionPath)
		assert.FileExists(t, checklistPath)
	})

	t.Run("error", func(t *testing.T) {
		fixture := newStepFixture(t, "fake-claude-error.sh", 30)

		err := Step{}.Run(context.Background(), fixture.runContext())
		require.NoError(t, err)

		manifest := fixture.readManifest(t)
		require.Equal(t, contracts.ManifestKindError, manifest.Kind)
		errorManifest, ok := manifest.Value.(contracts.ManifestError)
		require.True(t, ok)
		assert.Equal(t, "rate_limit", errorManifest.Reason)

		fixture.assertArtifactsAbsent(t)
	})

	t.Run("timeout", func(t *testing.T) {
		fixture := newStepFixture(t, "fake-claude-timeout.sh", 1)

		err := Step{}.Run(context.Background(), fixture.runContext())
		require.NoError(t, err)

		manifest := fixture.readManifest(t)
		require.Equal(t, contracts.ManifestKindTimeout, manifest.Kind)
		timeoutManifest, ok := manifest.Value.(contracts.ManifestTimeout)
		require.True(t, ok)
		assert.Equal(t, 1, timeoutManifest.TimeoutSeconds)

		fixture.assertArtifactsAbsent(t)
	})

	t.Run("rule loading", func(t *testing.T) {
		fixture := newStepFixture(t, "fake-claude-success.sh", 30)
		fixture.writeCandidateRules(t, []contracts.Candidate{
			{
				CandidateID:        "cand-1",
				Kind:               contracts.CandidateKindUpdate,
				TargetRuleID:       "rule-abc",
				Title:              "Use the existing rule",
				ProposedBodyPath:   "40/candidates/cand-1.md",
				ProposedBodySha256: strings.Repeat("a", 64),
			},
		})
		fixture.writeRuleSidecar(t, "rule-abc", "rule-abc body\nwith context\n")

		err := Step{}.Run(context.Background(), fixture.runContext())
		require.NoError(t, err)

		sessionBytes, err := os.ReadFile(fixture.runArtifactPath(t, "session.jsonl"))
		require.NoError(t, err)
		assert.Contains(t, string(sessionBytes), "rule-abc body")
		assert.Contains(t, string(sessionBytes), "rule-abc")
	})
}

type stepFixture struct {
	repoRoot     string
	runsBase     string
	worktreeBase string
	runID        contracts.RunID
	runIO        internalio.RunContext
	taskPackage  contracts.TaskPackage
	repoDir      string
	baseSHA      string
	claudeScript string
	timeout      int
}

func newStepFixture(t *testing.T, scriptName string, timeout int) *stepFixture {
	t.Helper()

	repoRoot := sourceRepoRoot(t)
	rootDir := t.TempDir()
	repoDir := filepath.Join(rootDir, "worktree-a1")
	runsBase := filepath.Join(rootDir, "runs")
	worktreeBase := filepath.Join(rootDir, "worktrees")
	require.NoError(t, os.MkdirAll(repoDir, 0o755))
	require.NoError(t, os.MkdirAll(runsBase, 0o755))
	require.NoError(t, os.MkdirAll(worktreeBase, 0o755))

	initGitRepo(t, repoDir)
	baseSHA := gitOutputString(t, repoDir, "rev-parse", "HEAD")

	runID := contracts.RunID("2026-04-21-PR42-abcdef0")
	runIO, err := internalio.NewRunContext(runID, runsBase, worktreeBase)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(runIO.RunDir(), 0o755))

	fixture := &stepFixture{
		repoRoot:     repoRoot,
		runsBase:     runsBase,
		worktreeBase: worktreeBase,
		runID:        runID,
		runIO:        runIO,
		repoDir:      repoDir,
		baseSHA:      baseSHA,
		claudeScript: filepath.Join(testdataDir(t), scriptName),
		timeout:      timeout,
	}
	fixture.taskPackage = fixture.buildTaskPackage(t)

	t.Setenv("STEP50_RUN_ID", string(runID))
	t.Setenv("STEP50_AGENT", "a1")

	return fixture
}

func (f *stepFixture) runContext() *orchestrator.StepRunContext {
	cfg := &config.Config{
		Repo: config.RepoConfig{
			Root: f.repoRoot,
		},
		RunsBasePath:     f.runsBase,
		WorktreeBasePath: f.worktreeBase,
		ClaudeCLIPath:    f.claudeScript,
		StepTimeouts: map[string]int{
			"step50": f.timeout,
		},
	}

	return &orchestrator.StepRunContext{
		Config:      cfg,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Agent:       "a1",
		Pass:        2,
		IO:          f.runIO,
		TaskPackage: &f.taskPackage,
	}
}

func (f *stepFixture) buildTaskPackage(t *testing.T) contracts.TaskPackage {
	t.Helper()

	agents := []contracts.AgentID{"a1", "a2", "a3"}
	worktrees := make([]contracts.WorktreeAllocation, 0, len(agents)*2)
	for pass := 1; pass <= 2; pass++ {
		for _, agent := range agents {
			path := filepath.Join(f.worktreeBase, string(f.runID)+fmtPassAgent(pass, agent))
			if pass == 2 && agent == "a1" {
				path = f.repoDir
			} else {
				require.NoError(t, os.MkdirAll(path, 0o755))
			}
			worktrees = append(worktrees, contracts.WorktreeAllocation{
				Agent:   agent,
				Pass:    pass,
				Path:    path,
				Branch:  "auto-improve/" + string(f.runID) + fmtPassAgent(pass, agent),
				BaseSHA: f.baseSHA,
				HeadSHA: f.baseSHA,
			})
		}
	}

	return contracts.TaskPackage{
		SchemaVersion:           "1",
		RunID:                   f.runID,
		PR:                      42,
		Title:                   "Step50 implementation",
		BaseSHA:                 f.baseSHA,
		BestBranch:              "main",
		ReconstructedTaskPrompt: "Apply pass2 implementation changes.",
		Worktrees:               worktrees,
		CreatedAt:               time.Now().UTC(),
	}
}

func (f *stepFixture) writeCandidateRules(t *testing.T, candidates []contracts.Candidate) {
	t.Helper()

	path, err := f.runIO.ResolveRunRelative(filepath.Join("40", "candidates.json"))
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))

	doc := contracts.Candidates{
		SchemaVersion:  "1",
		RunID:          f.runID,
		Candidates:     candidates,
		CandidatesHash: contracts.CanonicalCandidatesHash(candidates),
		CreatedAt:      time.Now().UTC(),
	}
	require.NoError(t, internalio.WriteJSONAtomic(path, doc))
}

func (f *stepFixture) writeRuleSidecar(t *testing.T, ruleID, body string) {
	t.Helper()

	rulesDir := filepath.Join(f.runsBase, "rules")
	require.NoError(t, os.MkdirAll(rulesDir, 0o755))
	require.NoError(t, internalio.WriteAtomic(filepath.Join(rulesDir, ruleID+".md"), []byte(body)))
}

func (f *stepFixture) readManifest(t *testing.T) contracts.Manifest {
	t.Helper()

	path, err := f.runIO.ManifestPath(2, "a1")
	require.NoError(t, err)
	manifest, err := internalio.ReadJSON[contracts.Manifest](path)
	require.NoError(t, err)
	return manifest
}

func (f *stepFixture) runArtifactPath(t *testing.T, name string) string {
	t.Helper()

	path, err := f.runIO.ResolveRunRelative(filepath.Join("50-pass2", "a1", name))
	require.NoError(t, err)
	return path
}

func (f *stepFixture) assertArtifactsAbsent(t *testing.T) {
	t.Helper()

	for _, name := range []string{"diff.patch", "session.jsonl", "checklist-result.json"} {
		_, err := os.Stat(f.runArtifactPath(t, name))
		require.Error(t, err)
		assert.True(t, os.IsNotExist(err))
	}
}

func initGitRepo(t *testing.T, dir string) {
	t.Helper()

	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "step50@example.com")
	runGit(t, dir, "config", "user.name", "Step50 Test")
	require.NoError(t, internalio.WriteAtomic(filepath.Join(dir, "README.md"), []byte("initial\n")))
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "initial commit")
}

func gitOutputString(t *testing.T, dir string, args ...string) string {
	t.Helper()

	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	output, err := cmd.Output()
	require.NoError(t, err)
	return strings.TrimSpace(string(output))
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()

	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", string(output))
}

func fmtPassAgent(pass int, agent contracts.AgentID) string {
	return fmt.Sprintf("-pass%d-%s", pass, agent)
}

func sourceRepoRoot(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

func testdataDir(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Join(filepath.Dir(file), "testdata")
}
