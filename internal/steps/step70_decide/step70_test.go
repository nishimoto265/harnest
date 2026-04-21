package step70_decide

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun_NoopWhenNoTarget(t *testing.T) {
	runCtx, pkg, candidates, store, _ := newFixture(t, "PR1")
	require.NoError(t, Run(context.Background(), 1, runCtx, pkg, candidates, store, Deps{Now: fixedNow()}))

	decision := readDecision(t, runCtx)
	assert.Equal(t, contracts.DecisionActionNoop, decision.Action)
	assert.NoFileExists(t, intentionPath(t, runCtx))
}

func TestRun_AdoptHappyPath(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR2")
	git := &fakeGit{head: resolver.target.BestShaBefore}
	deps := Deps{Git: git, Resolver: resolver, Now: fixedNow()}
	require.NoError(t, Run(context.Background(), 2, runCtx, pkg, candidates, store, deps))

	decision := readDecision(t, runCtx)
	assert.Equal(t, contracts.DecisionActionAdopt, decision.Action)

	// promoting + promoted events persisted by step70 itself.
	events := readStateEvents(t, runCtx)
	assert.Equal(t, contracts.StateKindPromoting, events[0].Kind)
	assert.Equal(t, contracts.StateKindPromoted, events[len(events)-1].Kind)

	// Intention deleted on finalize.
	assert.NoFileExists(t, intentionPath(t, runCtx))

	// Exactly one lease push (target_sha) landed.
	require.Len(t, git.pushCalls, 1)
	assert.Equal(t, resolver.target.TargetSHA, git.pushCalls[0].target)
}

func TestRun_SentinelBlocksExecution(t *testing.T) {
	runCtx, pkg, candidates, store, _ := newFixture(t, "PR3")
	require.NoError(t, os.MkdirAll(filepath.Join(runCtx.RunsBase, "needs-recovery"), 0o755))
	blockPath := filepath.Join(runCtx.RunsBase, "needs-recovery", "other-run.json")
	require.NoError(t, os.WriteFile(blockPath, []byte("{}"), 0o644))

	require.NoError(t, Run(context.Background(), 3, runCtx, pkg, candidates, store, Deps{Now: fixedNow()}))

	// No decision written.
	decisionPath, err := runCtx.ResolveRunRelative("70/decision.json")
	require.NoError(t, err)
	assert.NoFileExists(t, decisionPath)
}

func TestRun_ResumeFromBranchPushed(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR4")
	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	intention.Stage = contracts.IntentionStageBranchPushed
	require.NoError(t, store.Save(intention))

	git := &fakeGit{head: resolver.target.TargetSHA}
	deps := Deps{Git: git, Resolver: resolver, Now: fixedNow()}
	require.NoError(t, Run(context.Background(), 4, runCtx, pkg, candidates, store, deps))

	decision := readDecision(t, runCtx)
	assert.Equal(t, contracts.DecisionActionAdopt, decision.Action)
	// No additional push from the resume path (branch already pushed).
	assert.Empty(t, git.pushCalls)
}

func TestRun_ResumeFromDecisionWritten(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR5")
	registryPath := runCtx.RulesRegistryPath()
	appendResult, _ := seedRegistryAdd(t, registryPath, resolver, runCtx.RunID, candidates.CandidatesHash)

	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	intention.Stage = contracts.IntentionStageDecisionWritten
	intention.RegistryAppendResult = &appendResult
	require.NoError(t, store.Save(intention))

	// decision.json pre-existing to simulate crash after stage 5.
	d := contracts.Decision{
		Action: contracts.DecisionActionAdopt,
		Value: contracts.DecisionAdopt{
			Action:               contracts.DecisionActionAdopt,
			SchemaVersion:        "1",
			RunID:                runCtx.RunID,
			IdempotencyKey:       intention.IdempotencyKey,
			BestShaBefore:        intention.BestShaBefore,
			TargetSha:            intention.TargetSha,
			CandidatesHash:       intention.CandidatesHash,
			RegistryAppendResult: appendResult,
			DecidedAt:            fixedNow()(),
		},
	}
	decisionPath, err := runCtx.ResolveRunRelative("70/decision.json")
	require.NoError(t, err)
	require.NoError(t, internalio.WriteJSONAtomic(decisionPath, d))

	git := &fakeGit{head: resolver.target.TargetSHA}
	deps := Deps{Git: git, Resolver: resolver, Now: fixedNow()}
	require.NoError(t, Run(context.Background(), 5, runCtx, pkg, candidates, store, deps))

	events := readStateEvents(t, runCtx)
	require.NotEmpty(t, events)
	assert.Equal(t, contracts.StateKindPromoted, events[len(events)-1].Kind)
	assert.NoFileExists(t, intentionPath(t, runCtx))
}

func TestRun_RollbackOnPushFailure(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR6")
	// Push fails, remote still at best_sha_before so rollback is safe.
	git := &fakeGit{head: resolver.target.BestShaBefore, pushErr: ErrLeaseFailure}
	deps := Deps{Git: git, Resolver: resolver, Now: fixedNow()}
	require.NoError(t, Run(context.Background(), 6, runCtx, pkg, candidates, store, deps))

	decision := readDecision(t, runCtx)
	assert.Equal(t, contracts.DecisionActionRollback, decision.Action)
	rb, ok := decision.Value.(contracts.DecisionRollback)
	require.True(t, ok)
	assert.Equal(t, contracts.RollbackReasonLeaseFailure, rb.RollbackReason)
}

func TestRun_NeedsManualRecoveryOnRemoteDivergence(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR7")
	// Push succeeds, but later rollback path reads an unrelated head.
	git := &fakeGit{head: strings.Repeat("9", 40), pushErr: ErrRemoteDivergence}
	deps := Deps{Git: git, Resolver: resolver, Now: fixedNow()}
	err := Run(context.Background(), 7, runCtx, pkg, candidates, store, deps)
	require.ErrorIs(t, err, ErrNeedsManualRecovery)

	// sentinel written.
	sentinelPath := filepath.Join(runCtx.RunsBase, "needs-recovery", string(runCtx.RunID)+".json")
	assert.FileExists(t, sentinelPath)

	events := readStateEvents(t, runCtx)
	require.NotEmpty(t, events)
	assert.Equal(t, contracts.StateKindNeedsManualRecovery, events[len(events)-1].Kind)
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

func TestRun_AdoptIdempotencyDuplicatePlanning(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR9")
	git := &fakeGit{head: resolver.target.BestShaBefore}
	deps := Deps{Git: git, Resolver: resolver, Now: fixedNow()}
	require.NoError(t, Run(context.Background(), 9, runCtx, pkg, candidates, store, deps))
	require.NoError(t, Run(context.Background(), 9, runCtx, pkg, candidates, store, deps))

	lines, err := readRegistryLines(runCtx.RulesRegistryPath())
	require.NoError(t, err)
	require.Len(t, lines, 1)

	events := readStateEvents(t, runCtx)
	promoted := 0
	for _, event := range events {
		if event.Kind == contracts.StateKindPromoted {
			promoted++
		}
	}
	assert.Equal(t, 1, promoted)
}

func TestRun_PlanningRecoveryPrePushCrash(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR10")
	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	require.NoError(t, store.Save(intention))

	git := &fakeGit{head: resolver.target.BestShaBefore}
	deps := Deps{Git: git, Resolver: resolver, Now: fixedNow()}
	require.NoError(t, Run(context.Background(), 10, runCtx, pkg, candidates, store, deps))

	events := readStateEvents(t, runCtx)
	kinds := make([]contracts.StateKind, 0, len(events))
	for _, event := range events {
		kinds = append(kinds, event.Kind)
	}
	assert.Contains(t, kinds, contracts.StateKindInterrupted)
	assert.Contains(t, kinds, contracts.StateKindPromoted)
}

func TestRun_RollbackWithoutRegistryAppendSkipsRollbackRegistryStage(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR11")
	git := &fakeGit{head: resolver.target.BestShaBefore, pushErr: ErrLeaseFailure}
	deps := Deps{Git: git, Resolver: resolver, Now: fixedNow()}
	require.NoError(t, Run(context.Background(), 11, runCtx, pkg, candidates, store, deps))

	assert.NoFileExists(t, intentionPath(t, runCtx))
	decision := readDecision(t, runCtx)
	assert.Equal(t, contracts.DecisionActionRollback, decision.Action)
	lines, err := readRegistryLines(runCtx.RulesRegistryPath())
	require.NoError(t, err)
	assert.Len(t, lines, 0)
}

func TestRun_ResumeFromRollingBackRegistryAppended(t *testing.T) {
	runCtx, pkg, candidates, store, resolver := newFixtureWithResolver(t, "PR12")
	appendResult, _ := seedRegistryAdd(t, runCtx.RulesRegistryPath(), resolver, runCtx.RunID, candidates.CandidatesHash)
	rollbackResult, err := appendRegistryRollback(runCtx, contracts.IntentionRecord{
		SchemaVersion:        "1",
		Stage:                contracts.IntentionStageRollingBackBranchReverted,
		IdempotencyKey:       contracts.ComputeAdoptIdempotencyKey(string(runCtx.RunID), resolver.target.TargetSHA, resolver.target.BestShaBefore, candidates.CandidatesHash),
		RunID:                runCtx.RunID,
		BestShaBefore:        resolver.target.BestShaBefore,
		TargetSha:            resolver.target.TargetSHA,
		CandidatesHash:       candidates.CandidatesHash,
		RegistryHeadBefore:   "",
		StartedAt:            time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
		RegistryAppendResult: &appendResult,
	}, contracts.RollbackReasonTransactionalFailure, time.Date(2026, 4, 21, 10, 0, 1, 0, time.UTC))
	require.NoError(t, err)

	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	intention.Stage = contracts.IntentionStageRollingBackRegistryAppended
	intention.RecoveryReason = contracts.RollbackReasonTransactionalFailure
	intention.FailedStep = contracts.FailedStep70
	intention.RegistryAppendResult = &rollbackResult
	require.NoError(t, store.Save(intention))

	deps := Deps{Git: &fakeGit{head: resolver.target.BestShaBefore}, Resolver: resolver, Now: fixedNow()}
	require.NoError(t, Run(context.Background(), 12, runCtx, pkg, candidates, store, deps))

	events := readStateEvents(t, runCtx)
	assert.Equal(t, contracts.StateKindRollback, events[len(events)-1].Kind)
	assert.NoFileExists(t, intentionPath(t, runCtx))
}

// ---- helpers ----

type fixtureResolver struct {
	target Target
}

func (r *fixtureResolver) Resolve(internalio.RunContext, *contracts.TaskPackage, *contracts.Candidates) (Target, bool, error) {
	return r.target, true, nil
}

type fakePushCall struct {
	branch   string
	target   string
	expected string
}

type fakeGit struct {
	head      string
	pushErr   error
	pushCalls []fakePushCall
}

func (g *fakeGit) RemoteHead(string) (string, error) {
	return g.head, nil
}

func (g *fakeGit) PushForceWithLease(branch, target, expected string) error {
	g.pushCalls = append(g.pushCalls, fakePushCall{branch: branch, target: target, expected: expected})
	if g.pushErr != nil && len(g.pushCalls) == 1 {
		return g.pushErr
	}
	// Subsequent calls (rollback revert) succeed so the rollback path can
	// reach terminal state.
	return nil
}

func fixedNow() func() time.Time {
	t := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	return func() time.Time {
		t = t.Add(time.Second)
		return t
	}
}

func newFixture(t *testing.T, prLabel string) (internalio.RunContext, *contracts.TaskPackage, *contracts.Candidates, IntentionWriter, *fixtureResolver) {
	t.Helper()
	tempRuns := t.TempDir()
	worktreeBase := t.TempDir()
	runID := contracts.RunID("2026-04-21-" + prLabel + "-abcdef0")
	runCtx, err := internalio.NewRunContext(runID, tempRuns, worktreeBase)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(runCtx.RunDir(), 0o755))

	pkg := validTaskPackage(runID)
	require.NoError(t, internalio.WriteJSONAtomic(runCtx.TaskPackagePath(), pkg))
	// Rebuild runCtx with worktrees populated.
	runCtx, err = internalio.RunContextFromTaskPackage(pkg, tempRuns, worktreeBase)
	require.NoError(t, err)

	candidates := emptyCandidates(runID)

	store := newMemStore(intentionPath(t, runCtx))
	return runCtx, &pkg, &candidates, store, &fixtureResolver{}
}

func newFixtureWithResolver(t *testing.T, prLabel string) (internalio.RunContext, *contracts.TaskPackage, *contracts.Candidates, IntentionWriter, *fixtureResolver) {
	runCtx, pkg, candidates, store, resolver := newFixture(t, prLabel)
	resolver.target = Target{
		BestBranch:    "best",
		BestShaBefore: strings.Repeat("1", 40),
		TargetSHA:     strings.Repeat("2", 40),
		RulesToAppend: []contracts.RuleRegistryEntry{adoptAddedEntry(runCtx.RunID, candidates.CandidatesHash)},
	}
	// Idempotency key requires the target.TargetSHA to be known; re-derive
	// against the empty candidates hash used by newFixture.
	return runCtx, pkg, candidates, store, resolver
}

func adoptAddedEntry(runID contracts.RunID, candidatesHash string) contracts.RuleRegistryEntry {
	key := contracts.ComputeAdoptIdempotencyKey(string(runID), strings.Repeat("2", 40), strings.Repeat("1", 40), candidatesHash)
	v := contracts.RuleRegistryAdded{
		Kind:           contracts.RegistryKindAdded,
		SchemaVersion:  "1",
		RuleID:         "rule-seed",
		RulePath:       "rules/rule-seed.md",
		Sha256:         strings.Repeat("a", 64),
		IdempotencyKey: key,
		VersionSeq:     1,
		PrevHash:       "",
		ByRunID:        runID,
		At:             time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
	}
	return contracts.RuleRegistryEntry{Kind: v.Kind, Value: v}
}

func planningIntention(runID contracts.RunID, target Target, candidatesHash string) contracts.IntentionRecord {
	return contracts.IntentionRecord{
		SchemaVersion:      "1",
		Stage:              contracts.IntentionStagePlanning,
		IdempotencyKey:     contracts.ComputeAdoptIdempotencyKey(string(runID), target.TargetSHA, target.BestShaBefore, candidatesHash),
		RunID:              runID,
		BestShaBefore:      target.BestShaBefore,
		TargetSha:          target.TargetSHA,
		CandidatesHash:     candidatesHash,
		RegistryHeadBefore: "",
		StartedAt:          time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
	}
}

func seedRegistryAdd(t *testing.T, path string, resolver *fixtureResolver, runID contracts.RunID, candidatesHash string) (contracts.RegistryAppendResult, contracts.RuleRegistryEntry) {
	t.Helper()
	entry := adoptAddedEntry(runID, candidatesHash)
	result, err := internalio.AppendRegistryEntry(path, entry)
	require.NoError(t, err)
	return result, entry
}

func validTaskPackage(runID contracts.RunID) contracts.TaskPackage {
	base := strings.Repeat("a", 40)
	worktrees := make([]contracts.WorktreeAllocation, 0, 6)
	for pass := 1; pass <= 2; pass++ {
		for _, agent := range []contracts.AgentID{"a1", "a2", "a3"} {
			worktrees = append(worktrees, contracts.WorktreeAllocation{
				Agent:   agent,
				Pass:    pass,
				Path:    filepath.Join("/tmp/mai-cmux/step70-test", string(runID), string(agent), pad(pass)),
				Branch:  "auto-improve/" + string(runID) + "/pass" + pad(pass) + "/" + string(agent),
				BaseSHA: base,
				HeadSHA: base,
			})
		}
	}
	return contracts.TaskPackage{
		SchemaVersion:           "1",
		RunID:                   runID,
		PR:                      1,
		Title:                   "test",
		BaseSHA:                 base,
		BestBranch:              "best",
		ReconstructedTaskPrompt: "p",
		Worktrees:               worktrees,
		CreatedAt:               time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
	}
}

func pad(pass int) string {
	if pass == 1 {
		return "1"
	}
	return "2"
}

func emptyCandidates(runID contracts.RunID) contracts.Candidates {
	return contracts.Candidates{
		SchemaVersion:  "1",
		RunID:          runID,
		Candidates:     []contracts.Candidate{},
		CandidatesHash: contracts.CanonicalCandidatesHash(nil),
		CreatedAt:      time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
	}
}

// memStore is a minimal in-memory IntentionWriter replacement for tests that
// exercise stage transitions. Saves are also persisted to disk so that a
// subsequent Run() call (resume) sees them.
type memStore struct {
	path string
}

func newMemStore(path string) *memStore { return &memStore{path: path} }

func (m *memStore) Load() (*contracts.IntentionRecord, error) {
	if m.path == "" {
		return nil, nil
	}
	_, err := os.Stat(m.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	rec, err := internalio.ReadJSON[contracts.IntentionRecord](m.path)
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

func (m *memStore) Save(r contracts.IntentionRecord) error {
	if m.path == "" {
		return nil
	}
	return internalio.WriteJSONAtomic(m.path, r)
}

func (m *memStore) Delete() error {
	if m.path == "" {
		return nil
	}
	if err := os.Remove(m.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func readDecision(t *testing.T, runCtx internalio.RunContext) contracts.Decision {
	t.Helper()
	path, err := runCtx.ResolveRunRelative("70/decision.json")
	require.NoError(t, err)
	d, err := internalio.ReadJSON[contracts.Decision](path)
	require.NoError(t, err)
	return d
}

func readStateEvents(t *testing.T, runCtx internalio.RunContext) []contracts.StateEntry {
	t.Helper()
	events, err := state.ScanEventsForRun(runCtx, runCtx.RunID)
	require.NoError(t, err)
	return events
}

func intentionPath(t *testing.T, runCtx internalio.RunContext) string {
	t.Helper()
	p, err := runCtx.ResolveRunRelative("70/intention.json")
	require.NoError(t, err)
	return p
}

func tryNonBlockingFlock(fd int) error {
	return syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
}
