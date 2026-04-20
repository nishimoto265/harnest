package stepio_test

import (
	"encoding/json"
	"fmt"
	"reflect"
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
	resp := stepio.Step70Response{
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

func TestStep70Response_PublicBypassSurfaceRemoved(t *testing.T) {
	data := validStep70ResponseJSONExternal(t)

	var resp stepio.Step70Response
	require.NoError(t, json.Unmarshal(data, &resp))

	_, hasValidate := reflect.TypeOf(stepio.Step70Response{}).MethodByName("Validate")
	assert.False(t, hasValidate)
	_, hasUnmarshalJSON := reflect.TypeOf((*stepio.Step70Response)(nil)).MethodByName("UnmarshalJSON")
	assert.False(t, hasUnmarshalJSON)
}

func TestDecodeAndValidateStep70Response_RemainsPublicBoundary(t *testing.T) {
	data := validStep70ResponseJSONExternal(t)
	req := validStep70RequestExternal()

	got, err := stepio.DecodeAndValidateStep70Response(data, req)
	require.NoError(t, err)
	assert.Equal(t, req.TaskPackage.RunID, got.RunID)
}
