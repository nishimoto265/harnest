package step50_implement

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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

func TestStepRunTerminalVariants(t *testing.T) {
	t.Setenv("FAKE_RUN_ID", "2026-04-21-PR42-abcdef0")
	t.Setenv("FAKE_AGENT", "a1")

	tests := []struct {
		name           string
		script         string
		timeoutSeconds int
		wantKind       contracts.ManifestKind
		wantReason     string
		wantExitCode   int
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
			name:           "signal",
			script:         "fake-claude-signal.sh",
			timeoutSeconds: 30,
			wantKind:       contracts.ManifestKindError,
			wantReason:     "signal",
			wantExitCode:   143,
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
				if tt.wantExitCode != 0 {
					assert.Equal(t, tt.wantExitCode, errorVariant.ExitCode)
				}
				if tt.wantReason == "rate_limit" {
					assert.Contains(t, errorVariant.Detail, "rate_limit")
				}
			case contracts.ManifestKindTimeout:
				timeoutVariant, ok := manifest.Value.(contracts.ManifestTimeout)
				require.True(t, ok)
				assert.Equal(t, tt.timeoutSeconds, timeoutVariant.TimeoutSeconds)
			}

			assertArtifactPresence(t, env.run.IO.RunDir(), tt.wantArtifacts)
		})
	}
}

func TestStepRun_PersistsChildPIDAndPGIDInResumeState(t *testing.T) {
	env := newStepTestEnv(t, "fake-claude-timeout.sh", 30)
	t.Setenv("FAKE_SLEEP_SECONDS", "1")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- (Step{}).Run(ctx, env.run)
	}()

	agentDir, err := agentDir(env.run.IO, 2, "a1")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		state, ok, err := loadResumeState(agentDir)
		if err != nil || !ok {
			return false
		}
		return state.Pid > 0 && state.Pgid > 0
	}, time.Second, 10*time.Millisecond)

	state, ok, err := loadResumeState(agentDir)
	require.NoError(t, err)
	require.True(t, ok)
	assert.NotEqual(t, os.Getpid(), state.Pid)
	assert.NotZero(t, state.Pgid)

	cancel()
	require.ErrorIs(t, <-errCh, context.Canceled)
}

func TestResumeIfNeeded_RequiresDeadPIDAsWellAsStaleHeartbeat(t *testing.T) {
	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	agentDir, err := agentDir(env.run.IO, 2, "a1")
	require.NoError(t, err)

	oldTime := time.Now().Add(-2 * time.Hour).UTC()
	require.NoError(t, os.MkdirAll(agentDir, 0o755))
	require.NoError(t, saveResumeState(agentDir, resumeState{
		ExpectedBaseSHA: env.run.TaskPackage.BaseSHA,
		StartedAt:       oldTime,
		Pid:             os.Getpid(),
		Pgid:            os.Getpid(),
		RetryCount:      1,
		LastHeartbeat:   oldTime,
	}))
	require.NoError(t, touchHeartbeat(agentDir, oldTime))

	allocation, err := worktreeFor(env.run.TaskPackage, 2, "a1")
	require.NoError(t, err)
	step := newStep(env.run.Config, stepOptions{
		now:        func() time.Time { return oldTime.Add(3 * time.Hour) },
		staleAfter: time.Second,
	})

	_, err = step.resumeIfNeeded(context.Background(), env.run, allocation, agentDir)
	require.ErrorIs(t, err, ErrRescueAbortedLeaseActive)
}

func TestStepRun_ZeroValuePreservesCustomStaleAfter(t *testing.T) {
	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	agentDir, err := agentDir(env.run.IO, 2, "a1")
	require.NoError(t, err)

	oldTime := time.Now().Add(-2 * time.Second).UTC()
	require.NoError(t, os.MkdirAll(agentDir, 0o755))
	require.NoError(t, saveResumeState(agentDir, resumeState{
		ExpectedBaseSHA: env.run.TaskPackage.BaseSHA,
		StartedAt:       oldTime,
		Pid:             os.Getpid(),
		Pgid:            os.Getpid(),
		RetryCount:      1,
		LastHeartbeat:   oldTime,
	}))
	require.NoError(t, touchHeartbeat(agentDir, oldTime))

	err = (Step{
		cfg:               env.run.Config,
		heartbeatInterval: 10 * time.Millisecond,
		staleAfter:        time.Second,
	}).Run(context.Background(), env.run)
	require.ErrorIs(t, err, ErrRescueAbortedLeaseActive)
}

func TestStep50GitHelpers_ReturnContextCancellation(t *testing.T) {
	wrapperDir := t.TempDir()
	wrapperPath := filepath.Join(wrapperDir, "git")
	wrapper := "#!/bin/sh\nsleep 5\nexit 1\n"
	require.NoError(t, os.WriteFile(wrapperPath, []byte(wrapper), 0o755))
	t.Setenv("PATH", wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	_, err := gitOutputBytesContext(ctx, t.TempDir(), "rev-list", "HEAD")
	require.ErrorIs(t, err, context.Canceled)

	ctx, cancel = context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	err = runGitCommand(ctx, t.TempDir(), "rev-list", "HEAD")
	require.ErrorIs(t, err, context.Canceled)
}

func TestStepRunIncludesRulePayloadsInPrompt(t *testing.T) {
	t.Setenv("FAKE_RUN_ID", "2026-04-21-PR42-abcdef0")
	t.Setenv("FAKE_AGENT", "a1")

	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	promptCapturePath := filepath.Join(t.TempDir(), "prompt.txt")
	t.Setenv("PROMPT_CAPTURE_FILE", promptCapturePath)

	const proposedBody = "# cand-1\nUse the candidate sidecar, not runsBase/rules.\n"
	rulesDir := filepath.Join(env.run.IO.RunsBase, "rules")
	require.NoError(t, os.MkdirAll(rulesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(rulesDir, "rule-abc.md"), []byte("stale registry body\n"), 0o644))

	candidate := writeCandidateSidecar(t, env.run.IO, contracts.Candidate{
		CandidateID:      "cand-1",
		Kind:             contracts.CandidateKindNew,
		Title:            "Add a new implementation rule",
		ProposedBodyPath: "40/candidates/cand-1.md",
	}, proposedBody)
	writeCandidatesFile(t, env.run.IO, []contracts.Candidate{candidate})

	err := (Step{}).Run(context.Background(), env.run)
	require.NoError(t, err)

	promptBytes, err := os.ReadFile(promptCapturePath)
	require.NoError(t, err)
	assert.Contains(t, string(promptBytes), "cand-1")
	assert.Contains(t, string(promptBytes), "kind: new")
	assert.Contains(t, string(promptBytes), "target_rule_id: (none)")
	assert.Contains(t, string(promptBytes), "Add a new implementation rule")
	assert.Contains(t, string(promptBytes), proposedBody)
	assert.NotContains(t, string(promptBytes), "stale registry body")
}

func TestStepRunParentCancelDoesNotWriteManifest(t *testing.T) {
	t.Setenv("FAKE_RUN_ID", "2026-04-21-PR42-abcdef0")
	t.Setenv("FAKE_AGENT", "a1")

	env := newStepTestEnv(t, "fake-claude-timeout.sh", 30)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	err := (Step{}).Run(ctx, env.run)
	require.ErrorIs(t, err, context.Canceled)

	_, statErr := os.Stat(env.manifestPath)
	require.Error(t, statErr)
	assert.True(t, os.IsNotExist(statErr))
	assertArtifactPresence(t, env.run.IO.RunDir(), false)
}

func TestStepRunMissingChecklistFailsClosed(t *testing.T) {
	t.Setenv("FAKE_RUN_ID", "2026-04-21-PR42-abcdef0")
	t.Setenv("FAKE_AGENT", "a1")
	t.Setenv("FAKE_SKIP_CHECKLIST", "1")

	env := newStepTestEnv(t, "fake-claude-success.sh", 30)

	err := (Step{}).Run(context.Background(), env.run)
	require.ErrorContains(t, err, "missing checklist artifact")
}

func TestStepRunSuccessDiffCapturesUntrackedFilesButSkipsChecklistArtifact(t *testing.T) {
	t.Setenv("FAKE_RUN_ID", "2026-04-21-PR42-abcdef0")
	t.Setenv("FAKE_AGENT", "a1")

	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	worktree := env.run.TaskPackage.Worktrees[3].Path
	require.NoError(t, os.WriteFile(filepath.Join(worktree, "notes.txt"), []byte("draft\n"), 0o644))

	err := (Step{}).Run(context.Background(), env.run)
	require.NoError(t, err)

	manifest := readManifest(t, env.manifestPath)
	success, ok := manifest.Value.(contracts.ManifestSuccess)
	require.True(t, ok)

	diffBytes, readErr := os.ReadFile(filepath.Join(env.run.IO.RunDir(), success.DiffPath))
	require.NoError(t, readErr)
	assert.Contains(t, string(diffBytes), "notes.txt")
	assert.NotContains(t, string(diffBytes), "checklist-result.json")
}

func TestStepRunRemovesStaleArtifactsOnNonSuccess(t *testing.T) {
	t.Setenv("FAKE_RUN_ID", "2026-04-21-PR42-abcdef0")
	t.Setenv("FAKE_AGENT", "a1")

	env := newStepTestEnv(t, "fake-claude-error.sh", 30)
	for _, rel := range []string{
		filepath.Join("50-pass2", "a1", "diff.patch"),
		filepath.Join("50-pass2", "a1", "session.jsonl"),
		filepath.Join("50-pass2", "a1", "checklist-result.json"),
	} {
		abs := filepath.Join(env.run.IO.RunDir(), rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0o755))
		require.NoError(t, os.WriteFile(abs, []byte("stale\n"), 0o644))
	}

	err := (Step{}).Run(context.Background(), env.run)
	require.NoError(t, err)

	manifest := readManifest(t, env.manifestPath)
	assert.Equal(t, contracts.ManifestKindError, manifest.Kind)
	diffPath := filepath.Join(env.run.IO.RunDir(), "50-pass2", "a1", "diff.patch")
	checklistPath := filepath.Join(env.run.IO.RunDir(), "50-pass2", "a1", "checklist-result.json")
	assert.FileExists(t, diffPath)
	assert.FileExists(t, checklistPath)
}

func TestLoadRulePayloadsRejectsPathTraversal(t *testing.T) {
	t.Setenv("FAKE_RUN_ID", "2026-04-21-PR42-abcdef0")
	t.Setenv("FAKE_AGENT", "a1")

	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	candidatesPath, err := env.run.IO.ResolveRunRelative(filepath.Join("40", "candidates.json"))
	require.NoError(t, err)

	bodyPath, err := env.run.IO.ResolveRunRelative(filepath.Join("40", "candidates", "good.md"))
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(bodyPath), 0o755))
	require.NoError(t, os.WriteFile(bodyPath, []byte("body\n"), 0o644))
	bodySHA := sha256Hex([]byte("body\n"))

	tests := []struct {
		name      string
		candidate contracts.Candidate
		wantErr   string
	}{
		{
			name: "candidate_id traversal",
			candidate: contracts.Candidate{
				CandidateID:        "../cand",
				Kind:               contracts.CandidateKindNew,
				Title:              "Bad candidate id",
				ProposedBodyPath:   "40/candidates/good.md",
				ProposedBodySha256: bodySHA,
			},
			wantErr: `invalid candidate_id "../cand"`,
		},
		{
			name: "target_rule_id traversal",
			candidate: contracts.Candidate{
				CandidateID:        "cand-1",
				Kind:               contracts.CandidateKindUpdate,
				TargetRuleID:       "../rule",
				Title:              "Bad target rule id",
				ProposedBodyPath:   "40/candidates/good.md",
				ProposedBodySha256: bodySHA,
			},
			wantErr: `invalid rule_id`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "target_rule_id traversal" {
				require.NoError(t, os.MkdirAll(filepath.Dir(candidatesPath), 0o755))
				require.NoError(t, os.WriteFile(candidatesPath, []byte(`{"schema_version":"1","run_id":"`+string(env.run.IO.RunID)+`","candidates":[{"candidate_id":"cand-1","kind":"update","target_rule_id":"../rule","title":"Bad target rule id","proposed_body_path":"40/candidates/good.md","proposed_body_sha256":"`+bodySHA+`"}],"candidates_hash":"`+contracts.CanonicalCandidatesHash([]contracts.Candidate{{
					CandidateID:        "cand-1",
					Kind:               contracts.CandidateKindUpdate,
					TargetRuleID:       "../rule",
					Title:              "Bad target rule id",
					ProposedBodyPath:   "40/candidates/good.md",
					ProposedBodySha256: bodySHA,
				}})+`","created_at":"2026-04-21T00:00:00Z"}`), 0o644))
			} else {
				writeCandidatesFileAtPath(t, candidatesPath, env.run.IO.RunID, []contracts.Candidate{tt.candidate})
			}
			_, err := LoadRulePayloads(candidatesPath)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestLoadRulePayloads_SkipsDuplicateCandidates(t *testing.T) {
	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	candidateNew := writeCandidateSidecar(t, env.run.IO, contracts.Candidate{
		CandidateID:      "cand-new",
		Kind:             contracts.CandidateKindNew,
		Title:            "New rule",
		ProposedBodyPath: "40/candidates/cand-new.md",
	}, "# cand-new\nnew body\n")
	candidateDuplicate := writeCandidateSidecar(t, env.run.IO, contracts.Candidate{
		CandidateID:      "cand-dup",
		Kind:             contracts.CandidateKindDuplicate,
		TargetRuleID:     "rule-v1",
		Title:            "Duplicate rule",
		ProposedBodyPath: "40/candidates/cand-dup.md",
	}, "# cand-dup\nduplicate body\n")
	writeCandidatesFile(t, env.run.IO, []contracts.Candidate{candidateNew, candidateDuplicate})

	candidatesPath, err := env.run.IO.ResolveRunRelative(filepath.Join("40", "candidates.json"))
	require.NoError(t, err)
	payloads, err := LoadRulePayloads(candidatesPath)
	require.NoError(t, err)
	require.Len(t, payloads, 1)
	assert.Equal(t, "cand-new", payloads[0].ID)
}

func TestLoadRulePayloads_AllowsValidatedRuleID(t *testing.T) {
	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	candidate := writeCandidateSidecar(t, env.run.IO, contracts.Candidate{
		CandidateID:      "cand-1",
		Kind:             contracts.CandidateKindUpdate,
		TargetRuleID:     "rule-v1",
		Title:            "Updated rule",
		ProposedBodyPath: "40/candidates/cand-1.md",
	}, "# cand-1\nupdated body\n")
	writeCandidatesFile(t, env.run.IO, []contracts.Candidate{candidate})

	candidatesPath, err := env.run.IO.ResolveRunRelative(filepath.Join("40", "candidates.json"))
	require.NoError(t, err)
	payloads, err := LoadRulePayloads(candidatesPath)
	require.NoError(t, err)
	require.Len(t, payloads, 1)
	assert.Equal(t, "rule-v1", payloads[0].TargetRuleID)
}

func TestCopyUntrackedFiles_SkipsSymlinksAndKeepsWhitespaceNames(t *testing.T) {
	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	worktree := env.run.TaskPackage.Worktrees[3].Path
	secretPath := filepath.Join(t.TempDir(), "id_rsa")
	require.NoError(t, os.WriteFile(secretPath, []byte("secret\n"), 0o600))
	require.NoError(t, os.Symlink(secretPath, filepath.Join(worktree, "loot")))
	require.NoError(t, os.WriteFile(filepath.Join(worktree, "space name.txt"), []byte("hello\n"), 0o644))

	rescueDir := filepath.Join(t.TempDir(), "rescue")
	require.NoError(t, os.MkdirAll(filepath.Join(rescueDir, "untracked"), 0o755))

	artifacts, err := copyUntrackedFiles(context.Background(), worktree, rescueDir)
	require.NoError(t, err)
	assert.NoFileExists(t, filepath.Join(rescueDir, "untracked", "loot"))
	assert.FileExists(t, filepath.Join(rescueDir, "untracked", "space name.txt"))
	assert.FileExists(t, filepath.Join(rescueDir, "untracked-symlinks.txt"))
	symlinkLog, err := os.ReadFile(filepath.Join(rescueDir, "untracked-symlinks.txt"))
	require.NoError(t, err)
	assert.Contains(t, string(symlinkLog), "loot")

	paths := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		paths = append(paths, artifact.Path)
	}
	assert.Contains(t, paths, "untracked/space name.txt")
	assert.Contains(t, paths, "untracked-symlinks.txt")
}

func TestShouldWriteTimeoutManifestRequiresRunError(t *testing.T) {
	execCtx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-execCtx.Done()

	assert.False(t, shouldWriteTimeoutManifest(nil, execCtx))
	assert.True(t, shouldWriteTimeoutManifest(errors.New("run failed"), execCtx))
}

func TestWriteCommitBundle_ZeroCommitProducesEmptyBundle(t *testing.T) {
	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	rescueDir := t.TempDir()

	commitCount, bundleMode, err := writeCommitBundle(context.Background(), env.run.TaskPackage.Worktrees[3].Path, rescueDir, env.run.TaskPackage.BaseSHA)
	require.NoError(t, err)
	assert.Equal(t, 0, commitCount)
	assert.Equal(t, agentrunner.RescueBundleModeNone, bundleMode)

	bundlePath := filepath.Join(rescueDir, "commits.bundle")
	info, statErr := os.Stat(bundlePath)
	require.NoError(t, statErr)
	assert.EqualValues(t, 0, info.Size())
}

func TestStepRun_RejectsDetachedForeignHead(t *testing.T) {
	t.Setenv("FAKE_RUN_ID", "2026-04-21-PR42-abcdef0")
	t.Setenv("FAKE_AGENT", "a1")

	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	runCommand(t, env.repoDir, "git", "checkout", "main")
	require.NoError(t, os.WriteFile(filepath.Join(env.repoDir, "foreign.txt"), []byte("foreign\n"), 0o644))
	runCommand(t, env.repoDir, "git", "add", "foreign.txt")
	runCommand(t, env.repoDir, "git", "commit", "-m", "foreign commit")
	foreignSHA := strings.TrimSpace(runCommand(t, env.repoDir, "git", "rev-parse", "HEAD"))
	runCommand(t, env.repoDir, "git", "checkout", "main")

	t.Setenv("FAKE_CHECKOUT_REF_BEFORE_COMMIT", foreignSHA)
	err := (Step{}).Run(context.Background(), env.run)
	require.ErrorContains(t, err, "current branch mismatch")
}

func TestStepRun_GitCommandsIgnoreInheritedGitDir(t *testing.T) {
	t.Setenv("FAKE_RUN_ID", "2026-04-21-PR42-abcdef0")
	t.Setenv("FAKE_AGENT", "a1")

	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	otherRepo := filepath.Join(t.TempDir(), "other-repo")
	initGitRepoWithWorktree(t, otherRepo, filepath.Join(t.TempDir(), "other-pass2-a1"))
	runCommand(t, otherRepo, "git", "commit", "--allow-empty", "-m", "other-head")
	otherHead := strings.TrimSpace(runCommand(t, otherRepo, "git", "rev-parse", "HEAD"))

	t.Setenv("GIT_DIR", filepath.Join(otherRepo, ".git"))
	t.Setenv("GIT_WORK_TREE", otherRepo)

	require.NoError(t, (Step{}).Run(context.Background(), env.run))

	manifest := readManifest(t, env.manifestPath)
	success := manifest.Value.(contracts.ManifestSuccess)
	assert.Equal(t, env.run.TaskPackage.BaseSHA, success.BaseSHA)
	assert.NotEqual(t, otherHead, success.HeadSHA)
}

func TestStepRun_RescueStartFailureLeavesNoPhantomLease(t *testing.T) {
	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	agentDir, err := agentDir(env.run.IO, 2, "a1")
	require.NoError(t, err)

	require.NoError(t, os.MkdirAll(agentDir, 0o755))
	require.NoError(t, saveResumeState(agentDir, resumeState{
		ExpectedBaseSHA: env.run.TaskPackage.BaseSHA,
		StartedAt:       time.Time{},
		Pid:             0,
		Pgid:            0,
		RetryCount:      1,
		LastHeartbeat:   time.Time{},
	}))

	failing := newStep(env.run.Config, stepOptions{
		now:               time.Now,
		heartbeatInterval: 10 * time.Millisecond,
		staleAfter:        time.Second,
		runner:            failBeforeStartRunner{},
	})
	err = failing.Run(context.Background(), env.run)
	require.ErrorContains(t, err, "synthetic start failure")

	state, ok, err := loadResumeState(agentDir)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, 1, state.RetryCount)
	assert.Zero(t, state.Pid)
	assert.Zero(t, state.Pgid)

	_, statErr := os.Stat(heartbeatPath(agentDir))
	require.Error(t, statErr)
	assert.True(t, os.IsNotExist(statErr))

	require.NoError(t, (Step{}).Run(context.Background(), env.run))
}

func TestStepRun_KillsDetachedSetsidChildAfterSuccessfulExit(t *testing.T) {
	t.Setenv("FAKE_RUN_ID", "2026-04-21-PR42-abcdef0")
	t.Setenv("FAKE_AGENT", "a1")

	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	helperPath := writeDetachedSleepHelper(t, t.TempDir())
	pidPath := filepath.Join(t.TempDir(), "detached-child.pid")
	t.Setenv("FAKE_CLAUDE_WRITE_FILE", filepath.Join(env.run.TaskPackage.Worktrees[3].Path, "detached.txt"))
	t.Setenv("FAKE_DETACH_HELPER", helperPath)
	t.Setenv("FAKE_DETACHED_PID_PATH", pidPath)
	t.Setenv("FAKE_DETACH_DELAY", "250ms")

	require.NoError(t, (Step{}).Run(context.Background(), env.run))

	pidBytes, err := os.ReadFile(pidPath)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return processDead(pid)
	}, 2*time.Second, 20*time.Millisecond)
}

func TestStepRun_KillsFastDetachedSetsidChildAfterSuccessfulExit(t *testing.T) {
	if raceBuild {
		t.Skip("timing-sensitive detached-child regression is covered in non-race mode")
	}
	t.Setenv("FAKE_RUN_ID", "2026-04-21-PR42-abcdef0")
	t.Setenv("FAKE_AGENT", "a1")

	env := newStepTestEnv(t, "fake-claude-success.sh", 30)
	helperPath := writeDetachedSleepHelper(t, t.TempDir())
	pidPath := filepath.Join(t.TempDir(), "fast-detached-child.pid")
	t.Setenv("FAKE_CLAUDE_WRITE_FILE", filepath.Join(env.run.TaskPackage.Worktrees[3].Path, "fast-detached.txt"))
	t.Setenv("FAKE_DETACH_HELPER", helperPath)
	t.Setenv("FAKE_DETACHED_PID_PATH", pidPath)
	t.Setenv("FAKE_DETACH_DELAY", "20ms")

	require.NoError(t, (Step{}).Run(context.Background(), env.run))

	pidBytes, err := os.ReadFile(pidPath)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return processDead(pid)
	}, 2*time.Second, 20*time.Millisecond)
}

type failBeforeStartRunner struct{}

func (failBeforeStartRunner) Run(context.Context, runnerRequest) (runnerResult, error) {
	return runnerResult{}, errors.New("synthetic start failure")
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
	writeCandidatesFile(t, runIO, nil)

	scriptPath := testScriptPath(t, script)
	cfg := &config.Config{
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
