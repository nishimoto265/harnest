package step70_decide

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/nishimoto265/harnest/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDrivePolicyPublish_ConfigFailuresRequireManualRecovery(t *testing.T) {
	tests := []struct {
		name       string
		prLabel    string
		mutate     func(*contracts.IntentionRecord)
		deps       Deps
		wantDetail string
	}{
		{
			name:    "policy branch missing while publishing",
			prLabel: "PR106",
			mutate: func(intention *contracts.IntentionRecord) {
				intention.Stage = contracts.IntentionStagePolicyPublishing
				intention.PolicyBranch = "auto-improve/policy"
				intention.PolicyHeadBefore = strings.Repeat("1", 40)
			},
			deps:       Deps{PolicyBranch: "", RepoRoot: "repo-root", Git: &fakeGit{head: strings.Repeat("1", 40)}, Now: fixedNow()},
			wantDetail: "policy_branch_config_missing",
		},
		{
			name:    "repo root missing",
			prLabel: "PR107",
			mutate: func(intention *contracts.IntentionRecord) {
				intention.Stage = contracts.IntentionStagePolicyPublishing
				intention.PolicyBranch = "auto-improve/policy"
				intention.PolicyHeadBefore = strings.Repeat("1", 40)
			},
			deps:       Deps{PolicyBranch: "auto-improve/policy", RepoRoot: "", Git: &fakeGit{head: strings.Repeat("1", 40)}, Now: fixedNow()},
			wantDetail: "policy_repo_root_missing",
		},
		{
			name:    "configured branch changes mid-publish",
			prLabel: "PR108",
			mutate: func(intention *contracts.IntentionRecord) {
				intention.Stage = contracts.IntentionStagePolicyPublishing
				intention.PolicyBranch = "auto-improve/policy-old"
				intention.PolicyHeadBefore = strings.Repeat("1", 40)
			},
			deps:       Deps{PolicyBranch: "auto-improve/policy", RepoRoot: "repo-root", Git: &fakeGit{head: strings.Repeat("1", 40)}, Now: fixedNow()},
			wantDetail: "policy_branch_config_mismatch",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runCtx, store, intention := newPolicyPublishTestFixture(t, tc.prLabel)
			tc.mutate(&intention)

			assertDrivePolicyPublishManualRecovery(t, runCtx, store, intention, tc.deps, tc.wantDetail)
		})
	}
}

func TestDrivePolicyPublish_RemoteAndProbeFailuresRequireManualRecovery(t *testing.T) {
	tests := []struct {
		name       string
		prLabel    string
		mutate     func(*contracts.IntentionRecord)
		deps       Deps
		setup      func(*testing.T)
		wantDetail string
	}{
		{
			name:    "published remote head probe fails",
			prLabel: "PR109",
			mutate: func(intention *contracts.IntentionRecord) {
				intention.Stage = contracts.IntentionStagePolicyPublished
				intention.PolicyBranch = "auto-improve/policy"
				intention.PolicyHeadBefore = strings.Repeat("1", 40)
				intention.PolicyHeadAfter = strings.Repeat("2", 40)
			},
			deps:       Deps{PolicyBranch: "auto-improve/policy", RepoRoot: "repo-root", Git: &fakeGit{remoteHeadErr: errors.New("remote unavailable")}, Now: fixedNow()},
			wantDetail: "policy_remote_head_failure",
		},
		{
			name:    "publishing remote head probe fails",
			prLabel: "PR110",
			mutate: func(intention *contracts.IntentionRecord) {
				intention.Stage = contracts.IntentionStagePolicyPublishing
				intention.PolicyBranch = "auto-improve/policy"
				intention.PolicyHeadBefore = strings.Repeat("1", 40)
			},
			deps:       Deps{PolicyBranch: "auto-improve/policy", RepoRoot: "repo-root", Git: &fakeGit{remoteHeadErr: errors.New("remote unavailable")}, Now: fixedNow()},
			wantDetail: "policy_remote_head_failure",
		},
		{
			name:    "local snapshot parity probe fails after branch moved",
			prLabel: "PR111",
			mutate: func(intention *contracts.IntentionRecord) {
				intention.Stage = contracts.IntentionStagePolicyPublishing
				intention.PolicyBranch = "auto-improve/policy"
				intention.PolicyHeadBefore = strings.Repeat("1", 40)
			},
			deps: Deps{PolicyBranch: "auto-improve/policy", RepoRoot: "repo-root", Git: &fakeGit{head: strings.Repeat("2", 40)}, Now: fixedNow()},
			setup: func(t *testing.T) {
				originalMatches := branchSnapshotMatchesLocal
				branchSnapshotMatchesLocal = func(context.Context, string, string, string) (bool, error) {
					return false, errors.New("probe failed")
				}
				t.Cleanup(func() { branchSnapshotMatchesLocal = originalMatches })
			},
			wantDetail: "policy_publish_probe_failure",
		},
		{
			name:    "branch moved to untracked snapshot",
			prLabel: "PR112",
			mutate: func(intention *contracts.IntentionRecord) {
				intention.Stage = contracts.IntentionStagePolicyPublishing
				intention.PolicyBranch = "auto-improve/policy"
				intention.PolicyHeadBefore = strings.Repeat("1", 40)
			},
			deps: Deps{PolicyBranch: "auto-improve/policy", RepoRoot: "repo-root", Git: &fakeGit{head: strings.Repeat("2", 40)}, Now: fixedNow()},
			setup: func(t *testing.T) {
				originalMatches := branchSnapshotMatchesLocal
				branchSnapshotMatchesLocal = func(context.Context, string, string, string) (bool, error) {
					return false, nil
				}
				t.Cleanup(func() { branchSnapshotMatchesLocal = originalMatches })
			},
			wantDetail: "policy_branch_stale_or_untracked_publish",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.setup != nil {
				tc.setup(t)
			}
			runCtx, store, intention := newPolicyPublishTestFixture(t, tc.prLabel)
			tc.mutate(&intention)

			assertDrivePolicyPublishManualRecovery(t, runCtx, store, intention, tc.deps, tc.wantDetail)
		})
	}
}

func TestDrivePolicyPublish_PlanMismatchRequiresManualRecovery(t *testing.T) {
	runCtx, store, intention := newPolicyPublishTestFixture(t, "PR113")
	intention.Stage = contracts.IntentionStagePolicyPublishing
	intention.PolicyBranch = "auto-improve/policy"
	intention.PolicyHeadBefore = strings.Repeat("1", 40)
	intention.PolicyHeadAfter = strings.Repeat("2", 40)
	plan := &fakePolicyPublishPlan{head: strings.Repeat("3", 40)}

	originalPrepare := preparePolicyPublish
	preparePolicyPublish = func(context.Context, string, string, string, string, string) (policyPublishPlan, error) {
		return plan, nil
	}
	t.Cleanup(func() { preparePolicyPublish = originalPrepare })

	assertDrivePolicyPublishManualRecovery(t, runCtx, store, intention, Deps{
		PolicyBranch: "auto-improve/policy",
		RepoRoot:     "repo-root",
		Git:          &fakeGit{head: strings.Repeat("1", 40)},
		Now:          fixedNow(),
	}, "policy_publish_plan_mismatch")
	assert.Equal(t, 1, plan.cleanupCalls)
}

func TestDrivePolicyPublish_PostSavePushFailureRequiresManualRecovery(t *testing.T) {
	runCtx, store, intention := newPolicyPublishTestFixture(t, "PR116")
	intention.Stage = contracts.IntentionStagePolicyPublishing
	intention.PolicyBranch = "auto-improve/policy"
	intention.PolicyHeadBefore = strings.Repeat("1", 40)
	plannedHead := strings.Repeat("2", 40)
	plan := &fakePolicyPublishPlan{head: plannedHead, pushErr: errors.New("push failed")}

	originalPrepare := preparePolicyPublish
	preparePolicyPublish = func(context.Context, string, string, string, string, string) (policyPublishPlan, error) {
		return plan, nil
	}
	t.Cleanup(func() { preparePolicyPublish = originalPrepare })

	assertDrivePolicyPublishManualRecovery(t, runCtx, store, intention, Deps{
		PolicyBranch: "auto-improve/policy",
		RepoRoot:     "repo-root",
		Git:          &fakeGit{head: strings.Repeat("1", 40)},
		Now:          fixedNow(),
	}, "policy_publish_failure")
	assert.Equal(t, 1, plan.pushCalls)
	assert.Equal(t, 1, plan.cleanupCalls)

	loaded, err := store.Load()
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, plannedHead, loaded.PolicyHeadAfter)
}

func TestDrivePolicyPublish_RetryWithPersistedHeadAfterPushFailureRequiresManualRecovery(t *testing.T) {
	runCtx, store, intention := newPolicyPublishTestFixture(t, "PR117")
	intention.Stage = contracts.IntentionStagePolicyPublishing
	intention.PolicyBranch = "auto-improve/policy"
	intention.PolicyHeadBefore = strings.Repeat("1", 40)
	intention.PolicyHeadAfter = strings.Repeat("2", 40)
	plan := &fakePolicyPublishPlan{head: intention.PolicyHeadAfter, pushErr: errors.New("push failed")}

	originalPrepare := preparePolicyPublish
	preparePolicyPublish = func(context.Context, string, string, string, string, string) (policyPublishPlan, error) {
		return plan, nil
	}
	t.Cleanup(func() { preparePolicyPublish = originalPrepare })

	assertDrivePolicyPublishManualRecovery(t, runCtx, store, intention, Deps{
		PolicyBranch: "auto-improve/policy",
		RepoRoot:     "repo-root",
		Git:          &fakeGit{head: strings.Repeat("1", 40)},
		Now:          fixedNow(),
	}, "policy_publish_failure")
	assert.Equal(t, 1, plan.pushCalls)
	assert.Equal(t, 1, plan.cleanupCalls)
}

func TestDrivePolicyPublish_RetryWithPersistedHeadAfterAlreadyRemoteMarksPublished(t *testing.T) {
	runCtx, store, intention := newPolicyPublishTestFixture(t, "PR118")
	intention.Stage = contracts.IntentionStagePolicyPublishing
	intention.PolicyBranch = "auto-improve/policy"
	intention.PolicyHeadBefore = strings.Repeat("1", 40)
	intention.PolicyHeadAfter = strings.Repeat("2", 40)

	prepareCalled := false
	originalPrepare := preparePolicyPublish
	preparePolicyPublish = func(context.Context, string, string, string, string, string) (policyPublishPlan, error) {
		prepareCalled = true
		return nil, errors.New("prepare should not be called")
	}
	t.Cleanup(func() { preparePolicyPublish = originalPrepare })

	updated, err := drivePolicyPublish(context.Background(), 918, runCtx, intention, store, state.NewWriter(runCtx), Deps{
		PolicyBranch: "auto-improve/policy",
		RepoRoot:     "repo-root",
		Git:          &fakeGit{head: intention.PolicyHeadAfter},
		Now:          fixedNow(),
	})
	require.NoError(t, err)
	assert.False(t, prepareCalled)
	assert.Equal(t, contracts.IntentionStagePolicyPublished, updated.Stage)

	loaded, err := store.Load()
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, contracts.IntentionStagePolicyPublished, loaded.Stage)
}

func newPolicyPublishTestFixture(t *testing.T, prLabel string) (internalio.RunContext, IntentionWriter, contracts.IntentionRecord) {
	t.Helper()
	runCtx, _, candidates, _, resolver := newFixtureWithResolver(t, prLabel)
	store := newMemStore(intentionPath(t, runCtx))
	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	intention.RegistryAppendResult = &contracts.RegistryAppendResult{Offset: 12, Sha256: strings.Repeat("a", 64)}
	return runCtx, store, intention
}

func assertDrivePolicyPublishManualRecovery(t *testing.T, runCtx internalio.RunContext, store IntentionWriter, intention contracts.IntentionRecord, deps Deps, wantDetail string) {
	t.Helper()
	require.NoError(t, intention.Validate())
	_, err := drivePolicyPublish(context.Background(), 900, runCtx, intention, store, state.NewWriter(runCtx), deps)
	require.ErrorIs(t, err, ErrNeedsManualRecovery)

	loaded, loadErr := store.Load()
	require.NoError(t, loadErr)
	require.NotNil(t, loaded)
	assert.Equal(t, contracts.IntentionStageNeedsManualRecovery, loaded.Stage)

	events := readStateEvents(t, runCtx)
	require.NotEmpty(t, events)
	recovery := mustNeedsManualRecoveryEvent(t, events[len(events)-1])
	assert.Equal(t, contracts.RollbackReasonTransactionalFailure, recovery.Reason)
	assert.Equal(t, wantDetail, recovery.Detail)
	assert.FileExists(t, filepath.Join(runCtx.RunsBase, needsRecoveryDir, string(runCtx.RunID)+".json"))
}

type fakePolicyPublishPlan struct {
	head         string
	pushErr      error
	pushCalls    int
	cleanupCalls int
}

func (p *fakePolicyPublishPlan) HeadSHA() string {
	return p.head
}

func (p *fakePolicyPublishPlan) Push(context.Context) error {
	p.pushCalls++
	return p.pushErr
}

func (p *fakePolicyPublishPlan) Cleanup() error {
	p.cleanupCalls++
	return nil
}
