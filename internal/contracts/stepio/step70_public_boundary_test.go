package stepio_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	stepio "github.com/nishimoto265/auto-improve/internal/contracts/stepio"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func buildTaskPackageForStep70External() contracts.TaskPackage {
	pkg := contracts.TaskPackage{
		SchemaVersion:           "1",
		RunID:                   "2026-04-20-PR42-abcdef0",
		PR:                      42,
		Title:                   "fix: example",
		BaseSHA:                 "1111111111111111111111111111111111111111",
		BestBranch:              "auto-improve/best",
		ReconstructedTaskPrompt: "hello",
		Worktrees:               make([]contracts.WorktreeAllocation, 6),
		CreatedAt:               time.Now(),
	}
	agents := []contracts.AgentID{"a1", "a2", "a3", "a1", "a2", "a3"}
	for i := range pkg.Worktrees {
		pass := 1
		if i >= 3 {
			pass = 2
		}
		pkg.Worktrees[i] = contracts.WorktreeAllocation{
			Agent:   agents[i],
			Pass:    pass,
			Path:    fmt.Sprintf("/tmp/wt/pass%d-%s", pass, agents[i]),
			Branch:  fmt.Sprintf("b-pass%d-%s", pass, agents[i]),
			BaseSHA: "1111111111111111111111111111111111111111",
			HeadSHA: "1111111111111111111111111111111111111111",
		}
	}
	return pkg
}

func validCandidatesForStep70External() contracts.Candidates {
	items := []contracts.Candidate{{
		CandidateID:        "c1",
		Kind:               contracts.CandidateKindNew,
		Title:              "tighten validation",
		ProposedBodyPath:   "40/candidates/c1.md",
		ProposedBodySha256: "0000000000000000000000000000000000000000000000000000000000000009",
	}}
	return contracts.Candidates{
		SchemaVersion:  "1",
		RunID:          "2026-04-20-PR42-abcdef0",
		Candidates:     items,
		CandidatesHash: contracts.CanonicalCandidatesHash(items),
		CreatedAt:      time.Now(),
	}
}

func validStep70RequestExternal() stepio.Step70Request {
	return stepio.Step70Request{
		TaskPackage:  buildTaskPackageForStep70External(),
		Candidates:   validCandidatesForStep70External(),
		RegistryPath: "/tmp/runs/rules-registry.jsonl",
	}
}

func validStep70ResponseJSONExternal(t *testing.T) []byte {
	t.Helper()
	candidates := validCandidatesForStep70External()
	resp := struct {
		RunID    contracts.RunID    `json:"run_id"`
		Decision contracts.Decision `json:"decision"`
		Promoted bool               `json:"promoted"`
	}{
		RunID: "2026-04-20-PR42-abcdef0",
		Decision: contracts.Decision{
			Action: contracts.DecisionActionAdopt,
			Value: contracts.DecisionAdopt{
				Action:         contracts.DecisionActionAdopt,
				SchemaVersion:  "1",
				RunID:          "2026-04-20-PR42-abcdef0",
				IdempotencyKey: contracts.ComputeAdoptIdempotencyKey("2026-04-20-PR42-abcdef0", "2222222222222222222222222222222222222222", "1111111111111111111111111111111111111111", candidates.CandidatesHash),
				BestShaBefore:  "1111111111111111111111111111111111111111",
				TargetSha:      "2222222222222222222222222222222222222222",
				CandidatesHash: candidates.CandidatesHash,
				RegistryAppendResult: contracts.RegistryAppendResult{
					Offset: 0,
					Sha256: "0000000000000000000000000000000000000000000000000000000000000003",
				},
				DecidedAt: time.Now(),
			},
		},
		Promoted: true,
	}
	data, err := json.Marshal(resp)
	require.NoError(t, err)
	return data
}

func TestStep70Response_DirectJSONUnmarshal_RejectsMalformedPayloads(t *testing.T) {
	data := validStep70ResponseJSONExternal(t)

	tests := []struct {
		name string
		data []byte
	}{
		{
			name: "duplicate key",
			data: []byte(strings.Replace(string(data), `"promoted":true`, `"promoted":true,"promoted":false`, 1)),
		},
		{
			name: "unknown field",
			data: []byte(strings.Replace(string(data), `"promoted":true`, `"unexpected":true,"promoted":true`, 1)),
		},
		{
			name: "trailing token",
			data: append(append([]byte(nil), data...), []byte(`{"extra":true}`)...),
		},
		{
			name: "response-local invariant",
			data: []byte(strings.Replace(string(data), `"promoted":true`, `"promoted":false`, 1)),
		},
		{
			name: "response-local run_id mismatch",
			data: []byte(strings.Replace(string(data), `"run_id":"2026-04-20-PR42-abcdef0"`, `"run_id":"2026-04-21-PR42-abcdef0"`, 1)),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var resp stepio.Step70Response
			err := json.Unmarshal(tt.data, &resp)
			require.Error(t, err)
		})
	}
}

func TestStep70Response_DirectJSONUnmarshal_SucceedsButRemainsUnbound(t *testing.T) {
	data := validStep70ResponseJSONExternal(t)

	var resp stepio.Step70Response
	err := json.Unmarshal(data, &resp)
	require.NoError(t, err)
	assert.False(t, resp.RequestBound())
	assert.ErrorIs(t, resp.Validate(), stepio.ErrStep70ResponseNotBound)
}

func TestStep70Response_DirectJSONUnmarshal_UnboundAccessorsAndMarshalFail(t *testing.T) {
	data := validStep70ResponseJSONExternal(t)

	var resp stepio.Step70Response
	require.NoError(t, json.Unmarshal(data, &resp))
	assert.False(t, resp.RequestBound())

	_, err := resp.RunID()
	require.Error(t, err)
	assert.ErrorIs(t, err, stepio.ErrStep70ResponseNotBound)

	_, err = resp.Decision()
	require.Error(t, err)
	assert.ErrorIs(t, err, stepio.ErrStep70ResponseNotBound)

	_, err = resp.Promoted()
	require.Error(t, err)
	assert.ErrorIs(t, err, stepio.ErrStep70ResponseNotBound)

	_, err = resp.MarshalJSON()
	require.Error(t, err)
	assert.ErrorIs(t, err, stepio.ErrStep70ResponseNotBound)
}

func TestDecodeAndValidateStep70Response_BindsRequest(t *testing.T) {
	data := validStep70ResponseJSONExternal(t)
	req := validStep70RequestExternal()

	got, err := stepio.DecodeAndValidateStep70Response(data, req)
	require.NoError(t, err)
	runID, err := got.RunID()
	require.NoError(t, err)
	assert.Equal(t, req.TaskPackage.RunID, runID)
	assert.True(t, got.RequestBound())
	assert.NoError(t, got.Validate())
}

func TestDecodeAndValidateStep70Response_RejectsRequestMismatchEvenWhenDirectDecodeSucceeds(t *testing.T) {
	data := validStep70ResponseJSONExternal(t)

	var direct stepio.Step70Response
	require.NoError(t, json.Unmarshal(data, &direct))
	assert.False(t, direct.RequestBound())

	req := validStep70RequestExternal()
	req.Candidates.Candidates[0].Title = "different candidate set"
	req.Candidates.CandidatesHash = contracts.CanonicalCandidatesHash(req.Candidates.Candidates)

	_, err := stepio.DecodeAndValidateStep70Response(data, req)
	require.Error(t, err)
	assert.ErrorIs(t, err, stepio.ErrStep70AdoptCandidatesHashMismatch)
}

func TestDecodeAndValidateStep70Response_PublicGetterReturnsCopy(t *testing.T) {
	data := validStep70ResponseJSONExternal(t)
	req := validStep70RequestExternal()

	got, err := stepio.DecodeAndValidateStep70Response(data, req)
	require.NoError(t, err)

	decision, err := got.Decision()
	require.NoError(t, err)
	adopt, ok := decision.Value.(contracts.DecisionAdopt)
	require.True(t, ok)
	adopt.Action = contracts.DecisionActionReject
	decision.Action = contracts.DecisionActionReject
	decision.Value = adopt

	gotDecision, err := got.Decision()
	require.NoError(t, err)
	assert.Equal(t, contracts.DecisionActionAdopt, gotDecision.Action)
	assert.Equal(t, contracts.DecisionActionAdopt, gotDecision.Value.(contracts.DecisionAdopt).Action)

	promoted, err := got.Promoted()
	require.NoError(t, err)
	assert.True(t, promoted)
}
