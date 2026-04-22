package step50_implement

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLoadRulePayloads_RejectsSymlinkedCandidateBody covers H1. A malicious
// candidates.json could point proposed_body_path at a plain file within 40/
// while the on-disk file is really a symlink pointing outside runs_base.
// Plain os.ReadFile would silently follow it.
func TestLoadRulePayloads_RejectsSymlinkedCandidateBody(t *testing.T) {
	root := t.TempDir()

	// Real attacker target outside runs/.
	attackerTarget := filepath.Join(root, "attacker", "secret.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(attackerTarget), 0o755))
	attackerBody := []byte("ATTACKER CONTENT\n")
	require.NoError(t, os.WriteFile(attackerTarget, attackerBody, 0o600))

	// Set up a runs-layout-shaped directory for the test.
	runDir := filepath.Join(root, "runs", "2026-04-21-PR42-abcdef0")
	fortyDir := filepath.Join(runDir, "40", "candidates")
	require.NoError(t, os.MkdirAll(fortyDir, 0o755))

	// Write a symlink at the expected candidate body path.
	symlinkPath := filepath.Join(fortyDir, "cand-001.md")
	require.NoError(t, os.Symlink(attackerTarget, symlinkPath))

	// Build a candidates.json whose sha256 matches the target's content
	// (so the only remaining line of defence is the symlink check).
	sum := sha256.Sum256(attackerBody)
	cands := contracts.Candidates{
		SchemaVersion: "1",
		RunID:         contracts.RunID("2026-04-21-PR42-abcdef0"),
		Candidates: []contracts.Candidate{{
			CandidateID:        "cand-001",
			Kind:               contracts.CandidateKindNew,
			TargetRuleID:       "",
			Title:              "demo",
			Problem:            "demo problem",
			Rationale:          "demo rationale",
			ProposedBodyPath:   "40/candidates/cand-001.md",
			ProposedBodySha256: hex.EncodeToString(sum[:]),
		}},
		CreatedAt: time.Now().UTC(),
	}
	cands.CandidatesHash = contracts.CanonicalCandidatesHash(cands.Candidates)

	candPath := filepath.Join(runDir, "40", "candidates.json")
	require.NoError(t, internalio.WriteJSONAtomic(candPath, cands))

	_, err := LoadRulePayloads(candPath)
	require.Error(t, err, "symlinked candidate body must be rejected")
	require.True(t, errors.Is(err, internalio.ErrNotRegularFile),
		"expected ErrNotRegularFile, got: %v", err)
}

// TestResumeState_ActiveLeaseRequiresLeaderStartTime covers M6 for step50.
// Mirrors the step20 test.
func TestResumeState_ActiveLeaseRequiresLeaderStartTime(t *testing.T) {
	now := time.Now().UTC()
	state := resumeState{
		ExpectedBaseSHA: "0000000000000000000000000000000000000000",
		StartedAt:       now,
		Pid:             42,
		Pgid:            42,
		LeaderStartTime: "",
		LastHeartbeat:   now,
	}
	err := state.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "leader_start_time")
	state.LeaderStartTime = "12345"
	require.NoError(t, state.Validate())
}

// TestStep_ZeroValueDoesNotPanicOnRun covers L2. Step{} with the cfg/now
// fields only partially-filled would nil-deref s.runner. After the fix,
// newStep is always re-invoked to populate every zero field.
func TestStep_ZeroValueDoesNotPanicOnRun(t *testing.T) {
	// Step{cfg:nil, now:nil, ...} triggers the bail-out path on run.Config
	// being nil — but before the L2 fix this check wasn't even reached
	// because step.runner was nil and dispatch panicked. Here we force a
	// hit on the early config-guard path; the important thing is that Run
	// returns a plain error rather than panicking.
	s := Step{}
	var gotPanic interface{}
	func() {
		defer func() { gotPanic = recover() }()
		_ = s.Run(context.Background(), RunContext{
			Pass: passNumber,
			IO:   internalio.RunContext{},
		})
	}()
	assert.Nil(t, gotPanic, "Step{}.Run must not panic on zero value; got: %v", gotPanic)
}
