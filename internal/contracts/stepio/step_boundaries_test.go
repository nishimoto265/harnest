package stepio

import (
	"strings"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validStep10Request() Step10Request {
	return Step10Request{
		PR:           42,
		BestBranch:   "auto-improve/best",
		HarnessFiles: true,
	}
}

func validStep10Response() Step10Response {
	pkg := buildTaskPackage()
	return Step10Response{
		RunID:            pkg.RunID,
		TaskPackage:      pkg,
		BaseSHA:          pkg.BaseSHA,
		WorktreesCreated: 6,
	}
}

func validManifestSuccess(pass int, agent contracts.AgentID) contracts.Manifest {
	return contracts.Manifest{
		Kind: contracts.ManifestKindSuccess,
		Value: contracts.ManifestSuccess{
			Kind:          contracts.ManifestKindSuccess,
			SchemaVersion: "1",
			RunID:         "2026-04-20-PR42-abcdef0",
			Pass:          pass,
			Agent:         agent,
			BranchName:    "branch-" + string(agent),
			HeadSHA:       "2222222222222222222222222222222222222222",
			BaseSHA:       "1111111111111111111111111111111111111111",
			DiffPath:      "20-pass1/" + string(agent) + "/diff.patch",
			SessionPath:   "20-pass1/" + string(agent) + "/session.jsonl",
			ChecklistPath: "20-pass1/" + string(agent) + "/checklist-result.json",
			PromptVersion: "p1",
			StartedAt:     time.Now(),
			FinishedAt:    time.Now(),
		},
	}
}

func validStep20Response() Step20Response {
	return Step20Response{
		RunID: "2026-04-20-PR42-abcdef0",
		Pass:  1,
		Results: []Step20AgentResult{{
			Agent:    "a1",
			Manifest: validManifestSuccess(1, "a1"),
		}},
	}
}

func validStep20Request() Step20Request {
	return Step20Request{
		TaskPackage:    buildTaskPackage(),
		Agents:         []contracts.AgentID{"a1", "a2", "a3"},
		TimeoutSeconds: 600,
	}
}

func validStep30Request() Step30Request {
	return Step30Request{
		TaskPackage:    buildTaskPackage(),
		ScorableAgents: []contracts.AgentID{"a1", "a2"},
		RubricVersion:  "rubric-v1",
		PromptVersion:  "prompt-v1",
	}
}

func validStep30Response() Step30Response {
	return Step30Response{
		RunID:           "2026-04-20-PR42-abcdef0",
		ScoresCount:     10,
		ComplianceCount: 2,
		ResolvedAt:      time.Now(),
	}
}

func validStep40Request() Step40Request {
	return Step40Request{
		TaskPackage:  buildTaskPackage(),
		RegistryPath: "/tmp/runs/rules-registry.jsonl",
	}
}

func validStep40Response() Step40Response {
	candidates := validCandidates()
	return Step40Response{
		RunID:           candidates.RunID,
		Candidates:      candidates,
		CandidatesCount: len(candidates.Candidates),
	}
}

func validStep50Request() Step50Request {
	return Step50Request{
		TaskPackage:      buildTaskPackage(),
		Agents:           []contracts.AgentID{"a1", "a2", "a3"},
		TimeoutSeconds:   600,
		CandidateRuleIDs: []string{"r-1"},
	}
}

func validStep50Response() Step50Response {
	return Step50Response{
		RunID: "2026-04-20-PR42-abcdef0",
		Pass:  2,
		Results: []Step20AgentResult{{
			Agent:    "a1",
			Manifest: validManifestSuccess(2, "a1"),
		}},
	}
}

func validStep60Request() Step60Request {
	return Step60Request{
		TaskPackage:    buildTaskPackage(),
		ScorableAgents: []contracts.AgentID{"a1", "a3"},
		RubricVersion:  "rubric-v1",
		PromptVersion:  "prompt-v1",
	}
}

func validStep60Response() Step60Response {
	return Step60Response{
		RunID:           "2026-04-20-PR42-abcdef0",
		ScoresCount:     10,
		ComplianceCount: 2,
		PairwiseCount:   1,
		ResolvedAt:      time.Now(),
	}
}

func replaceJSONFragment(t *testing.T, data []byte, old, new string) []byte {
	t.Helper()
	raw := string(data)
	require.Contains(t, raw, old)
	return []byte(strings.Replace(raw, old, new, 1))
}

func assertJSONBoundaryFailures(t *testing.T, valid []byte, unmarshal func([]byte) error, anchor, duplicate, remove string) {
	t.Helper()

	err := unmarshal(replaceJSONFragment(t, valid, anchor, duplicate))
	require.Error(t, err)
	assert.ErrorIs(t, err, contracts.ErrDuplicateJSONKey)

	err = unmarshal(replaceJSONFragment(t, valid, anchor, `"unexpected":true,`+anchor))
	require.Error(t, err)

	err = unmarshal(append(valid, []byte(`{"extra":true}`)...))
	require.Error(t, err)
	assert.ErrorIs(t, err, contracts.ErrTrailingJSON)

	err = unmarshal(replaceJSONFragment(t, valid, remove, ""))
	require.Error(t, err)
}

func TestStepIO_JSONBoundaries_StrictRequestsAndResponses(t *testing.T) {
	tests := []struct {
		name      string
		valid     []byte
		unmarshal func([]byte) error
		anchor    string
		duplicate string
		remove    string
	}{
		{
			name:  "step10 request",
			valid: mustMarshalJSON(t, validStep10Request()),
			unmarshal: func(data []byte) error {
				var v Step10Request
				return v.UnmarshalJSON(data)
			},
			anchor:    `"best_branch":"auto-improve/best"`,
			duplicate: `"best_branch":"auto-improve/best","best_branch":"other-branch"`,
			remove:    `"best_branch":"auto-improve/best",`,
		},
		{
			name:  "step10 response",
			valid: mustMarshalJSON(t, validStep10Response()),
			unmarshal: func(data []byte) error {
				var v Step10Response
				return v.UnmarshalJSON(data)
			},
			anchor:    `"worktrees_created":6`,
			duplicate: `"worktrees_created":6,"worktrees_created":0`,
			remove:    `"run_id":"2026-04-20-PR42-abcdef0",`,
		},
		{
			name:  "step20 request",
			valid: mustMarshalJSON(t, validStep20Request()),
			unmarshal: func(data []byte) error {
				var v Step20Request
				return v.UnmarshalJSON(data)
			},
			anchor:    `"timeout_seconds":600`,
			duplicate: `"timeout_seconds":600,"timeout_seconds":1`,
			remove:    `,"timeout_seconds":600`,
		},
		{
			name:  "step20 response",
			valid: mustMarshalJSON(t, validStep20Response()),
			unmarshal: func(data []byte) error {
				var v Step20Response
				return v.UnmarshalJSON(data)
			},
			anchor:    `"pass":1`,
			duplicate: `"pass":1,"pass":1`,
			remove:    `"run_id":"2026-04-20-PR42-abcdef0",`,
		},
		{
			name:  "step30 request",
			valid: mustMarshalJSON(t, validStep30Request()),
			unmarshal: func(data []byte) error {
				var v Step30Request
				return v.UnmarshalJSON(data)
			},
			anchor:    `"rubric_version":"rubric-v1"`,
			duplicate: `"rubric_version":"rubric-v1","rubric_version":"rubric-v2"`,
			remove:    `"rubric_version":"rubric-v1",`,
		},
		{
			name:  "step30 response",
			valid: mustMarshalJSON(t, validStep30Response()),
			unmarshal: func(data []byte) error {
				var v Step30Response
				return v.UnmarshalJSON(data)
			},
			anchor:    `"scores_count":10`,
			duplicate: `"scores_count":10,"scores_count":9`,
			remove:    `"run_id":"2026-04-20-PR42-abcdef0",`,
		},
		{
			name:  "step40 request",
			valid: mustMarshalJSON(t, validStep40Request()),
			unmarshal: func(data []byte) error {
				var v Step40Request
				return v.UnmarshalJSON(data)
			},
			anchor:    `"registry_path":"/tmp/runs/rules-registry.jsonl"`,
			duplicate: `"registry_path":"/tmp/runs/rules-registry.jsonl","registry_path":"/tmp/other.jsonl"`,
			remove:    `,"registry_path":"/tmp/runs/rules-registry.jsonl"`,
		},
		{
			name:  "step40 response",
			valid: mustMarshalJSON(t, validStep40Response()),
			unmarshal: func(data []byte) error {
				var v Step40Response
				return v.UnmarshalJSON(data)
			},
			anchor:    `"candidates_count":1`,
			duplicate: `"candidates_count":1,"candidates_count":2`,
			remove:    `"run_id":"2026-04-20-PR42-abcdef0",`,
		},
		{
			name:  "step50 request",
			valid: mustMarshalJSON(t, validStep50Request()),
			unmarshal: func(data []byte) error {
				var v Step50Request
				return v.UnmarshalJSON(data)
			},
			anchor:    `"timeout_seconds":600`,
			duplicate: `"timeout_seconds":600,"timeout_seconds":1`,
			remove:    `,"candidate_rule_ids":["r-1"]`,
		},
		{
			name:  "step50 response",
			valid: mustMarshalJSON(t, validStep50Response()),
			unmarshal: func(data []byte) error {
				var v Step50Response
				return v.UnmarshalJSON(data)
			},
			anchor:    `"pass":2`,
			duplicate: `"pass":2,"pass":2`,
			remove:    `"run_id":"2026-04-20-PR42-abcdef0",`,
		},
		{
			name:  "step60 request",
			valid: mustMarshalJSON(t, validStep60Request()),
			unmarshal: func(data []byte) error {
				var v Step60Request
				return v.UnmarshalJSON(data)
			},
			anchor:    `"prompt_version":"prompt-v1"`,
			duplicate: `"prompt_version":"prompt-v1","prompt_version":"prompt-v2"`,
			remove:    `,"prompt_version":"prompt-v1"`,
		},
		{
			name:  "step60 response",
			valid: mustMarshalJSON(t, validStep60Response()),
			unmarshal: func(data []byte) error {
				var v Step60Response
				return v.UnmarshalJSON(data)
			},
			anchor:    `"pairwise_count":1`,
			duplicate: `"pairwise_count":1,"pairwise_count":2`,
			remove:    `"run_id":"2026-04-20-PR42-abcdef0",`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertJSONBoundaryFailures(t, tt.valid, tt.unmarshal, tt.anchor, tt.duplicate, tt.remove)
		})
	}
}

func TestStep10Response_Validate_RejectsTaskPackageMismatch(t *testing.T) {
	resp := validStep10Response()
	resp.BaseSHA = "ffffffffffffffffffffffffffffffffffffffff"

	err := resp.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStep10ResponseBaseSHAMismatch)
}

func TestStep20Response_Validate_RejectsManifestAgentMismatch(t *testing.T) {
	resp := validStep20Response()
	resp.Results[0].Manifest = validManifestSuccess(1, "a2")

	err := resp.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStepResponseManifestAgentMismatch)
}

func TestStep30Request_Validate_RejectsScorableAgentOutsidePass(t *testing.T) {
	req := validStep30Request()
	req.ScorableAgents = []contracts.AgentID{"a4"}

	err := req.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStep30ScorableAgentPassMismatch)
}

func TestStep40Response_Validate_RejectsCandidatesCountMismatch(t *testing.T) {
	resp := validStep40Response()
	resp.CandidatesCount++

	err := resp.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStep40CandidatesCountMismatch)
}

func TestStep40Request_Validate_RejectsRelativeRegistryPath(t *testing.T) {
	req := validStep40Request()
	req.RegistryPath = "tmp/runs/rules-registry.jsonl"

	err := req.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRegistryPathNotAbsolute)
}

func TestStep50Response_Validate_RejectsManifestPassMismatch(t *testing.T) {
	resp := validStep50Response()
	resp.Results[0].Manifest = validManifestSuccess(1, "a1")

	err := resp.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStepResponseManifestPassMismatch)
}

func TestStep60Request_Validate_RejectsScorableAgentOutsidePass(t *testing.T) {
	req := validStep60Request()
	req.ScorableAgents = []contracts.AgentID{"a4"}

	err := req.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStep60ScorableAgentPassMismatch)
}

func TestDecodeAndValidateStep20Response_RejectsAgentOverlap(t *testing.T) {
	req := validStep20Request()
	resp := Step20Response{
		RunID: "2026-04-20-PR42-abcdef0",
		Pass:  1,
		Results: []Step20AgentResult{
			{Agent: "a1", Manifest: validManifestSuccess(1, "a1")},
			{Agent: "a2", Manifest: validManifestSuccess(1, "a2")},
		},
		RescueExhausted: []RescueExhausted{
			{Agent: "a2", RetryCount: 3},
			{Agent: "a3", RetryCount: 3},
		},
	}

	_, err := DecodeAndValidateStep20Response(mustMarshalJSON(t, resp), req)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAgentResultOverlap)
}

func TestDecodeAndValidateStep20Response_RejectsCoverageMismatch(t *testing.T) {
	req := validStep20Request()
	resp := Step20Response{
		RunID: "2026-04-20-PR42-abcdef0",
		Pass:  1,
		Results: []Step20AgentResult{
			{Agent: "a1", Manifest: validManifestSuccess(1, "a1")},
		},
		RescueExhausted: []RescueExhausted{
			{Agent: "a2", RetryCount: 3},
		},
	}

	_, err := DecodeAndValidateStep20Response(mustMarshalJSON(t, resp), req)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAgentCoverageMismatch)
}

func TestDecodeAndValidateStep20Response_AcceptsExactPartition(t *testing.T) {
	req := validStep20Request()
	resp := Step20Response{
		RunID: "2026-04-20-PR42-abcdef0",
		Pass:  1,
		Results: []Step20AgentResult{
			{Agent: "a1", Manifest: validManifestSuccess(1, "a1")},
			{Agent: "a2", Manifest: validManifestSuccess(1, "a2")},
		},
		RescueExhausted: []RescueExhausted{
			{Agent: "a3", RetryCount: 3},
		},
	}

	got, err := DecodeAndValidateStep20Response(mustMarshalJSON(t, resp), req)
	require.NoError(t, err)
	assert.Equal(t, resp.RunID, got.RunID)
}

func TestDecodeAndValidateStep20Response_RejectsCrossRunReplay(t *testing.T) {
	req := validStep20Request()
	req.TaskPackage.RunID = "2026-04-21-PR42-abcdef0"
	resp := Step20Response{
		RunID: "2026-04-20-PR42-abcdef0",
		Pass:  1,
		Results: []Step20AgentResult{
			{Agent: "a1", Manifest: validManifestSuccess(1, "a1")},
			{Agent: "a2", Manifest: validManifestSuccess(1, "a2")},
		},
		RescueExhausted: []RescueExhausted{
			{Agent: "a3", RetryCount: 3},
		},
	}

	_, err := DecodeAndValidateStep20Response(mustMarshalJSON(t, resp), req)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrResponseRunIDMismatch)
}

func TestDecodeAndValidateStep50Response_RejectsCoverageMismatchOnInjectedAgent(t *testing.T) {
	req := validStep50Request()
	resp := Step50Response{
		RunID: "2026-04-20-PR42-abcdef0",
		Pass:  2,
		Results: []Step20AgentResult{
			{Agent: "a1", Manifest: validManifestSuccess(2, "a1")},
			{Agent: "a2", Manifest: validManifestSuccess(2, "a2")},
		},
		RescueExhausted: []RescueExhausted{
			{Agent: "a4", RetryCount: 3},
		},
	}

	_, err := DecodeAndValidateStep50Response(mustMarshalJSON(t, resp), req)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAgentCoverageMismatch)
}

func TestDecodeAndValidateStep50Response_AcceptsExactPartition(t *testing.T) {
	req := validStep50Request()
	resp := Step50Response{
		RunID: "2026-04-20-PR42-abcdef0",
		Pass:  2,
		Results: []Step20AgentResult{
			{Agent: "a1", Manifest: validManifestSuccess(2, "a1")},
			{Agent: "a3", Manifest: validManifestSuccess(2, "a3")},
		},
		RescueExhausted: []RescueExhausted{
			{Agent: "a2", RetryCount: 3},
		},
	}

	got, err := DecodeAndValidateStep50Response(mustMarshalJSON(t, resp), req)
	require.NoError(t, err)
	assert.Equal(t, resp.RunID, got.RunID)
}

func TestDecodeAndValidateStep50Response_RejectsCrossRunReplay(t *testing.T) {
	req := validStep50Request()
	req.TaskPackage.RunID = "2026-04-21-PR42-abcdef0"
	resp := Step50Response{
		RunID: "2026-04-20-PR42-abcdef0",
		Pass:  2,
		Results: []Step20AgentResult{
			{Agent: "a1", Manifest: validManifestSuccess(2, "a1")},
			{Agent: "a3", Manifest: validManifestSuccess(2, "a3")},
		},
		RescueExhausted: []RescueExhausted{
			{Agent: "a2", RetryCount: 3},
		},
	}

	_, err := DecodeAndValidateStep50Response(mustMarshalJSON(t, resp), req)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrResponseRunIDMismatch)
}
