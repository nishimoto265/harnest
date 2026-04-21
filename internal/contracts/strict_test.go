package contracts

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type nilValidatingValue struct{}

func (*nilValidatingValue) Validate() error { return nil }

// Phase 0-bootstrap-1 gate 3rd-round findings #1 and #2: EncodeStrict /
// MarshalStrict run Validate() / validator.Struct before emitting JSON, so
// producers that hand-craft a struct and call the writer cannot bypass
// contract-level invariants. Symmetric with decodeStrict auto-chain.

// #1: Manifest (top-level persisted) — writer enforces variant validation.
func TestEncodeStrict_Manifest_RejectsInvalidVariant(t *testing.T) {
	// ManifestSuccess missing required HeadSHA / BaseSHA → tag validation
	// must reject at write time.
	m := Manifest{
		Kind: ManifestKindSuccess,
		Value: ManifestSuccess{
			Kind:          ManifestKindSuccess,
			SchemaVersion: "1",
			RunID:         "2026-04-20-PR42-abcdef0",
			Pass:          1,
			Agent:         "a1",
			// BranchName / HeadSHA / BaseSHA / DiffPath / SessionPath /
			// ChecklistPath / PromptVersion / StartedAt / FinishedAt all missing.
		},
	}
	var buf bytes.Buffer
	err := EncodeStrict(&buf, m)
	assert.Error(t, err, "EncodeStrict must reject ManifestSuccess with missing required fields")
	assert.Equal(t, 0, buf.Len(), "buffer must not be written on validation failure")
}

func TestMarshalStrict_Manifest_RejectsInvalidVariant(t *testing.T) {
	m := Manifest{
		Kind: ManifestKindSuccess,
		Value: ManifestSuccess{
			Kind:          ManifestKindSuccess,
			SchemaVersion: "1",
			RunID:         "not-a-valid-run-id",
			Pass:          1,
			Agent:         "a1",
			BranchName:    "x",
			HeadSHA:       "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			BaseSHA:       "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			DiffPath:      "d",
			SessionPath:   "s",
			ChecklistPath: "c",
			PromptVersion: "v",
			StartedAt:     time.Now(),
			FinishedAt:    time.Now(),
		},
	}
	data, err := MarshalStrict(m)
	assert.Error(t, err, "MarshalStrict must reject invalid RunID format")
	assert.Nil(t, data)
}

// #1: TaskPackage (top-level persisted) — writer enforces the 3×2 matrix
// invariant (via Validate()).
func TestEncodeStrict_TaskPackage_RejectsMatrixViolation(t *testing.T) {
	pkg := validTaskPackage()
	// Break the pass1/pass2 agent set equality.
	pkg.Worktrees[3].Agent = "a4"
	pkg.Worktrees[4].Agent = "a5"
	pkg.Worktrees[5].Agent = "a6"
	var buf bytes.Buffer
	err := EncodeStrict(&buf, pkg)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTaskPackagePassAgentMismatch)
}

func TestMarshalStrict_TaskPackage_AcceptsValid(t *testing.T) {
	pkg := validTaskPackage()
	data, err := MarshalStrict(pkg)
	require.NoError(t, err)
	assert.True(t, bytes.Contains(data, []byte(`"schema_version":"1"`)))
}

// #1: IntentionRecord (step70-persisted) — writer enforces stage-conditional
// required fields via Validate().
func TestEncodeStrict_IntentionRecord_RejectsMissingRegistryAppendResult(t *testing.T) {
	r := validIntentionBase()
	r.Stage = IntentionStageRegistryAppended
	// RegistryAppendResult left nil → Validate() must fail on encode.
	var buf bytes.Buffer
	err := EncodeStrict(&buf, r)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrIntentionMissingRegistryAppendResult)
}

func TestEncodeStrict_IntentionRecord_AcceptsValidPlanning(t *testing.T) {
	r := validIntentionBase()
	r.Stage = IntentionStagePlanning
	var buf bytes.Buffer
	assert.NoError(t, EncodeStrict(&buf, r))
	assert.Contains(t, buf.String(), `"stage":"planning"`)
}

// #2: RuleRegistryStatusChanged — writer rejects invalid transitions
// (decode-time auto-chain already covered; this proves producers cannot
// bypass by going straight to MarshalStrict).
func TestMarshalStrict_RuleRegistryStatusChanged_RejectsInvalidTransition(t *testing.T) {
	e := RuleRegistryStatusChanged{
		Kind:          RegistryKindStatusChanged,
		SchemaVersion: "1",
		RuleID:        "r-0001",
		PrevStatus:    RuleStatusActive,
		NewStatus:     RuleStatusArchived, // active→archived is forbidden via status_changed
		Transition:    SunsetTransitionArchive,
		OpID:          "0000000000000000000000000000000000000000000000000000000000000050",
		VersionSeq:    4,
		PrevHash:      "0000000000000000000000000000000000000000000000000000000000000077",
		BySunsetRunID: "sunset-2026-04-22",
		At:            time.Now(),
	}
	_, err := MarshalStrict(e)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRegistryStatusChangedInvalidTransition)
}

// EncodeStrict of a direct-Marshal struct that has extra keys is symmetric
// with the decodeStrict failure on unknown keys. We cannot produce extra
// keys from json.Marshal on a typed struct, but we can at least confirm
// a valid round-trip does not regress.
func TestEncodeStrict_RuleRegistryAdded_ValidRoundTrip(t *testing.T) {
	e := RuleRegistryAdded{
		Kind:           RegistryKindAdded,
		SchemaVersion:  "1",
		RuleID:         "r-0001",
		RulePath:       "rules/r-0001.md",
		Sha256:         "0000000000000000000000000000000000000000000000000000000000000001",
		IdempotencyKey: "0000000000000000000000000000000000000000000000000000000000000002",
		VersionSeq:     1,
		PrevHash:       "",
		ByRunID:        "2026-04-20-PR42-abcdef0",
		At:             time.Now(),
	}
	data, err := MarshalStrict(e)
	require.NoError(t, err)
	assert.True(t, bytes.Contains(data, []byte(`"kind":"added"`)))
}

// #6: Candidate.Validate rejects kind=update/duplicate without TargetRuleID.
func TestCandidate_Validate_UpdateRequiresTarget(t *testing.T) {
	c := Candidate{
		CandidateID:        "c1",
		Kind:               CandidateKindUpdate,
		TargetRuleID:       "", // missing
		Title:              "x",
		ProposedBodyPath:   "40/candidates/c1.md",
		ProposedBodySha256: "0000000000000000000000000000000000000000000000000000000000000001",
	}
	err := c.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrCandidateTargetRequired)
}

func TestCandidate_Validate_DuplicateRequiresTarget(t *testing.T) {
	c := Candidate{
		CandidateID:        "c1",
		Kind:               CandidateKindDuplicate,
		TargetRuleID:       "",
		Title:              "x",
		ProposedBodyPath:   "40/candidates/c1.md",
		ProposedBodySha256: "0000000000000000000000000000000000000000000000000000000000000001",
	}
	err := c.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrCandidateTargetRequired)
}

// #6: Candidate.Validate rejects kind=new with non-empty TargetRuleID.
func TestCandidate_Validate_NewForbidsTarget(t *testing.T) {
	c := Candidate{
		CandidateID:        "c1",
		Kind:               CandidateKindNew,
		TargetRuleID:       "r-existing",
		Title:              "x",
		ProposedBodyPath:   "40/candidates/c1.md",
		ProposedBodySha256: "0000000000000000000000000000000000000000000000000000000000000001",
	}
	err := c.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrCandidateTargetForbidden)
}

func TestCandidate_Validate_NewAcceptsEmptyTarget(t *testing.T) {
	c := Candidate{
		CandidateID:        "c1",
		Kind:               CandidateKindNew,
		Title:              "x",
		ProposedBodyPath:   "40/candidates/c1.md",
		ProposedBodySha256: "0000000000000000000000000000000000000000000000000000000000000001",
	}
	assert.NoError(t, c.Validate())
}

func TestCandidate_Validate_UpdateAcceptsTarget(t *testing.T) {
	c := Candidate{
		CandidateID:        "c1",
		Kind:               CandidateKindUpdate,
		TargetRuleID:       "r-0001",
		Title:              "x",
		ProposedBodyPath:   "40/candidates/c1.md",
		ProposedBodySha256: "0000000000000000000000000000000000000000000000000000000000000001",
	}
	assert.NoError(t, c.Validate())
}

// #6: Candidates.Validate propagates per-Candidate failures with an index
// prefix so callers can localize the offending row.
func TestCandidates_Validate_PropagatesCandidateError(t *testing.T) {
	cs := Candidates{
		SchemaVersion: "1",
		RunID:         "2026-04-20-PR42-abcdef0",
		Candidates: []Candidate{
			{
				CandidateID:        "c1",
				Kind:               CandidateKindUpdate,
				TargetRuleID:       "", // missing
				Title:              "x",
				ProposedBodyPath:   "40/candidates/c1.md",
				ProposedBodySha256: "0000000000000000000000000000000000000000000000000000000000000001",
			},
		},
		CandidatesHash: "0000000000000000000000000000000000000000000000000000000000000002",
		CreatedAt:      time.Now(),
	}
	err := cs.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrCandidateTargetRequired)
	assert.True(t, strings.Contains(err.Error(), "candidates[0]"))
}

func TestCandidate_Validate_RejectsOverflowRefOutside40Prefix(t *testing.T) {
	c := Candidate{
		CandidateID: "c1",
		Kind:        CandidateKindNew,
		Title:       "x",
		ProblemOverflowRef: &OverflowRef{
			Path:   "30/reasons/problem.txt",
			Sha256: "0000000000000000000000000000000000000000000000000000000000000001",
		},
		ProposedBodyPath:   "40/candidates/c1.md",
		ProposedBodySha256: "0000000000000000000000000000000000000000000000000000000000000002",
	}
	err := c.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrOverflowRefPathPrefixMismatch)
}

func TestCandidate_Validate_RejectsAbsoluteBodyPath(t *testing.T) {
	c := Candidate{
		CandidateID:        "c1",
		Kind:               CandidateKindNew,
		Title:              "x",
		ProposedBodyPath:   "/tmp/c1.md",
		ProposedBodySha256: "0000000000000000000000000000000000000000000000000000000000000001",
	}
	err := c.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrCandidateBodyPathInvalid)
	assert.ErrorIs(t, err, ErrPathRelativeAbsolute)
}

func TestCandidates_VerifyCandidatesHash_RoundTripAndTamper(t *testing.T) {
	cs := Candidates{
		SchemaVersion: "1",
		RunID:         "2026-04-20-PR42-abcdef0",
		Candidates: []Candidate{
			{
				CandidateID:        "c1",
				Kind:               CandidateKindNew,
				Title:              "x",
				ProposedBodyPath:   "40/candidates/c1.md",
				ProposedBodySha256: "0000000000000000000000000000000000000000000000000000000000000001",
			},
		},
		CreatedAt: time.Now(),
	}
	cs.CandidatesHash = CanonicalCandidatesHash(cs.Candidates)
	require.NoError(t, cs.VerifyCandidatesHash())

	mutated := cs
	mutated.Candidates[0].Title = "changed"
	err := mutated.VerifyCandidatesHash()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrCandidatesHashMismatch)

	tampered := cs
	tampered.CandidatesHash = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	err = tampered.VerifyCandidatesHash()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrCandidatesHashMismatch)
}

func TestCandidates_UnmarshalJSON_RejectsDuplicateTopLevelKey(t *testing.T) {
	data := []byte(`{
  "schema_version": "1",
  "run_id": "2026-04-20-PR42-abcdef0",
  "run_id": "2026-04-21-PR42-abcdef0",
  "candidates": [],
  "candidates_hash": "4f53cda18c2baa0c0354bb5f9a3ecbe5edc3d5f9d9f54a2e4f3b68d5c4d6f6f8",
  "created_at": "2026-04-20T12:00:00Z"
}`)
	var cs Candidates
	err := json.Unmarshal(data, &cs)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDuplicateJSONKey)
}

func TestCandidates_UnmarshalJSON_RejectsDuplicateItemKey(t *testing.T) {
	data := []byte(`{
  "schema_version": "1",
  "run_id": "2026-04-20-PR42-abcdef0",
  "candidates": [
    {
      "candidate_id": "c1",
      "candidate_id": "c1-dup",
      "kind": "new",
      "title": "title",
      "proposed_body_path": "40/candidates/c1.md",
      "proposed_body_sha256": "0000000000000000000000000000000000000000000000000000000000000001"
    }
  ],
  "candidates_hash": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
  "created_at": "2026-04-20T12:00:00Z"
}`)
	var cs Candidates
	err := json.Unmarshal(data, &cs)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDuplicateJSONKey)
}

func TestCandidates_UnmarshalJSON_RejectsDuplicateNestedStructKey(t *testing.T) {
	data := []byte(`{
  "schema_version": "1",
  "run_id": "2026-04-20-PR42-abcdef0",
  "candidates": [
    {
      "candidate_id": "c1",
      "kind": "new",
      "title": "title",
      "problem_overflow_ref": {
        "path": "40/problems/c1.txt",
        "sha256": "0000000000000000000000000000000000000000000000000000000000000002",
        "sha256": "0000000000000000000000000000000000000000000000000000000000000003"
      },
      "proposed_body_path": "40/candidates/c1.md",
      "proposed_body_sha256": "0000000000000000000000000000000000000000000000000000000000000001"
    }
  ],
  "candidates_hash": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
  "created_at": "2026-04-20T12:00:00Z"
}`)
	var cs Candidates
	err := json.Unmarshal(data, &cs)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDuplicateJSONKey)
}

// #4: TaskPackage.Validate rejects duplicate worktree paths.
func TestTaskPackage_Validate_RejectsDuplicatePath(t *testing.T) {
	pkg := validTaskPackage()
	pkg.Worktrees[0].Path = pkg.Worktrees[1].Path
	err := pkg.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTaskPackageDuplicatePath)
}

// #4: TaskPackage.Validate rejects duplicate worktree branches.
func TestTaskPackage_Validate_RejectsDuplicateBranch(t *testing.T) {
	pkg := validTaskPackage()
	pkg.Worktrees[0].Branch = pkg.Worktrees[1].Branch
	err := pkg.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTaskPackageDuplicateBranch)
}

// #4: Cross-pass duplicate path (same Path in pass1 and pass2) also rejected.
func TestTaskPackage_Validate_RejectsCrossPassDuplicatePath(t *testing.T) {
	pkg := validTaskPackage()
	pkg.Worktrees[3].Path = pkg.Worktrees[0].Path // pass2-a1 shares pass1-a1's path
	err := pkg.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTaskPackageDuplicatePath)
}

func TestTaskPackage_Validate_RejectsCrossPassDuplicateBranch(t *testing.T) {
	pkg := validTaskPackage()
	pkg.Worktrees[3].Branch = pkg.Worktrees[0].Branch
	err := pkg.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTaskPackageDuplicateBranch)
}

// #3: decodeStrict auto-chains IntentionRecord.Validate so stage-conditional
// required fields are enforced even from the read path. Callers must use
// decodeStrict (or readers that wrap it) instead of plain json.Unmarshal.
func TestDecodeStrict_IntentionRecord_EnforcesStageInvariant(t *testing.T) {
	// stage=registry_appended but missing registry_append_result.
	candidatesHash := "0000000000000000000000000000000000000000000000000000000000000002"
	idempotencyKey := ComputeAdoptIdempotencyKey("2026-04-20-PR42-abcdef0", "2222222222222222222222222222222222222222", "1111111111111111111111111111111111111111", candidatesHash)
	data := []byte(`{
  "schema_version": "1",
  "stage": "registry_appended",
  "idempotency_key": "` + idempotencyKey + `",
  "run_id": "2026-04-20-PR42-abcdef0",
  "best_sha_before": "1111111111111111111111111111111111111111",
  "target_sha": "2222222222222222222222222222222222222222",
  "candidates_hash": "` + candidatesHash + `",
  "registry_head_before": "",
  "planned_adoption": {
    "idempotency_key": "` + idempotencyKey + `",
    "entries": [
      {
        "kind": "added",
        "op_id": "` + ComputePlannedAdoptionEntryOpID(idempotencyKey, 0, "r-0001") + `",
        "rule_id": "r-0001",
        "rule_path": "rules/r-0001.md",
        "sha256": "0000000000000000000000000000000000000000000000000000000000000005"
      }
    ]
  },
  "started_at": "2026-04-20T10:00:00Z"
}`)
	var r IntentionRecord
	err := decodeStrict(data, &r)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrIntentionMissingRegistryAppendResult)
}

// #2: Direct-Marshal of a struct that would fail Validate() is still
// accepted by json.Marshal (by design — that is the Codex-flagged hole),
// but MarshalStrict catches it before the JSON is persisted. This test
// pins that difference so future refactors can't collapse the two.
func TestMarshalStrict_vs_json_Marshal_StatusChanged(t *testing.T) {
	// Construct an invalid status_changed struct directly.
	e := RuleRegistryStatusChanged{
		Kind:          RegistryKindStatusChanged,
		SchemaVersion: "1",
		RuleID:        "r-0001",
		PrevStatus:    RuleStatusActive,
		NewStatus:     RuleStatusArchived, // illegal for status_changed
		Transition:    SunsetTransitionArchive,
		OpID:          "0000000000000000000000000000000000000000000000000000000000000050",
		VersionSeq:    4,
		PrevHash:      "0000000000000000000000000000000000000000000000000000000000000077",
		BySunsetRunID: "sunset-2026-04-22",
		At:            time.Now(),
	}
	// MarshalStrict must reject.
	_, err := MarshalStrict(e)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRegistryStatusChangedInvalidTransition))
}

func TestDecodeStrict_RejectsDuplicateTopLevelKey(t *testing.T) {
	data := []byte(`{"kind":"started","kind":"step_done","pr":42,"run_id":"2026-04-20-PR42-abcdef0","step":"10","at":"2026-04-20T10:00:00Z"}`)
	var e StateEntry
	err := json.Unmarshal(data, &e)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDuplicateJSONKey)
}

func TestDecodeStrict_RejectsDuplicateNestedRegistryAppendResultKey(t *testing.T) {
	data := []byte(`{
  "action":"adopt",
  "schema_version":"1",
  "run_id":"2026-04-20-PR42-abcdef0",
  "idempotency_key":"0000000000000000000000000000000000000000000000000000000000000001",
  "best_sha_before":"1111111111111111111111111111111111111111",
  "target_sha":"2222222222222222222222222222222222222222",
  "candidates_hash":"0000000000000000000000000000000000000000000000000000000000000002",
  "registry_append_result":{"offset":0,"offset":1,"sha256":"0000000000000000000000000000000000000000000000000000000000000003"},
  "decided_at":"2026-04-20T12:00:00Z"
}`)
	var d Decision
	err := json.Unmarshal(data, &d)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDuplicateJSONKey)
}

func TestDecodeStrict_RejectsDuplicateNestedOverflowRefKey(t *testing.T) {
	data := []byte(`{
  "schema_version":"1",
  "run_id":"2026-04-20-PR42-abcdef0",
  "pass":1,
  "agent":"a1",
  "judge_role":"primary",
  "dimension":"fidelity",
  "score":95,
  "reasons":"ok",
  "reasons_overflow_ref":{"path":"30/reasons/x.txt","sha256":"0000000000000000000000000000000000000000000000000000000000000004","sha256":"0000000000000000000000000000000000000000000000000000000000000005"},
  "output_sha256":"0000000000000000000000000000000000000000000000000000000000000006",
  "rubric_version":"v1",
  "prompt_version":"p1",
  "resolved_at":"2026-04-20T12:00:00Z"
}`)
	var row RawScoreEntry
	err := decodeStrict(data, &row)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDuplicateJSONKey)
}

func TestDecodeStrict_RejectsEmptyPayloadsWithTypedError(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{name: "empty", data: nil},
		{name: "whitespace", data: []byte("  \n\t  ")},
		{name: "bom only", data: []byte{0xEF, 0xBB, 0xBF}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out map[string]any
			err := decodeStrict(tt.data, &out)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrEmptyJSON)
		})
	}
}

func TestCustomUnmarshalJSON_RejectsDuplicateDiscriminatorKeys(t *testing.T) {
	tests := []struct {
		name      string
		data      []byte
		unmarshal func([]byte) error
	}{
		{
			name: "manifest kind",
			data: []byte(`{"kind":"success","kind":"error","schema_version":"1","run_id":"2026-04-20-PR42-abcdef0","pass":1,"agent":"a1","branch_name":"b","head_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","base_sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","diff_path":"20-pass1/a1/diff.patch","session_path":"20-pass1/a1/session.jsonl","checklist_path":"20-pass1/a1/checklist-result.json","prompt_version":"v1","started_at":"2026-04-20T10:00:00Z","finished_at":"2026-04-20T10:01:00Z"}`),
			unmarshal: func(data []byte) error {
				var v Manifest
				return v.UnmarshalJSON(data)
			},
		},
		{
			name: "decision action",
			data: []byte(`{"action":"adopt","action":"reject","schema_version":"1","run_id":"2026-04-20-PR42-abcdef0","reason":"below_threshold","decided_at":"2026-04-20T12:00:00Z"}`),
			unmarshal: func(data []byte) error {
				var v Decision
				return v.UnmarshalJSON(data)
			},
		},
		{
			name: "registry kind",
			data: []byte(`{"kind":"added","kind":"updated","schema_version":"1","rule_id":"r-0001","rule_path":"rules/r-0001.md","sha256":"0000000000000000000000000000000000000000000000000000000000000001","prev_sha256":"0000000000000000000000000000000000000000000000000000000000000002","idempotency_key":"0000000000000000000000000000000000000000000000000000000000000003","version_seq":2,"prev_hash":"0000000000000000000000000000000000000000000000000000000000000004","by_run_id":"2026-04-20-PR42-abcdef0","at":"2026-04-20T12:00:00Z"}`),
			unmarshal: func(data []byte) error {
				var v RuleRegistryEntry
				return v.UnmarshalJSON(data)
			},
		},
		{
			name: "state kind",
			data: []byte(`{"kind":"started","kind":"step_done","pr":42,"run_id":"2026-04-20-PR42-abcdef0","step":"10","at":"2026-04-20T10:00:00Z"}`),
			unmarshal: func(data []byte) error {
				var v StateEntry
				return v.UnmarshalJSON(data)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.unmarshal(tt.data)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrDuplicateJSONKey)
		})
	}
}

func TestCustomUnmarshalJSON_RejectsEmptyAndTrailingPayloads(t *testing.T) {
	tests := []struct {
		name      string
		unmarshal func([]byte) error
		valid     []byte
	}{
		{
			name: "manifest",
			unmarshal: func(data []byte) error {
				var v Manifest
				return v.UnmarshalJSON(data)
			},
			valid: []byte(fixtureManifestSuccess(t)),
		},
		{
			name: "decision",
			unmarshal: func(data []byte) error {
				var v Decision
				return v.UnmarshalJSON(data)
			},
			valid: []byte(fixtureDecisionAdopt()),
		},
		{
			name: "registry",
			unmarshal: func(data []byte) error {
				var v RuleRegistryEntry
				return v.UnmarshalJSON(data)
			},
			valid: []byte(`{"kind":"added","schema_version":"1","rule_id":"r-0001","rule_path":"rules/r-0001.md","sha256":"0000000000000000000000000000000000000000000000000000000000000001","idempotency_key":"0000000000000000000000000000000000000000000000000000000000000002","version_seq":1,"by_run_id":"2026-04-20-PR42-abcdef0","at":"2026-04-20T12:00:00Z"}`),
		},
		{
			name: "state",
			unmarshal: func(data []byte) error {
				var v StateEntry
				return v.UnmarshalJSON(data)
			},
			valid: []byte(fixtureStateStarted()),
		},
		{
			name: "classification",
			unmarshal: func(data []byte) error {
				var v ClassificationEntry
				return v.UnmarshalJSON(data)
			},
			valid: []byte(`{"schema_version":"1","run_id":"2026-04-20-PR42-abcdef0","candidate_id":"c1","kind":"new","similarity_score":0,"classified_at":"2026-04-20T12:00:00Z"}`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name+" empty", func(t *testing.T) {
			err := tt.unmarshal(nil)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrEmptyJSON)
		})
		t.Run(tt.name+" trailing", func(t *testing.T) {
			err := tt.unmarshal(append(append([]byte(nil), tt.valid...), []byte(`{"extra":true}`)...))
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrTrailingJSON)
		})
	}
}

func TestRunValidation_RejectsTypedNilPointer(t *testing.T) {
	var value *nilValidatingValue
	err := runValidation(value)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNilValidationValue)
}
