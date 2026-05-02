package step50_implement

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/steps/agentrunner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func requireProcessInspection(t *testing.T) {
	t.Helper()
	startTime, err := agentrunner.LookupProcessStartTime(os.Getpid())
	if err != nil || startTime == "" || strings.HasPrefix(startTime, "unavailable:") {
		t.Skipf("process inspection unavailable in this sandbox: %v", err)
	}
	requireProcessDescendantVisibility(t)
}

func requireProcessDescendantVisibility(t *testing.T) {
	t.Helper()
	cmd := exec.Command("sleep", "5")
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	})
	if !psShowsChildOfCurrentProcess(t, cmd.Process.Pid) {
		t.Skip("process descendant listing unavailable in this sandbox")
	}
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
}

func psShowsChildOfCurrentProcess(t *testing.T, childPID int) bool {
	t.Helper()
	out, err := exec.Command("ps", "-axo", "pid=,ppid=").Output()
	if err != nil {
		return false
	}
	parentPID := os.Getpid()
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		pid, pidErr := strconv.Atoi(fields[0])
		ppid, ppidErr := strconv.Atoi(fields[1])
		if pidErr == nil && ppidErr == nil && pid == childPID && ppid == parentPID {
			return true
		}
	}
	return false
}

type failBeforeStartRunner struct{}

func (failBeforeStartRunner) Run(context.Context, runnerRequest) (runnerResult, error) {
	return runnerResult{}, errors.New("synthetic start failure")
}

type cancelAfterSuccessRunner struct {
	cancel func()
	runID  contracts.RunID
	agent  contracts.AgentID
}

func (r cancelAfterSuccessRunner) Run(_ context.Context, req runnerRequest) (runnerResult, error) {
	startedAt := time.Now().Add(-time.Second).UTC()
	if req.OnStart != nil {
		if err := req.OnStart(agentrunner.ProcessLease{
			PID:       4242,
			PGID:      4242,
			StartTime: "Tue Apr 22 10:00:00 2026",
		}, startedAt); err != nil {
			return runnerResult{}, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(req.SessionPath), 0o755); err != nil {
		return runnerResult{}, err
	}
	if err := os.WriteFile(req.SessionPath, []byte("{\"event\":\"ok\"}\n"), 0o644); err != nil {
		return runnerResult{}, err
	}
	if err := os.WriteFile(filepath.Join(req.Workdir, "implemented.txt"), []byte("ok\n"), 0o644); err != nil {
		return runnerResult{}, err
	}
	if err := os.WriteFile(filepath.Join(req.Workdir, checklistFileName), []byte(`{"schema_version":"1","run_id":"`+string(r.runID)+`","pass":2,"agent":"`+string(r.agent)+`","items":[]}`), 0o644); err != nil {
		return runnerResult{}, err
	}
	_, err := gitOutputContext(context.Background(), strings.TrimSpace, req.Workdir, "add", "implemented.txt")
	if err != nil {
		return runnerResult{}, err
	}
	_, err = gitOutputContext(context.Background(), strings.TrimSpace, req.Workdir, "commit", "-m", "synthetic success")
	if err != nil {
		return runnerResult{}, err
	}
	if r.cancel != nil {
		r.cancel()
	}
	return runnerResult{
		StartedAt:  startedAt,
		FinishedAt: time.Now().UTC(),
		Lease: agentrunner.ProcessLease{
			PID:       4242,
			PGID:      4242,
			StartTime: "Tue Apr 22 10:00:00 2026",
		},
	}, nil
}

type stepTestEnv struct {
	run          RunContext
	manifestPath string
	repoDir      string
}

func newStepTestEnv(t *testing.T, script string, timeoutSeconds int) stepTestEnv {
	t.Helper()

	runsBase := t.TempDir()
	worktreeBase := t.TempDir()
	repoDir := t.TempDir()
	runID := contracts.RunID("2026-04-21-PR42-abcdef0")

	baseSHA := initGitRepoWithWorktree(t, repoDir, filepath.Join(worktreeBase, fmt.Sprintf("%s-pass2-a1", runID)))

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
			{Agent: "a1", Pass: 1, Path: filepath.Join(worktreeBase, fmt.Sprintf("%s-pass1-a1", runID)), Branch: "test/pass1/a1", BaseSHA: baseSHA, HeadSHA: baseSHA},
			{Agent: "a2", Pass: 1, Path: filepath.Join(worktreeBase, fmt.Sprintf("%s-pass1-a2", runID)), Branch: "test/pass1/a2", BaseSHA: baseSHA, HeadSHA: baseSHA},
			{Agent: "a3", Pass: 1, Path: filepath.Join(worktreeBase, fmt.Sprintf("%s-pass1-a3", runID)), Branch: "test/pass1/a3", BaseSHA: baseSHA, HeadSHA: baseSHA},
			{Agent: "a1", Pass: 2, Path: filepath.Join(worktreeBase, fmt.Sprintf("%s-pass2-a1", runID)), Branch: "test/pass2/a1", BaseSHA: baseSHA, HeadSHA: baseSHA},
			{Agent: "a2", Pass: 2, Path: filepath.Join(worktreeBase, fmt.Sprintf("%s-pass2-a2", runID)), Branch: "test/pass2/a2", BaseSHA: baseSHA, HeadSHA: baseSHA},
			{Agent: "a3", Pass: 2, Path: filepath.Join(worktreeBase, fmt.Sprintf("%s-pass2-a3", runID)), Branch: "test/pass2/a3", BaseSHA: baseSHA, HeadSHA: baseSHA},
		},
	}

	runIO, err := internalio.RunContextFromTaskPackage(taskPackage, runsBase, worktreeBase)
	require.NoError(t, err)
	writeCandidatesFile(t, runIO, nil)

	scriptPath := testScriptPath(t, script)
	cfg := &config.Config{
		Repo: config.RepoConfig{
			Root:          repoDir,
			DefaultBranch: "main",
			BestBranch:    "best/main",
		},
		RunsBasePath:     runsBase,
		WorktreeBasePath: worktreeBase,
		ClaudeCLIPath:    scriptPath,
		StepTimeouts: map[string]int{
			"step50": timeoutSeconds,
		},
	}
	run := RunContext{
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
		repoDir:      repoDir,
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

func writeDetachedSleepHelper(t *testing.T, dir string) string {
	t.Helper()
	sourcePath := filepath.Join(dir, "detached_sleep_helper.go")
	binaryPath := filepath.Join(dir, "detached-sleep-helper")
	source := `package main

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) < 3 {
		os.Exit(2)
	}
	if os.Getenv("DETACHED_SLEEP_CHILD") == "1" {
		if err := os.WriteFile(os.Args[1], []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
			os.Exit(1)
		}
		time.Sleep(60 * time.Second)
		return
	}

	delay, err := time.ParseDuration(os.Args[2])
	if err != nil {
		os.Exit(2)
	}
	cmd := exec.Command(os.Args[0], os.Args[1], os.Args[2])
	cmd.Env = append(os.Environ(), "DETACHED_SLEEP_CHILD=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		os.Exit(1)
	}
	time.Sleep(delay)
}
`
	require.NoError(t, os.WriteFile(sourcePath, []byte(source), 0o644))

	cmd := exec.Command("go", "build", "-o", binaryPath, sourcePath)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
	return binaryPath
}

func writeContainsFakeGitWrapper(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, "git")
	script := `#!/bin/bash
set -euo pipefail
joined="$*"
if [[ -n "${FAKE_GIT_SLEEP_ON_SUBSTRING:-}" && "$joined" == *"${FAKE_GIT_SLEEP_ON_SUBSTRING}"* ]]; then
  sleep "${FAKE_GIT_SLEEP_SECONDS:-5}"
fi
exec "$REAL_GIT" "$@"
`
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
}

func useFakeGitWrapper(t *testing.T, wrapperPath string) {
	t.Helper()
	oldCommandContext := trustedGitCommandContext
	trustedGitCommandContext = func(ctx context.Context, name string, args ...string) (*exec.Cmd, error) {
		if name == "git" {
			return exec.CommandContext(ctx, wrapperPath, args...), nil
		}
		return oldCommandContext(ctx, name, args...)
	}
	t.Cleanup(func() {
		trustedGitCommandContext = oldCommandContext
	})
}

func useFakeStreamGitOutputWithLimit(t *testing.T, wrapperPath string) {
	t.Helper()
	oldStream := streamGitOutputWithLimit
	streamGitOutputWithLimit = func(ctx context.Context, worktreePath, errPrefix, destPath string, limit int64, args ...string) (int64, error) {
		cmd := exec.CommandContext(ctx, wrapperPath, append([]string{"-C", worktreePath}, args...)...)
		cmd.Env = os.Environ()
		out, err := cmd.Output()
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		if err != nil {
			return 0, err
		}
		if int64(len(out)) > limit {
			return int64(len(out)), fmt.Errorf("%w: git %s bytes=%d limit=%d", agentrunner.ErrRescueDiffOverLimit, strings.Join(args, " "), len(out), limit)
		}
		require.NoError(t, os.MkdirAll(filepath.Dir(destPath), 0o755))
		require.NoError(t, os.WriteFile(destPath, out, 0o644))
		return int64(len(out)), nil
	}
	t.Cleanup(func() {
		streamGitOutputWithLimit = oldStream
	})
}

func processDead(pid int) bool {
	if pid <= 0 {
		return true
	}
	err := syscall.Kill(pid, 0)
	return errors.Is(err, syscall.ESRCH)
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

func writeCandidatesFile(t *testing.T, runIO internalio.RunContext, candidates []contracts.Candidate) {
	t.Helper()
	candidatesPath, err := runIO.ResolveRunRelative(filepath.Join("40", "candidates.json"))
	require.NoError(t, err)
	writeCandidatesFileAtPath(t, candidatesPath, runIO.RunID, candidates)
}

func writeCandidatesFileAtPath(t *testing.T, candidatesPath string, runID contracts.RunID, candidates []contracts.Candidate) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(candidatesPath), 0o755))
	doc := contracts.Candidates{
		SchemaVersion:  "1",
		RunID:          runID,
		Candidates:     candidates,
		CandidatesHash: contracts.CanonicalCandidatesHash(candidates),
		CreatedAt:      time.Now().UTC(),
	}
	require.NoError(t, internalio.WriteJSONAtomic(candidatesPath, doc))
}

func writeCandidateSidecar(t *testing.T, runIO internalio.RunContext, candidate contracts.Candidate, body string) contracts.Candidate {
	t.Helper()
	path, err := runIO.ResolveRunRelative(candidate.ProposedBodyPath)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	candidate.ProposedBodySha256 = sha256Hex([]byte(body))
	return candidate
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
