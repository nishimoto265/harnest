package step50_implement

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadRulePayloadsRejectsSymlinkedCandidateBody(t *testing.T) {
	const passwdPath = "/etc/passwd"

	passwdData, err := os.ReadFile(passwdPath)
	if err != nil {
		t.Skipf("passwd fixture unavailable: %v", err)
	}
	firstLine := strings.SplitN(string(passwdData), "\n", 2)[0]

	runsBase := filepath.Join(t.TempDir(), "runs")
	worktreeBase := filepath.Join(t.TempDir(), "worktrees")
	runCtx, err := internalio.NewRunContext("2026-04-21-PR42-abcdef0", runsBase, worktreeBase)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(runCtx.RunDir(), 0o755))

	bodyPath, err := runCtx.ResolveRunRelative("40/candidates/loot.md")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(bodyPath), 0o755))
	require.NoError(t, os.Symlink(passwdPath, bodyPath))

	candidate := contracts.Candidate{
		CandidateID:        "loot",
		Kind:               contracts.CandidateKindNew,
		Title:              "Loot",
		ProposedBodyPath:   "40/candidates/loot.md",
		ProposedBodySha256: strings.Repeat("a", 64),
	}
	candidates := contracts.Candidates{
		SchemaVersion:  "1",
		RunID:          runCtx.RunID,
		Candidates:     []contracts.Candidate{candidate},
		CandidatesHash: contracts.CanonicalCandidatesHash([]contracts.Candidate{candidate}),
		CreatedAt:      time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
	}
	candidatesPath, err := runCtx.ResolveRunRelative("40/candidates.json")
	require.NoError(t, err)
	require.NoError(t, internalio.WriteJSONAtomic(candidatesPath, candidates))

	_, err = LoadRulePayloads(candidatesPath)
	require.Error(t, err)
	assert.ErrorIs(t, err, internalio.ErrUnsafePath)
	assert.NotContains(t, err.Error(), firstLine)
}

func TestLoadRulePayloadsRejectsOversizedCandidateBody(t *testing.T) {
	runsBase := filepath.Join(t.TempDir(), "runs")
	worktreeBase := filepath.Join(t.TempDir(), "worktrees")
	runCtx, err := internalio.NewRunContext("2026-04-21-PR43-abcdef0", runsBase, worktreeBase)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(runCtx.RunDir(), 0o755))

	bodyPath, err := runCtx.ResolveRunRelative("40/candidates/large.md")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(bodyPath), 0o755))
	file, err := os.OpenFile(bodyPath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o644)
	require.NoError(t, err)
	require.NoError(t, file.Truncate(50*1024*1024))
	require.NoError(t, file.Close())

	candidate := contracts.Candidate{
		CandidateID:        "cand-large",
		Kind:               contracts.CandidateKindNew,
		Title:              "Large",
		ProposedBodyPath:   "40/candidates/large.md",
		ProposedBodySha256: strings.Repeat("a", 64),
	}
	candidates := contracts.Candidates{
		SchemaVersion:  "1",
		RunID:          runCtx.RunID,
		Candidates:     []contracts.Candidate{candidate},
		CandidatesHash: contracts.CanonicalCandidatesHash([]contracts.Candidate{candidate}),
		CreatedAt:      time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
	}
	candidatesPath, err := runCtx.ResolveRunRelative("40/candidates.json")
	require.NoError(t, err)
	require.NoError(t, internalio.WriteJSONAtomic(candidatesPath, candidates))

	_, err = LoadRulePayloads(candidatesPath)
	require.ErrorIs(t, err, internalio.ErrFileTooLarge)
}

func TestValidatePromptIdentifier_RejectsNewlines(t *testing.T) {
	err := validatePromptIdentifier("candidate_id", "cand-1\n- kind: update")
	require.Error(t, err)
}
