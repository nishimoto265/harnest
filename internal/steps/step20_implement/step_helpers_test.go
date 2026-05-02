package step20_implement

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/steps/agentrunner"
	"github.com/stretchr/testify/require"
)

const processTestEventuallyTimeout = 10 * time.Second

type testFixture struct {
	step     *Step
	run      RunContext
	cfg      *config.Config
	runIO    internalio.RunContext
	baseSHA  string
	agentDir string
	worktree string

	stateSnapshot            []byte
	heartbeatSnapshotModTime time.Time
}

func newTestFixture(t *testing.T, timeoutSec int) *testFixture {
	t.Helper()

	root := t.TempDir()
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	require.NoError(t, os.MkdirAll(runsBase, 0o755))
	require.NoError(t, os.MkdirAll(worktreeBase, 0o755))

	repoRoot := mustRepoRoot(t)
	scriptPath := writeFakeClaudeScript(t, root)

	runID := contracts.RunID("2026-04-21-PR42-abcdef0")
	runIO, err := internalio.NewRunContext(runID, runsBase, worktreeBase)
	require.NoError(t, err)

	worktree := filepath.Join(worktreeBase, "repo-a1")
	baseSHA := initGitRepo(t, worktree, "auto-improve/"+string(runID)+"/pass1/a1")
	pkg := buildTaskPackage(t, runID, worktreeBase, worktree, baseSHA)

	cfg := &config.Config{
		Repo: config.RepoConfig{
			Root:          repoRoot,
			DefaultBranch: "main",
			BestBranch:    "best",
		},
		Worktree:                  config.WorktreeConfig{Base: worktreeBase},
		Paths:                     config.PathsConfig{Runs: runsBase},
		ClaudeCLIPath:             scriptPath,
		RescueMaxRetries:          3,
		RegistryHighThreshold:     config.DefaultRegistryHighThreshold,
		RegistryCriticalThreshold: config.DefaultRegistryCriticalThreshold,
		StepTimeouts: map[string]int{
			"step20": timeoutSec,
		},
	}

	step := newStep(cfg, stepOptions{
		now:               time.Now,
		heartbeatInterval: 10 * time.Millisecond,
		staleAfter:        time.Second,
	})
	agentDir, err := agentDir(runIO, 1, "a1")
	require.NoError(t, err)
	return &testFixture{
		step: step,
		run: RunContext{
			Config:      cfg,
			Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
			PR:          42,
			Pass:        1,
			Agent:       "a1",
			IO:          runIO,
			TaskPackage: &pkg,
		},
		cfg:      cfg,
		runIO:    runIO,
		baseSHA:  baseSHA,
		agentDir: agentDir,
		worktree: worktree,
	}
}

func (fx *testFixture) manifestPath() string {
	path, err := fx.runIO.ManifestPath(1, fx.run.Agent)
	if err != nil {
		panic(err)
	}
	return path
}

func (fx *testFixture) diffPath() string {
	path, err := artifactPath(fx.runIO, 1, fx.run.Agent, diffFileName)
	if err != nil {
		panic(err)
	}
	return path
}

func (fx *testFixture) checklistPath() string {
	path, err := artifactPath(fx.runIO, 1, fx.run.Agent, checklistFileName)
	if err != nil {
		panic(err)
	}
	return path
}

func (fx *testFixture) sessionPath() string {
	path, err := artifactPath(fx.runIO, 1, fx.run.Agent, sessionFileName)
	if err != nil {
		panic(err)
	}
	return path
}

func (fx *testFixture) statePath() string {
	return resumeStatePath(fx.agentDir)
}

func latestRescueDir(t *testing.T, agentDir string) string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(agentDir, rescuedDirName))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	return filepath.Join(agentDir, rescuedDirName, entries[0].Name())
}

func stubQuiescentRescueWorktree(t *testing.T) {
	t.Helper()
	originalWorktreePIDs := rescueWorktreeProcessIDs
	rescueWorktreeProcessIDs = func(context.Context, string) ([]int, error) { return nil, nil }
	t.Cleanup(func() {
		rescueWorktreeProcessIDs = originalWorktreePIDs
	})
}

func (fx *testFixture) heartbeatLeasePath() string {
	return heartbeatPath(fx.agentDir)
}

func (fx *testFixture) readManifest(t *testing.T) contracts.Manifest {
	t.Helper()
	manifest, err := internalio.ReadJSON[contracts.Manifest](fx.manifestPath())
	require.NoError(t, err)
	return manifest
}

func (fx *testFixture) seedResumeState(t *testing.T, retryCount int) {
	t.Helper()
	oldTime := time.Now().Add(-2 * time.Hour).UTC()
	require.NoError(t, os.MkdirAll(fx.agentDir, 0o755))
	require.NoError(t, saveResumeState(fx.agentDir, resumeState{
		ExpectedBaseSHA: fx.baseSHA,
		StartedAt:       oldTime,
		Pid:             999999,
		LeaderStartTime: "stale-start",
		RetryCount:      retryCount,
		LastHeartbeat:   oldTime,
	}))
	require.NoError(t, touchHeartbeat(fx.agentDir, oldTime))
}

func (fx *testFixture) seedResumeStateWithoutHeartbeat(t *testing.T, retryCount int) {
	t.Helper()
	oldTime := time.Now().Add(-2 * time.Hour).UTC()
	require.NoError(t, os.MkdirAll(fx.agentDir, 0o755))
	require.NoError(t, saveResumeState(fx.agentDir, resumeState{
		ExpectedBaseSHA: fx.baseSHA,
		StartedAt:       oldTime,
		Pid:             999999,
		LeaderStartTime: "stale-start",
		RetryCount:      retryCount,
		LastHeartbeat:   oldTime,
	}))
}

func (fx *testFixture) seedActiveLeaseState(t *testing.T) {
	t.Helper()
	now := time.Now().UTC()
	startTime, err := lookupLeaseStartTime(os.Getpid())
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(fx.agentDir, 0o755))
	require.NoError(t, saveResumeState(fx.agentDir, resumeState{
		ExpectedBaseSHA: fx.baseSHA,
		StartedAt:       now,
		Pid:             os.Getpid(),
		LeaderStartTime: startTime,
		RetryCount:      1,
		LastHeartbeat:   now,
	}))
	require.NoError(t, touchHeartbeat(fx.agentDir, now))
	stateBytes, err := os.ReadFile(fx.statePath())
	require.NoError(t, err)
	fx.stateSnapshot = stateBytes
	info, err := os.Stat(fx.heartbeatLeasePath())
	require.NoError(t, err)
	fx.heartbeatSnapshotModTime = info.ModTime()
}

func writeFakeClaudeScript(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "fake-claude.sh")
	script := `#!/bin/bash
set -euo pipefail
if [[ "${FAKE_CLAUDE_STDOUT:-}" != "" ]]; then
  printf '%s' "${FAKE_CLAUDE_STDOUT}"
fi
if [[ "${FAKE_CLAUDE_STDERR:-}" != "" ]]; then
  printf '%s' "${FAKE_CLAUDE_STDERR}" >&2
fi
if [[ "${FAKE_CLAUDE_CHECKLIST_JSON:-}" != "" ]]; then
  printf '%s' "${FAKE_CLAUDE_CHECKLIST_JSON}" > checklist-result.json
elif [[ "${FAKE_CLAUDE_MKFIFO_CHECKLIST:-0}" == "1" ]]; then
  rm -f checklist-result.json
  mkfifo checklist-result.json
elif [[ "${FAKE_SKIP_CHECKLIST:-0}" != "1" ]]; then
  cat > checklist-result.json <<EOF
{"schema_version":"1","run_id":"${FAKE_RUN_ID:-2026-04-21-PR42-abcdef0}","pass":1,"agent":"${FAKE_AGENT:-a1}","items":[]}
EOF
fi
if [[ "${FAKE_CLAUDE_WRITE_FILE:-}" != "" ]]; then
  if [[ "${FAKE_CLAUDE_WRITE_SIZE:-0}" != "0" ]]; then
    head -c "${FAKE_CLAUDE_WRITE_SIZE}" /dev/zero | tr '\0' 'x' > "${FAKE_CLAUDE_WRITE_FILE}"
  else
    printf 'dirty worktree\n' > "${FAKE_CLAUDE_WRITE_FILE}"
  fi
fi
if [[ "${FAKE_CLAUDE_COMMIT:-}" == "1" ]]; then
  git commit --allow-empty -m test >/dev/null 2>&1
fi
if [[ "${FAKE_CLAUDE_CHECKOUT_REF_BEFORE_EXIT:-}" != "" ]]; then
  git checkout "${FAKE_CLAUDE_CHECKOUT_REF_BEFORE_EXIT}" >/dev/null 2>&1
fi
if [[ "${FAKE_CLAUDE_FORK_SESSION_WRITER:-}" == "1" ]]; then
  (
    while true; do
      printf '{"event":"child-process"}\n'
      sleep 0.05
    done
  ) &
fi
if [[ "${FAKE_CLAUDE_BACKGROUND_SENTINEL_HELPER:-}" != "" ]]; then
  "${FAKE_CLAUDE_BACKGROUND_SENTINEL_HELPER}" \
    "${FAKE_CLAUDE_BACKGROUND_SENTINEL_PATH}" \
    "${FAKE_CLAUDE_BACKGROUND_SENTINEL_DELAY:-200ms}"
fi
if [[ "${FAKE_CLAUDE_DETACH_HELPER:-}" != "" ]]; then
  "${FAKE_CLAUDE_DETACH_HELPER}" \
    "${FAKE_CLAUDE_DETACHED_PID_PATH}" \
    "${FAKE_CLAUDE_DETACH_DELAY:-200ms}"
fi
if [[ "${FAKE_CLAUDE_SLEEP_SECONDS:-0}" != "0" ]]; then
  sleep "${FAKE_CLAUDE_SLEEP_SECONDS}"
fi
exit "${FAKE_CLAUDE_EXIT_CODE:-0}"
`
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	return path
}

func writeBackgroundSentinelHelper(t *testing.T, dir string) string {
	t.Helper()
	sourcePath := filepath.Join(dir, "background_sentinel_helper.go")
	binaryPath := filepath.Join(dir, "background-sentinel-helper")
	source := `package main

import (
	"os"
	"os/exec"
	"strconv"
	"time"
)

func main() {
	if len(os.Args) < 3 {
		os.Exit(2)
	}
	if os.Getenv("BACKGROUND_SENTINEL_CHILD") == "1" {
		if err := os.WriteFile(os.Args[1]+".pid", []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
			os.Exit(1)
		}
		delay, err := time.ParseDuration(os.Args[2])
		if err != nil {
			os.Exit(2)
		}
		time.Sleep(delay)
		if err := os.WriteFile(os.Args[1], []byte("background-child\n"), 0o644); err != nil {
			os.Exit(1)
		}
		return
	}

	cmd := exec.Command(os.Args[0], os.Args[1], os.Args[2])
	cmd.Env = append(os.Environ(), "BACKGROUND_SENTINEL_CHILD=1")
	if err := cmd.Start(); err != nil {
		os.Exit(1)
	}
}
`
	require.NoError(t, os.WriteFile(sourcePath, []byte(source), 0o644))

	cmd := exec.Command("go", "build", "-o", binaryPath, sourcePath)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return binaryPath
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
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return binaryPath
}

func writeDetachedWorktreeWriterHelper(t *testing.T, dir string) string {
	t.Helper()
	sourcePath := filepath.Join(dir, "detached_worktree_writer_helper.go")
	binaryPath := filepath.Join(dir, "detached-worktree-writer-helper")
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
	if os.Getenv("DETACHED_WORKTREE_WRITER_CHILD") == "1" {
		if err := os.WriteFile(os.Args[2], []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
			os.Exit(1)
		}
		file, err := os.OpenFile(os.Args[1], os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			os.Exit(1)
		}
		defer file.Close()
		for {
			if _, err := file.WriteString("ghost\n"); err != nil {
				os.Exit(1)
			}
			file.Sync()
			time.Sleep(25 * time.Millisecond)
		}
	}

	cmd := exec.Command(os.Args[0], os.Args[1], os.Args[2])
	cmd.Env = append(os.Environ(), "DETACHED_WORKTREE_WRITER_CHILD=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		os.Exit(1)
	}
	time.Sleep(75 * time.Millisecond)
}
`
	require.NoError(t, os.WriteFile(sourcePath, []byte(source), 0o644))

	cmd := exec.Command("go", "build", "-o", binaryPath, sourcePath)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return binaryPath
}

func mustRepoRoot(t *testing.T) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return strings.TrimSpace(string(out))
}

func initGitRepo(t *testing.T, dir, branch string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.name", "Test User")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "commit", "--allow-empty", "-m", "base")
	runGit(t, dir, "checkout", "-b", branch)
	return strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
}

func buildTaskPackage(t *testing.T, runID contracts.RunID, worktreeBase, pass1Path, baseSHA string) contracts.TaskPackage {
	t.Helper()
	pkg := contracts.TaskPackage{
		SchemaVersion:           "1",
		RunID:                   runID,
		PR:                      42,
		Title:                   "test",
		BaseSHA:                 baseSHA,
		BestBranch:              "best",
		ReconstructedTaskPrompt: "Implement the requested change.",
		CreatedAt:               time.Now().UTC(),
	}
	for pass := 1; pass <= 2; pass++ {
		for _, agent := range []contracts.AgentID{"a1", "a2", "a3"} {
			path := filepath.Join(worktreeBase, fmt.Sprintf("pass%d-%s", pass, agent))
			if pass == 1 && agent == "a1" {
				path = pass1Path
			} else {
				require.NoError(t, os.MkdirAll(path, 0o755))
			}
			pkg.Worktrees = append(pkg.Worktrees, contracts.WorktreeAllocation{
				Agent:   agent,
				Pass:    pass,
				Path:    path,
				Branch:  fmt.Sprintf("auto-improve/%s/pass%d/%s", runID, pass, agent),
				BaseSHA: baseSHA,
				HeadSHA: baseSHA,
			})
		}
	}
	return pkg
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return string(out)
}

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
	if err := os.WriteFile(filepath.Join(req.Workdir, checklistFileName), []byte(`{"schema_version":"1","run_id":"`+string(r.runID)+`","pass":1,"agent":"`+string(r.agent)+`","items":[]}`), 0o644); err != nil {
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

func writeFakeGitWrapper(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, "git")
	script := `#!/bin/bash
set -euo pipefail
joined="$*"
printf '%s\n' "$joined" >> "$FAKE_GIT_LOG"
if [[ -n "${FAKE_GIT_SLEEP_ON_PREFIX:-}" && "$joined" == "${FAKE_GIT_SLEEP_ON_PREFIX}"* ]]; then
  sleep "${FAKE_GIT_SLEEP_SECONDS:-5}"
fi
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
