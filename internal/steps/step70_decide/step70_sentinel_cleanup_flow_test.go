package step70_decide

import (
	"context"
	"errors"
	"fmt"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRun_SentinelBlocksExecution(t *testing.T) {
	runCtx, pkg, candidates, store, _ := newFixture(t, "PR3")
	require.NoError(t, os.MkdirAll(filepath.Join(runCtx.RunsBase, "needs-recovery"), 0o755))
	blockPath := filepath.Join(runCtx.RunsBase, "needs-recovery", "other-run.json")
	require.NoError(t, os.WriteFile(blockPath, []byte("{}"), 0o644))

	require.ErrorIs(t, Run(context.Background(), 3, runCtx, pkg, candidates, store, Deps{Now: fixedNow()}), ErrBlockedBySentinel)

	// No decision written.
	decisionPath, err := runCtx.ResolveRunRelative("70/decision.json")
	require.NoError(t, err)
	assert.NoFileExists(t, decisionPath)
}
func TestRun_SunsetMarkerBlocksExecution(t *testing.T) {
	runCtx, pkg, candidates, store, _ := newFixture(t, "PR303")
	require.NoError(t, os.WriteFile(filepath.Join(runCtx.RunsBase, sunsetMarkerFile), []byte("{}"), 0o644))

	require.ErrorIs(t, Run(context.Background(), 3, runCtx, pkg, candidates, store, Deps{Now: fixedNow()}), ErrBlockedBySentinel)

	decisionPath, err := runCtx.ResolveRunRelative("70/decision.json")
	require.NoError(t, err)
	assert.NoFileExists(t, decisionPath)
}
func TestRun_DivergedSunsetMarkerBlocksExecution(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR304")
	require.NoError(t, os.WriteFile(filepath.Join(runCtx.RunsBase, sunsetMarkerFile+".diverged"), []byte("{}"), 0o644))

	err := Run(context.Background(), 304, runCtx, pkg, candidates, store, Deps{
		Git:      &fakeGit{head: resolver.target.BestShaBefore},
		Resolver: resolver,
		Now:      fixedNow(),
	})
	require.ErrorIs(t, err, ErrBlockedBySentinel)
	assert.Contains(t, err.Error(), sunsetMarkerFile+".diverged")
}
func TestFlockContention_ChildCannotAcquire(t *testing.T) {
	runCtx, _, _, _, _ := newFixture(t, "PR8")
	lock, err := internalio.AcquirePromotionLock(runCtx)
	require.NoError(t, err)
	defer lock.Unlock()

	cmd := exec.Command(os.Args[0], "-test.run=TestTryNonBlockingFlockHelper")
	cmd.Env = append(os.Environ(),
		"GO_WANT_FLOCK_HELPER=1",
		"FLOCK_PATH="+runCtx.PromotionLockPath(),
	)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err)
	assert.Equal(t, "wouldblock\n", string(out))

	require.NoError(t, lock.Unlock())

	cmd = exec.Command(os.Args[0], "-test.run=TestTryNonBlockingFlockHelper")
	cmd.Env = append(os.Environ(),
		"GO_WANT_FLOCK_HELPER=1",
		"FLOCK_PATH="+runCtx.PromotionLockPath(),
	)
	out, err = cmd.CombinedOutput()
	require.NoError(t, err)
	assert.Equal(t, "ok\n", string(out))
}
func TestTryNonBlockingFlockHelper(t *testing.T) {
	if os.Getenv("GO_WANT_FLOCK_HELPER") != "1" {
		return
	}
	path := os.Getenv("FLOCK_PATH")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		fmt.Fprint(os.Stdout, err)
		os.Exit(1)
	}
	defer f.Close()
	if err := tryNonBlockingFlock(int(f.Fd())); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			fmt.Fprintln(os.Stdout, "wouldblock")
			os.Exit(0)
		}
		fmt.Fprint(os.Stdout, err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stdout, "ok")
	os.Exit(0)
}
func TestRun_ResumeFromBranchPushed_RollsBackWhenSentinelAppearsBeforeResumeStep(t *testing.T) {
	runCtx, pkg, candidates, _, resolver := newFixtureWithResolver(t, "PR422")
	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	intention.Stage = contracts.IntentionStageBranchPushed

	store := newLoadHookStore(intentionPath(t, runCtx), func() {
		require.NoError(t, writeSentinel(runCtx.RunsBase, "2026-04-21-PR99-feedbee", 99, contracts.RollbackReasonTransactionalFailure, contracts.FailedStep70, fixedNow()()))
	})
	require.NoError(t, store.Save(intention))

	git := &fakeGit{head: resolver.target.TargetSHA}
	require.NoError(t, Run(context.Background(), 422, runCtx, pkg, candidates, store, Deps{Git: git, Resolver: unexpectedResolver{t: t}, Now: fixedNow()}))

	rollback := mustDecisionRollback(t, readDecision(t, runCtx))
	assert.Equal(t, contracts.RollbackReasonTransactionalFailure, rollback.RollbackReason)
	assert.Len(t, git.pushCalls, 1)
}
func TestRun_AbortsBeforePushWhenOtherRunSentinelAppears(t *testing.T) {
	runCtx, pkg, candidates, _, resolver := newFixtureWithResolver(t, "PR16")
	store := newHookStore(intentionPath(t, runCtx), func(record contracts.IntentionRecord) {
		if record.Stage == contracts.IntentionStagePlanning {
			require.NoError(t, writeSentinel(runCtx.RunsBase, "2026-04-21-PR99-deadbee", 99, contracts.RollbackReasonTransactionalFailure, contracts.FailedStep70, fixedNow()()))
		}
	})

	git := &fakeGit{head: resolver.target.BestShaBefore}
	err := Run(context.Background(), 16, runCtx, pkg, candidates, store, Deps{Git: git, Resolver: resolver, Now: fixedNow()})
	require.ErrorIs(t, err, ErrBlockedBySentinel)
	assert.Empty(t, git.pushCalls)
	require.FileExists(t, intentionPath(t, runCtx))
	intention, loadErr := store.Load()
	require.NoError(t, loadErr)
	require.NotNil(t, intention)
	assert.Equal(t, contracts.IntentionStagePlanning, intention.Stage)

	decisionPath, pathErr := runCtx.ResolveRunRelative("70/decision.json")
	require.NoError(t, pathErr)
	assert.NoFileExists(t, decisionPath)
}
func TestRun_IgnoresSelfOwnedSentinelAtStageBoundary(t *testing.T) {
	runCtx, pkg, candidates, _, resolver := newFixtureWithResolver(t, "PR17")
	store := newHookStore(intentionPath(t, runCtx), func(record contracts.IntentionRecord) {
		if record.Stage == contracts.IntentionStagePlanning {
			require.NoError(t, writeSentinel(runCtx.RunsBase, runCtx.RunID, 17, contracts.RollbackReasonTransactionalFailure, contracts.FailedStep70, fixedNow()()))
		}
	})

	git := &fakeGit{head: resolver.target.BestShaBefore}
	require.NoError(t, Run(context.Background(), 17, runCtx, pkg, candidates, store, Deps{Git: git, Resolver: resolver, Now: fixedNow()}))
	assert.Equal(t, contracts.DecisionActionAdopt, readDecision(t, runCtx).Action)
	require.Len(t, git.pushCalls, 1)
}
func TestRun_RollsBackWhenOtherRunSentinelAppearsAfterPush(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR18")
	git := &fakeGit{
		head: resolver.target.TargetSHA,
		onPush: func(call fakePushCall) {
			if call.target == resolver.target.TargetSHA {
				require.NoError(t, writeSentinel(runCtx.RunsBase, "2026-04-21-PR98-cafef00", 98, contracts.RollbackReasonTransactionalFailure, contracts.FailedStep70, fixedNow()()))
			}
		},
	}

	require.NoError(t, Run(context.Background(), 18, runCtx, pkg, candidates, store, Deps{Git: git, Resolver: resolver, Now: fixedNow()}))

	rollback := mustDecisionRollback(t, readDecision(t, runCtx))
	assert.Equal(t, contracts.RollbackReasonTransactionalFailure, rollback.RollbackReason)
	require.Len(t, git.pushCalls, 2)
}
func TestRun_ResumeFromBranchPushed_RollsBackWhenOtherRunSentinelAppearsBeforeResumeRegistry(t *testing.T) {
	runCtx, pkg, candidates, _, resolver := newFixtureWithResolver(t, "PR705")
	store := newLoadHookStore(intentionPath(t, runCtx), func() {
		require.NoError(t, writeSentinel(runCtx.RunsBase, "2026-04-21-PR99-feedbee", 99, contracts.RollbackReasonTransactionalFailure, contracts.FailedStep70, fixedNow()()))
	})
	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	intention.Stage = contracts.IntentionStageBranchPushed
	require.NoError(t, store.Save(intention))

	require.NoError(t, Run(context.Background(), 705, runCtx, pkg, candidates, store, Deps{
		Git:      &fakeGit{head: resolver.target.TargetSHA},
		Resolver: unexpectedResolver{t: t},
		Now:      fixedNow(),
	}))

	rollback := mustDecisionRollback(t, readDecision(t, runCtx))
	assert.Equal(t, contracts.RollbackReasonTransactionalFailure, rollback.RollbackReason)
}
func TestRun_RollsBackWhenOtherRunSentinelAppearsAfterRegistryAppend(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR19")
	original := appendRegistryEntry
	appendRegistryEntry = func(path string, entry contracts.RuleRegistryEntry) (contracts.RegistryAppendResult, error) {
		result, err := original(path, entry)
		if err == nil && path == runCtx.RulesRegistryPath() {
			switch entry.Kind {
			case contracts.RegistryKindAdded, contracts.RegistryKindUpdated:
				require.NoError(t, writeSentinel(runCtx.RunsBase, "2026-04-21-PR97-feedbee", 97, contracts.RollbackReasonTransactionalFailure, contracts.FailedStep70, fixedNow()()))
			}
		}
		return result, err
	}
	t.Cleanup(func() {
		appendRegistryEntry = original
	})

	git := &fakeGit{head: resolver.target.TargetSHA}
	require.NoError(t, Run(context.Background(), 19, runCtx, pkg, candidates, store, Deps{Git: git, Resolver: resolver, Now: fixedNow()}))

	rollback := mustDecisionRollback(t, readDecision(t, runCtx))
	assert.Equal(t, contracts.RollbackReasonTransactionalFailure, rollback.RollbackReason)
	lines, err := readRegistryLines(runCtx.RulesRegistryPath())
	require.NoError(t, err)
	require.Len(t, lines, 2)
}
func TestRun_RollsBackBeforeSecondRegistryAppendWhenSentinelAppearsMidLoop(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR191")
	resolver.target.RulesToAppend = adoptAddedEntriesWithTarget(runCtx.RunID, candidates.CandidatesHash, resolver.target.TargetSHA, "rule-a", "rule-b")
	stageFixtureRuleSidecars(t, runCtx, resolver.target)

	original := appendRegistryEntry
	appendCount := 0
	appendRegistryEntry = func(path string, entry contracts.RuleRegistryEntry) (contracts.RegistryAppendResult, error) {
		result, err := original(path, entry)
		if err == nil && path == runCtx.RulesRegistryPath() && entry.Kind == contracts.RegistryKindAdded {
			appendCount++
			if appendCount == 1 {
				require.NoError(t, writeSentinel(runCtx.RunsBase, "2026-04-21-PR97-feedbee", 97, contracts.RollbackReasonTransactionalFailure, contracts.FailedStep70, fixedNow()()))
			}
		}
		return result, err
	}
	t.Cleanup(func() {
		appendRegistryEntry = original
	})

	git := &fakeGit{head: resolver.target.TargetSHA}
	require.NoError(t, Run(context.Background(), 19, runCtx, pkg, candidates, store, Deps{Git: git, Resolver: resolver, Now: fixedNow()}))

	lines, err := readRegistryLines(runCtx.RulesRegistryPath())
	require.NoError(t, err)
	require.Len(t, lines, 2)
	assert.Equal(t, contracts.RegistryKindAdded, lines[0].Entry.Kind)
	assert.Equal(t, contracts.RegistryKindRolledBack, lines[1].Entry.Kind)
}
func TestCleanupWorktrees_RejectsPathOutsideWorktreeBase(t *testing.T) {
	runCtx, err := internalio.NewRunContext("2026-04-21-PR999-abcdef0", realTempDir(t), realTempDir(t))
	require.NoError(t, err)
	pkg := &contracts.TaskPackage{
		SchemaVersion:           "1",
		RunID:                   runCtx.RunID,
		PR:                      999,
		Title:                   "cleanup guard",
		BaseSHA:                 strings.Repeat("a", 40),
		BestBranch:              "best",
		ReconstructedTaskPrompt: "cleanup guard",
		CreatedAt:               time.Now().UTC(),
		Worktrees: []contracts.WorktreeAllocation{
			{Agent: "a1", Pass: 1, Path: filepath.Join(realTempDir(t), "escape"), Branch: "stub/pass1/a1", BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("a", 40)},
			{Agent: "a2", Pass: 1, Path: filepath.Join(runCtx.WorktreeBase, "pass1-a2"), Branch: "stub/pass1/a2", BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("a", 40)},
			{Agent: "a3", Pass: 1, Path: filepath.Join(runCtx.WorktreeBase, "pass1-a3"), Branch: "stub/pass1/a3", BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("a", 40)},
			{Agent: "a1", Pass: 2, Path: filepath.Join(runCtx.WorktreeBase, "pass2-a1"), Branch: "stub/pass2/a1", BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("a", 40)},
			{Agent: "a2", Pass: 2, Path: filepath.Join(runCtx.WorktreeBase, "pass2-a2"), Branch: "stub/pass2/a2", BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("a", 40)},
			{Agent: "a3", Pass: 2, Path: filepath.Join(runCtx.WorktreeBase, "pass2-a3"), Branch: "stub/pass2/a3", BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("a", 40)},
		},
	}

	err = cleanupWorktrees(context.Background(), runCtx, pkg, NoopGitOps{})
	require.Error(t, err)
	assert.ErrorIs(t, err, internalio.ErrWorktreePathEscapesBase)
}
func TestCleanupWorktrees_UnregisteredMissingPathIsNoop(t *testing.T) {
	runCtx, pkg, _, _, _ := newFixture(t, "PR203")
	git := &fakeGit{removeWorktreeErr: ErrWorktreeUnregistered}

	require.NoError(t, cleanupWorktrees(context.Background(), runCtx, pkg, git))

	require.Len(t, git.removeWorktreeCalls, len(pkg.Worktrees))
	for _, wt := range pkg.Worktrees {
		assert.NoFileExists(t, wt.Path)
	}
}
func TestCleanupWorktrees_UnregisteredExistingPathRequiresVerification(t *testing.T) {
	runCtx, pkg, _, _, _ := newFixture(t, "PR204")
	for _, wt := range pkg.Worktrees {
		require.NoError(t, os.MkdirAll(wt.Path, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(wt.Path, "leftover.txt"), []byte("leftover\n"), 0o644))
	}
	git := &fakeGit{removeWorktreeErr: ErrWorktreeUnregistered}

	err := cleanupWorktrees(context.Background(), runCtx, pkg, git)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrWorktreeUnregistered)
	require.Len(t, git.removeWorktreeCalls, 1)
	for _, wt := range pkg.Worktrees {
		assert.DirExists(t, wt.Path)
	}
}

// ---- helpers ----
