package step70_decide

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/policyrepo"
	"github.com/nishimoto265/auto-improve/internal/state"
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
			runCtx, _, candidates, _, resolver := newFixtureWithResolver(t, tc.prLabel)
			store := newMemStore(intentionPath(t, runCtx))
			intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
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
			runCtx, _, candidates, _, resolver := newFixtureWithResolver(t, tc.prLabel)
			store := newMemStore(intentionPath(t, runCtx))
			intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
			tc.mutate(&intention)

			assertDrivePolicyPublishManualRecovery(t, runCtx, store, intention, tc.deps, tc.wantDetail)
		})
	}
}

func TestDrivePolicyPublish_PlanMismatchRequiresManualRecovery(t *testing.T) {
	runCtx, _, candidates, _, resolver := newFixtureWithResolver(t, "PR113")
	store := newMemStore(intentionPath(t, runCtx))
	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	intention.Stage = contracts.IntentionStagePolicyPublishing
	intention.PolicyBranch = "auto-improve/policy"
	intention.PolicyHeadBefore = strings.Repeat("1", 40)
	intention.PolicyHeadAfter = strings.Repeat("2", 40)

	originalPrepare := preparePolicyPublish
	preparePolicyPublish = func(context.Context, string, string, string, string, string) (*policyrepo.PreparedPublish, error) {
		return &policyrepo.PreparedPublish{Head: strings.Repeat("3", 40)}, nil
	}
	t.Cleanup(func() { preparePolicyPublish = originalPrepare })

	assertDrivePolicyPublishManualRecovery(t, runCtx, store, intention, Deps{
		PolicyBranch: "auto-improve/policy",
		RepoRoot:     "repo-root",
		Git:          &fakeGit{head: strings.Repeat("1", 40)},
		Now:          fixedNow(),
	}, "policy_publish_plan_mismatch")
}

func TestPolicySnapshotMetadataForBranch_TrimsAndRejectsMismatch(t *testing.T) {
	runCtx, _, _, _, _ := newFixtureWithResolver(t, "PR114")
	policyDir := filepath.Join(runCtx.RunDir(), "policy")
	require.NoError(t, os.MkdirAll(policyDir, 0o755))
	require.NoError(t, internalio.WriteJSONAtomic(filepath.Join(policyDir, "snapshot.json"), policyrepo.SnapshotMetadata{
		SchemaVersion: "1",
		PolicyBranch:  " auto-improve/policy ",
		PolicyHead:    strings.Repeat("1", 40),
		RegistryHead:  "",
	}))

	meta, ok, err := policySnapshotMetadataForBranch(runCtx, "auto-improve/policy")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, strings.Repeat("1", 40), meta.PolicyHead)

	require.NoError(t, internalio.WriteJSONAtomic(filepath.Join(policyDir, "snapshot.json"), policyrepo.SnapshotMetadata{
		SchemaVersion: "1",
		PolicyBranch:  "auto-improve/other-policy",
		PolicyHead:    strings.Repeat("1", 40),
		RegistryHead:  "",
	}))

	_, _, err = policySnapshotMetadataForBranch(runCtx, "auto-improve/policy")
	require.ErrorContains(t, err, "policy snapshot branch mismatch")
}

func TestLocalPolicySnapshotPreAdoptBlockReason_RejectsBranchMetadata(t *testing.T) {
	runCtx, _, _, _, _ := newFixtureWithResolver(t, "PR115")
	policyDir := filepath.Join(runCtx.RunDir(), "policy")
	require.NoError(t, os.MkdirAll(policyDir, 0o755))
	require.NoError(t, internalio.WriteJSONAtomic(filepath.Join(policyDir, "snapshot.json"), policyrepo.SnapshotMetadata{
		SchemaVersion: "1",
		PolicyBranch:  "auto-improve/policy",
		PolicyHead:    strings.Repeat("1", 40),
		RegistryHead:  "",
	}))

	_, err := localPolicySnapshotPreAdoptBlockReason(runCtx)
	require.ErrorContains(t, err, "local policy snapshot has branch metadata")
}

func assertDrivePolicyPublishManualRecovery(t *testing.T, runCtx internalio.RunContext, store IntentionWriter, intention contracts.IntentionRecord, deps Deps, wantDetail string) {
	t.Helper()
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
