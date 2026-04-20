package stepio

import (
	"errors"
	"fmt"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/validation"
)

// Step20Request is the input envelope for step 20 (implement pass1).
// io-contracts.md §step 20 / 50.
type Step20Request struct {
	TaskPackage contracts.TaskPackage `json:"task_package"`
	// Agents: step20 が並列起動する agent 一覧 (通常 a1 / a2 / a3)。
	Agents []contracts.AgentID `json:"agents" validate:"required,unique,dive,agent_id_fmt"`
	// TimeoutSeconds: agent 1 体あたりの wall-clock timeout。
	TimeoutSeconds int `json:"timeout_seconds" validate:"required,gt=0"`
}

// ErrStep20AgentPassMismatch is returned when the Agents slice does not match
// the pass-1 agent set in TaskPackage.Worktrees (subset + full coverage).
var ErrStep20AgentPassMismatch = errors.New("stepio: step20: agents do not match TaskPackage.Worktrees[pass=1]")

// Validate enforces tag-based validation + structural invariant:
// every Agent must appear in TaskPackage.Worktrees[pass==1] and together cover
// all pass-1 agent IDs. Embedded TaskPackage is validated via its own
// Validate() to enforce the 3×2 matrix invariant (finding #5), so subset-only
// Agents cannot pass by riding on a malformed TaskPackage (finding #7).
func (r Step20Request) Validate() error {
	if err := validation.Instance().Struct(r); err != nil {
		return err
	}
	if err := r.TaskPackage.Validate(); err != nil {
		return err
	}
	return validateAgentsAgainstPass(r.Agents, r.TaskPackage, 1)
}

func (r *Step20Request) UnmarshalJSON(data []byte) error {
	type alias Step20Request
	var a alias
	if err := contracts.DecodeStrictJSON(data, &a); err != nil {
		return err
	}
	*r = Step20Request(a)
	return r.Validate()
}

// validateAgentsAgainstPass enforces: Agents set == set of worktrees[pass==p].Agent.
func validateAgentsAgainstPass(agents []contracts.AgentID, pkg contracts.TaskPackage, pass int) error {
	want := map[contracts.AgentID]struct{}{}
	for _, w := range pkg.Worktrees {
		if w.Pass == pass {
			want[w.Agent] = struct{}{}
		}
	}
	got := map[contracts.AgentID]struct{}{}
	for _, a := range agents {
		got[a] = struct{}{}
	}
	if len(got) != len(want) {
		return fmt.Errorf("%w: agents=%d worktrees(pass=%d)=%d", ErrStep20AgentPassMismatch, len(got), pass, len(want))
	}
	for a := range got {
		if _, ok := want[a]; !ok {
			return fmt.Errorf("%w: agent %s not present in worktrees(pass=%d)", ErrStep20AgentPassMismatch, a, pass)
		}
	}
	return nil
}

// Step20AgentResult: 各 agent ごとの実行結果。manifest は Manifest tagged union。
type Step20AgentResult struct {
	Agent    contracts.AgentID  `json:"agent" validate:"required,agent_id_fmt"`
	Manifest contracts.Manifest `json:"manifest"`
}

// Step20Response is the output envelope for step 20.
type Step20Response struct {
	RunID   contracts.RunID     `json:"run_id" validate:"required,run_id_fmt"`
	Pass    int                 `json:"pass" validate:"required,eq=1"` // 固定 1 だが共通 field として持つ
	Results []Step20AgentResult `json:"results"`
	// RescueExhausted: worktree rescue が retry 上限 (3) に達した agent 一覧。
	// orchestrator が processed.jsonl に needs_manual_recovery を append する契約
	// (step 自身は append しない、single-writer invariant 維持)。
	RescueExhausted []RescueExhausted `json:"rescue_exhausted,omitempty"`
}

// RescueExhausted: step20/50 agent の rescue 上限到達通知。
type RescueExhausted struct {
	Agent      contracts.AgentID `json:"agent" validate:"required,agent_id_fmt"`
	RetryCount int               `json:"retry_count" validate:"gte=3"`
}

func (r *Step20Response) UnmarshalJSON(data []byte) error {
	type alias Step20Response
	var a alias
	if err := contracts.DecodeStrictJSON(data, &a); err != nil {
		return err
	}
	*r = Step20Response(a)
	return r.Validate()
}

func (r Step20Response) Validate() error {
	if err := validation.Instance().Struct(r); err != nil {
		return err
	}
	return validateImplementationResponse(r.RunID, r.Pass, r.Results, r.RescueExhausted)
}

// DecodeAndValidateStep20Response applies the response-local strict decode and
// then enforces the request-aware partition contract:
// results ∩ rescue_exhausted == ∅ and results ∪ rescue_exhausted == req.Agents.
func DecodeAndValidateStep20Response(data []byte, req Step20Request) (Step20Response, error) {
	var resp Step20Response
	if err := resp.UnmarshalJSON(data); err != nil {
		return Step20Response{}, err
	}
	if err := req.Validate(); err != nil {
		return Step20Response{}, err
	}
	if err := validateImplementationPartition(resp.Results, resp.RescueExhausted, req.Agents); err != nil {
		return Step20Response{}, err
	}
	if resp.RunID != req.TaskPackage.RunID {
		return Step20Response{}, fmt.Errorf("%w: response.run_id=%s request.run_id=%s", ErrResponseRunIDMismatch, resp.RunID, req.TaskPackage.RunID)
	}
	return resp, nil
}
