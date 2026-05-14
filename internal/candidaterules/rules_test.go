package candidaterules

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
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

	runCtx := newTestRunContext(t, "2026-04-21-PR42-abcdef0")
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
	candidatesPath := writeCandidatesFile(t, runCtx, []contracts.Candidate{candidate})

	_, err = LoadRulePayloads(candidatesPath)
	require.Error(t, err)
	assert.ErrorIs(t, err, internalio.ErrUnsafePath)
	assert.NotContains(t, err.Error(), firstLine)
}

func TestLoadRulePayloadsRejectsOversizedCandidateBody(t *testing.T) {
	runCtx := newTestRunContext(t, "2026-04-21-PR43-abcdef0")
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
	candidatesPath := writeCandidatesFile(t, runCtx, []contracts.Candidate{candidate})

	_, err = LoadRulePayloads(candidatesPath)
	require.ErrorIs(t, err, internalio.ErrFileTooLarge)
}

func TestLoadRulePayloadsRejectsPathTraversal(t *testing.T) {
	runCtx := newTestRunContext(t, "2026-04-21-PR44-abcdef0")
	bodyPath, err := runCtx.ResolveRunRelative("40/candidates/good.md")
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
			candidatesPath := writeCandidatesFileAllowInvalidTarget(t, runCtx, []contracts.Candidate{tt.candidate})
			_, err := LoadRulePayloads(candidatesPath)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestLoadRulePayloads_SkipsDuplicateCandidates(t *testing.T) {
	runCtx := newTestRunContext(t, "2026-04-21-PR45-abcdef0")
	candidateNew := writeCandidateSidecar(t, runCtx, contracts.Candidate{
		CandidateID:      "cand-new",
		Kind:             contracts.CandidateKindNew,
		Title:            "New rule",
		ProposedBodyPath: "40/candidates/cand-new.md",
	}, "# cand-new\nnew body\n")
	candidateDuplicate := writeCandidateSidecar(t, runCtx, contracts.Candidate{
		CandidateID:      "cand-dup",
		Kind:             contracts.CandidateKindDuplicate,
		TargetRuleID:     "rule-v1",
		Title:            "Duplicate rule",
		ProposedBodyPath: "40/candidates/cand-dup.md",
	}, "# cand-dup\nduplicate body\n")
	candidatesPath := writeCandidatesFile(t, runCtx, []contracts.Candidate{candidateNew, candidateDuplicate})

	payloads, err := LoadRulePayloads(candidatesPath)
	require.NoError(t, err)
	require.Len(t, payloads, 1)
	assert.Equal(t, "cand-new", payloads[0].ID)
}

func TestLoadRulePayloads_AllowsValidatedRuleID(t *testing.T) {
	runCtx := newTestRunContext(t, "2026-04-21-PR46-abcdef0")
	candidate := writeCandidateSidecar(t, runCtx, contracts.Candidate{
		CandidateID:      "cand-1",
		Kind:             contracts.CandidateKindUpdate,
		TargetRuleID:     "rule-v1",
		Title:            "Updated rule",
		ProposedBodyPath: "40/candidates/cand-1.md",
	}, "# cand-1\nupdated body\n")
	candidatesPath := writeCandidatesFile(t, runCtx, []contracts.Candidate{candidate})

	payloads, err := LoadRulePayloads(candidatesPath)
	require.NoError(t, err)
	require.Len(t, payloads, 1)
	assert.Equal(t, "rule-v1", payloads[0].TargetRuleID)
}

func TestValidatePromptIdentifier_RejectsNewlines(t *testing.T) {
	err := validatePromptIdentifier("candidate_id", "cand-1\n- kind: update")
	require.Error(t, err)
}

func TestToJudgeRulesMapsPromptPayloadFields(t *testing.T) {
	rules := ToJudgeRules([]RulePayload{{
		ID:           "cand-1",
		Kind:         "update",
		TargetRuleID: "rule-v1",
		Title:        "Improve rule",
		ProposedBody: "body\n",
	}})

	require.Len(t, rules, 1)
	assert.Equal(t, "cand-1", rules[0].ID)
	assert.Equal(t, "update", rules[0].Kind)
	assert.Equal(t, "rule-v1", rules[0].TargetRuleID)
	assert.Equal(t, "Improve rule", rules[0].Title)
	assert.Equal(t, "body\n", rules[0].Body)
}

func newTestRunContext(t *testing.T, runID string) internalio.RunContext {
	t.Helper()
	runsBase := filepath.Join(t.TempDir(), "runs")
	worktreeBase := filepath.Join(t.TempDir(), "worktrees")
	runCtx, err := internalio.NewRunContext(contracts.RunID(runID), runsBase, worktreeBase)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(runCtx.RunDir(), 0o755))
	return runCtx
}

func writeCandidatesFile(t *testing.T, runIO internalio.RunContext, candidates []contracts.Candidate) string {
	t.Helper()
	candidatesPath, err := runIO.ResolveRunRelative("40/candidates.json")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(candidatesPath), 0o755))
	doc := contracts.Candidates{
		SchemaVersion:  "1",
		RunID:          runIO.RunID,
		Candidates:     candidates,
		CandidatesHash: contracts.CanonicalCandidatesHash(candidates),
		CreatedAt:      time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
	}
	require.NoError(t, internalio.WriteJSONAtomic(candidatesPath, doc))
	return candidatesPath
}

func writeCandidatesFileAllowInvalidTarget(t *testing.T, runIO internalio.RunContext, candidates []contracts.Candidate) string {
	t.Helper()
	candidatesPath, err := runIO.ResolveRunRelative("40/candidates.json")
	require.NoError(t, err)
	if len(candidates) != 1 || candidates[0].TargetRuleID == "" {
		return writeCandidatesFile(t, runIO, candidates)
	}
	require.NoError(t, os.MkdirAll(filepath.Dir(candidatesPath), 0o755))
	body := `{"schema_version":"1","run_id":"` + string(runIO.RunID) + `","candidates":[{"candidate_id":"` + candidates[0].CandidateID + `","kind":"` + string(candidates[0].Kind) + `","target_rule_id":"` + candidates[0].TargetRuleID + `","title":"` + candidates[0].Title + `","proposed_body_path":"` + candidates[0].ProposedBodyPath + `","proposed_body_sha256":"` + candidates[0].ProposedBodySha256 + `"}],"candidates_hash":"` + contracts.CanonicalCandidatesHash(candidates) + `","created_at":"2026-04-21T00:00:00Z"}`
	require.NoError(t, os.WriteFile(candidatesPath, []byte(body), 0o644))
	return candidatesPath
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
