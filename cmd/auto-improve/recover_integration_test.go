package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/nishimoto265/harnest/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationRecoverAdoptAnywaySubprocess(t *testing.T) {
	requireIntegrationEnv(t)

	root, runsBase, worktreeBase, runID := seedRecoverActionRun(t)
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))
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

	bin := buildIntegrationBinary(t)
	binDir := filepath.Join(root, "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	writeExecutable(t, filepath.Join(binDir, "git"), recoverAdoptAnywayGitScript())

	pkg, err := internalio.ReadJSON[contracts.TaskPackage](filepath.Join(runDir, "task-package.json"))
	require.NoError(t, err)
	var worktreesList strings.Builder
	for _, wt := range pkg.Worktrees {
		worktreesList.WriteString(wt.Path)
		worktreesList.WriteByte('\n')
	}
	gitStateDir := filepath.Join(root, "git-state")
	require.NoError(t, os.MkdirAll(gitStateDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(gitStateDir, "worktrees.list"), []byte(worktreesList.String()), 0o644))

	cmd := exec.Command(bin, "recover", "--run", string(runID), "--adopt-anyway")
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		integrationTrustedPathEnvVar+"="+trustedPathWithFakeBin(binDir),
		"AUTO_IMPROVE_GIT_STATE_DIR="+gitStateDir,
		"AUTO_IMPROVE_TEST_REMOTE_SHA="+strings.Repeat("b", 40),
	)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	require.NoError(t, cmd.Run(), "stdout=%s stderr=%s", stdout.String(), stderr.String())

	events, err := state.ScanEventsForRun(mustNewRunCtx(t, runID, runsBase, worktreeBase), runID)
	require.NoError(t, err)
	require.NotEmpty(t, events)
	assert.Equal(t, contracts.StateKindPromoted, events[len(events)-1].Kind)
	assert.NoFileExists(t, filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelFilename(runID)))
}
