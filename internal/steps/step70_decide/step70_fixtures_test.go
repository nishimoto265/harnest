package step70_decide

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/nishimoto265/harnest/internal/state"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func realTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	real, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	return real
}

type unexpectedResolver struct {
	t *testing.T
}

func (r unexpectedResolver) Resolve(internalio.RunContext, *contracts.TaskPackage, *contracts.Candidates) (Target, bool, error) {
	r.t.Helper()
	r.t.Fatalf("unexpected resolver call")
	return Target{}, false, nil
}

type sequenceResolver struct {
	targets []Target
	index   int
}

func (r *sequenceResolver) Resolve(runCtx internalio.RunContext, _ *contracts.TaskPackage, _ *contracts.Candidates) (Target, bool, error) {
	if len(r.targets) == 0 {
		return Target{}, false, nil
	}
	if r.index >= len(r.targets) {
		target := r.targets[len(r.targets)-1]
		stageFixtureRuleSidecarsForResolver(runCtx, target)
		return target, true, nil
	}
	target := r.targets[r.index]
	r.index++
	stageFixtureRuleSidecarsForResolver(runCtx, target)
	return target, true, nil
}

type fakePushCall struct {
	branch   string
	target   string
	expected string
}
type fakeGit struct {
	head                string
	heads               map[string]string
	remoteHeadErr       error
	pushErr             error
	removeWorktreeErr   error
	pushCalls           []fakePushCall
	onPush              func(fakePushCall)
	remoteHeadCalls     int
	removeWorktreeCalls []string
}
type cancelOnPushGit struct {
	head   string
	cancel context.CancelFunc
}

func (g *fakeGit) RemoteHead(_ context.Context, branch string) (string, error) {
	g.remoteHeadCalls++
	if g.remoteHeadErr != nil {
		return "", g.remoteHeadErr
	}
	if g.heads != nil {
		if head, ok := g.heads[branch]; ok {
			return head, nil
		}
	}
	return g.head, nil
}
func (g *fakeGit) PushForceWithLease(_ context.Context, branch, target, expected string) error {
	call := fakePushCall{branch: branch, target: target, expected: expected}
	g.pushCalls = append(g.pushCalls, call)
	if g.onPush != nil {
		g.onPush(call)
	}
	if g.pushErr != nil && len(g.pushCalls) == 1 {
		return g.pushErr
	}
	// Subsequent calls (rollback revert) succeed so the rollback path can
	// reach terminal state.
	return nil
}
func (g *fakeGit) RemoveWorktree(_ context.Context, path string) error {
	g.removeWorktreeCalls = append(g.removeWorktreeCalls, path)
	return g.removeWorktreeErr
}
func (g cancelOnPushGit) RemoteHead(_ context.Context, _ string) (string, error) {
	return g.head, nil
}
func (g cancelOnPushGit) PushForceWithLease(ctx context.Context, branch, target, expected string) error {
	_ = branch
	_ = target
	_ = expected
	if g.cancel != nil {
		g.cancel()
	}
	return ctx.Err()
}
func (g cancelOnPushGit) RemoveWorktree(_ context.Context, _ string) error {
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
	tempRuns := realTempDir(t)
	worktreeBase := realTempDir(t)
	runID := contracts.RunID("2026-04-21-" + prLabel + "-abcdef0")
	runCtx, err := internalio.NewRunContext(runID, tempRuns, worktreeBase)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(runCtx.RunDir(), 0o755))

	pkg := validTaskPackage(runID, worktreeBase)
	require.NoError(t, internalio.WriteJSONAtomic(runCtx.TaskPackagePath(), pkg))
	// Rebuild runCtx with worktrees populated.
	runCtx, err = internalio.RunContextFromTaskPackage(pkg, tempRuns, worktreeBase)
	require.NoError(t, err)

	candidates := emptyCandidates(runID)

	store := newMemStore(intentionPath(t, runCtx))
	return runCtx, &pkg, &candidates, store, &fixtureResolver{}
}
func mustStagedRulePath(t *testing.T, runCtx internalio.RunContext, rulePath string) string {
	t.Helper()
	path, err := stagedRuleSidecarPath(runCtx, rulePath)
	require.NoError(t, err)
	return path
}
func mustRunPath(t *testing.T, runCtx internalio.RunContext, rel string) string {
	t.Helper()
	path, err := runCtx.ResolveRunRelative(rel)
	require.NoError(t, err)
	return path
}
func newFixtureWithResolver(t *testing.T, prLabel string) (internalio.RunContext, *contracts.TaskPackage, *contracts.Candidates, IntentionWriter, *fixtureResolver) {
	runCtx, pkg, candidates, store, resolver := newFixture(t, prLabel)
	resolver.target = Target{
		BestBranch:    "best",
		BestShaBefore: strings.Repeat("1", 40),
		TargetSHA:     strings.Repeat("2", 40),
		RulesToAppend: []contracts.RuleRegistryEntry{adoptAddedEntry(runCtx.RunID, candidates.CandidatesHash)},
	}
	stageFixtureRuleSidecars(t, runCtx, resolver.target)
	// Idempotency key requires the target.TargetSHA to be known; re-derive
	// against the empty candidates hash used by newFixture.
	return runCtx, pkg, candidates, store, resolver
}
func stageFixtureRuleSidecars(t *testing.T, runCtx internalio.RunContext, target Target) {
	t.Helper()
	for _, entry := range target.RulesToAppend {
		ruleID, rulePath, sha, err := registryEntryRuleSidecar(entry)
		require.NoError(t, err)
		body := fixtureRuleBody(ruleID)
		if sha256String(body) != sha {
			continue
		}
		require.NoError(t, internalio.WriteAtomic(mustStagedRulePath(t, runCtx, rulePath), []byte(body)))
	}
}
func validTaskPackage(runID contracts.RunID, worktreeBase string) contracts.TaskPackage {
	base := strings.Repeat("a", 40)
	worktrees := make([]contracts.WorktreeAllocation, 0, 6)
	for pass := 1; pass <= 2; pass++ {
		for _, agent := range []contracts.AgentID{"a1", "a2", "a3"} {
			worktrees = append(worktrees, contracts.WorktreeAllocation{
				Agent:   agent,
				Pass:    pass,
				Path:    filepath.Join(worktreeBase, fmt.Sprintf("%s-pass%d-%s", runID, pass, agent)),
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
func sha256String(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

type memStore struct {
	path string
}

func newMemStore(path string) *memStore { return &memStore{path: path} }

type trackingStore struct {
	*memStore
	saved []contracts.IntentionRecord
}
type hookStore struct {
	*memStore
	hook func(contracts.IntentionRecord)
}
type loadHookStore struct {
	*memStore
	hook func()
}
type noopStore struct{}
type deleteFailStore struct {
	IntentionWriter
	deleteErr error
}

func newTrackingStore(path string) *trackingStore {
	return &trackingStore{memStore: newMemStore(path)}
}
func newHookStore(path string, hook func(contracts.IntentionRecord)) *hookStore {
	return &hookStore{memStore: newMemStore(path), hook: hook}
}
func newLoadHookStore(path string, hook func()) *loadHookStore {
	return &loadHookStore{memStore: newMemStore(path), hook: hook}
}
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
func (s *loadHookStore) Load() (*contracts.IntentionRecord, error) {
	if s.hook != nil {
		s.hook()
		s.hook = nil
	}
	return s.memStore.Load()
}
func (s *loadHookStore) Save(r contracts.IntentionRecord) error {
	return s.memStore.Save(r)
}
func (s *loadHookStore) Delete() error {
	return s.memStore.Delete()
}
func (noopStore) Load() (*contracts.IntentionRecord, error) { return nil, nil }
func (noopStore) Save(contracts.IntentionRecord) error      { return nil }
func (noopStore) Delete() error                             { return nil }
func (s deleteFailStore) Delete() error                     { return s.deleteErr }
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
func (s *trackingStore) Save(r contracts.IntentionRecord) error {
	s.saved = append(s.saved, r)
	return s.memStore.Save(r)
}
func (s *hookStore) Save(r contracts.IntentionRecord) error {
	if err := s.memStore.Save(r); err != nil {
		return err
	}
	if s.hook != nil {
		s.hook(r)
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
func readStateKinds(t *testing.T, runCtx internalio.RunContext) []contracts.StateKind {
	t.Helper()
	events := readStateEvents(t, runCtx)
	kinds := make([]contracts.StateKind, 0, len(events))
	for _, event := range events {
		kinds = append(kinds, event.Kind)
	}
	return kinds
}
func mustDecisionAdopt(t *testing.T, decision contracts.Decision) contracts.DecisionAdopt {
	t.Helper()
	switch v := decision.Value.(type) {
	case contracts.DecisionAdopt:
		return v
	case *contracts.DecisionAdopt:
		require.NotNil(t, v)
		return *v
	default:
		t.Fatalf("expected adopt decision, got action=%s type=%T", decision.Action, decision.Value)
		return contracts.DecisionAdopt{}
	}
}
func mustNeedsManualRecoveryEvent(t *testing.T, entry contracts.StateEntry) contracts.StateEntryNeedsManualRecovery {
	t.Helper()
	switch v := entry.Value.(type) {
	case contracts.StateEntryNeedsManualRecovery:
		return v
	case *contracts.StateEntryNeedsManualRecovery:
		require.NotNil(t, v)
		return *v
	default:
		t.Fatalf("expected needs_manual_recovery event, got kind=%s type=%T", entry.Kind, entry.Value)
		return contracts.StateEntryNeedsManualRecovery{}
	}
}
func mustDecisionRollback(t *testing.T, decision contracts.Decision) contracts.DecisionRollback {
	t.Helper()
	switch v := decision.Value.(type) {
	case contracts.DecisionRollback:
		return v
	case *contracts.DecisionRollback:
		require.NotNil(t, v)
		return *v
	default:
		t.Fatalf("expected rollback decision, got action=%s type=%T", decision.Action, decision.Value)
		return contracts.DecisionRollback{}
	}
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
