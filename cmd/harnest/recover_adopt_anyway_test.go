package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/nishimoto265/harnest/internal/state"
	"github.com/nishimoto265/harnest/internal/steps/step70_decide"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecoverAdoptAnywayPromotesAndClearsSentinel(t *testing.T) {
	root, runsBase, worktreeBase, runID := seedRecoverActionRun(t)
	runDir := filepath.Join(runsBase, string(runID))
	require.NoError(t, os.WriteFile(filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelFilename(runID)), []byte(`{"run_id":"2026-04-21-PR52-abcdef0","pr":52,"reason":"transactional_failure","failed_step":"70","created_at":"2026-04-21T12:00:00Z"}`), 0o644))
	candidatesDoc, err := internalio.ReadJSON[contracts.Candidates](filepath.Join(runDir, "40", "candidates.json"))
	require.NoError(t, err)
	candidatesHash := candidatesDoc.CandidatesHash
	intention := seedRecoverIntention(runID, contracts.IntentionStageDecisionWritten, strings.Repeat("a", 40), strings.Repeat("b", 40), candidatesHash)
	appendResult := appendRecoverRegistryEntry(t, runsBase, runID, intention)
	intention.RegistryAppendResult = &appendResult
	require.NoError(t, internalio.WriteJSONAtomic(filepath.Join(runDir, "70", "intention.json"), intention))
	decision := contracts.Decision{
		Action: contracts.DecisionActionAdopt,
		Value: contracts.DecisionAdopt{
			Action:               contracts.DecisionActionAdopt,
			SchemaVersion:        "1",
			RunID:                runID,
			IdempotencyKey:       intention.IdempotencyKey,
			BestShaBefore:        intention.BestShaBefore,
			TargetSha:            intention.TargetSha,
			CandidatesHash:       candidatesHash,
			RegistryAppendResult: appendResult,
			DecidedAt:            time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
		},
	}
	require.NoError(t, internalio.WriteJSONAtomic(filepath.Join(runDir, "70", "decision.json"), decision))
	seedRecoverPublishedRule(t, runsBase)

	writeTestConfig(t, root, runsBase, worktreeBase)
	originalGitFactory := recoverGitOpsForRepo
	recoverGitOpsForRepo = func(string) step70_decide.GitOps {
		return &recoverTestGit{head: strings.Repeat("b", 40)}
	}
	t.Cleanup(func() { recoverGitOpsForRepo = originalGitFactory })

	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(originalWD) })

	var stdout bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)
	cmd.SetArgs([]string{"recover", "--run", string(runID), "--adopt-anyway"})
	require.NoError(t, cmd.Execute())

	assert.NoFileExists(t, filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelFilename(runID)))
	assert.NoFileExists(t, filepath.Join(runDir, "70", "intention.json"))
	events, err := state.ScanEventsForRun(mustNewRunCtx(t, runID, runsBase, worktreeBase), runID)
	require.NoError(t, err)
	require.NotEmpty(t, events)
	assert.Equal(t, contracts.StateKindPromoted, events[len(events)-1].Kind)
}

func TestRecoverAdoptAnywayAllowsNeedsManualRecoveryStage(t *testing.T) {
	root, runsBase, worktreeBase, runID := seedRecoverActionRun(t)
	runDir := filepath.Join(runsBase, string(runID))
	require.NoError(t, os.WriteFile(filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelFilename(runID)), []byte(`{"run_id":"2026-04-21-PR52-abcdef0","pr":52,"reason":"transactional_failure","failed_step":"70","created_at":"2026-04-21T12:00:00Z"}`), 0o644))
	candidatesDoc, err := internalio.ReadJSON[contracts.Candidates](filepath.Join(runDir, "40", "candidates.json"))
	require.NoError(t, err)
	candidatesHash := candidatesDoc.CandidatesHash
	intention := seedRecoverIntention(runID, contracts.IntentionStageNeedsManualRecovery, strings.Repeat("a", 40), strings.Repeat("b", 40), candidatesHash)
	appendResult := appendRecoverRegistryEntry(t, runsBase, runID, intention)
	intention.RegistryAppendResult = &appendResult
	intention.RecoveryReason = contracts.RollbackReasonTransactionalFailure
	intention.FailedStep = contracts.FailedStep70
	require.NoError(t, internalio.WriteJSONAtomic(filepath.Join(runDir, "70", "intention.json"), intention))
	seedRecoverPublishedRule(t, runsBase)

	writeTestConfig(t, root, runsBase, worktreeBase)
	originalGitFactory := recoverGitOpsForRepo
	recoverGitOpsForRepo = func(string) step70_decide.GitOps {
		return &recoverTestGit{head: strings.Repeat("b", 40)}
	}
	t.Cleanup(func() { recoverGitOpsForRepo = originalGitFactory })

	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(originalWD) })

	var stdout bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)
	cmd.SetArgs([]string{"recover", "--run", string(runID), "--adopt-anyway"})
	require.NoError(t, cmd.Execute())

	assert.NoFileExists(t, filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelFilename(runID)))
	assert.NoFileExists(t, filepath.Join(runDir, "70", "intention.json"))
	events, err := state.ScanEventsForRun(mustNewRunCtx(t, runID, runsBase, worktreeBase), runID)
	require.NoError(t, err)
	require.NotEmpty(t, events)
	assert.Equal(t, contracts.StateKindPromoted, events[len(events)-1].Kind)
}

func TestRecoverAdoptAnywayReconstructsMissingRegistryAppendResult(t *testing.T) {
	root, runsBase, worktreeBase, runID := seedRecoverActionRun(t)
	runDir := filepath.Join(runsBase, string(runID))
	require.NoError(t, os.WriteFile(filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelFilename(runID)), []byte(`{"run_id":"2026-04-21-PR52-abcdef0","pr":52,"reason":"transactional_failure","failed_step":"70","created_at":"2026-04-21T12:00:00Z"}`), 0o644))
	candidatesDoc, err := internalio.ReadJSON[contracts.Candidates](filepath.Join(runDir, "40", "candidates.json"))
	require.NoError(t, err)
	candidatesHash := candidatesDoc.CandidatesHash
	intention := seedRecoverIntention(runID, contracts.IntentionStageNeedsManualRecovery, strings.Repeat("a", 40), strings.Repeat("b", 40), candidatesHash)
	intention.RegistryAppendResult = nil
	intention.RecoveryReason = contracts.RollbackReasonTransactionalFailure
	intention.FailedStep = contracts.FailedStep70
	require.NoError(t, internalio.WriteJSONAtomic(filepath.Join(runDir, "70", "intention.json"), intention))

	_ = appendRecoverRegistryEntry(t, runsBase, runID, intention)
	seedRecoverPublishedRule(t, runsBase)

	writeTestConfig(t, root, runsBase, worktreeBase)
	originalGitFactory := recoverGitOpsForRepo
	recoverGitOpsForRepo = func(string) step70_decide.GitOps {
		return &recoverTestGit{head: strings.Repeat("b", 40)}
	}
	t.Cleanup(func() { recoverGitOpsForRepo = originalGitFactory })

	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(originalWD) })

	var stdout bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)
	cmd.SetArgs([]string{"recover", "--run", string(runID), "--adopt-anyway"})
	require.NoError(t, cmd.Execute())

	decision, err := internalio.ReadJSON[contracts.Decision](filepath.Join(runDir, "70", "decision.json"))
	require.NoError(t, err)
	adopt, ok := decision.Value.(contracts.DecisionAdopt)
	require.True(t, ok)
	assert.NotEmpty(t, adopt.RegistryAppendResult.Sha256)
	assert.NoFileExists(t, filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelFilename(runID)))
}
