package stepio

import (
	"errors"
	"fmt"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/validation"
)

// Step50Request is the input envelope for step 50 (implement pass2).
// io-contracts.md §step 20 / 50。step20 と同形だが候補ルール適用が加わる。
type Step50Request struct {
	TaskPackage    contracts.TaskPackage `json:"task_package"`
	Agents         []contracts.AgentID   `json:"agents" validate:"required,unique,dive,agent_id_fmt"`
	TimeoutSeconds int                   `json:"timeout_seconds" validate:"required,gt=0"`
	// CandidateRuleIDs: best 設定に加えて pass2 で適用する候補 rule_id 群。
	CandidateRuleIDs []string `json:"candidate_rule_ids" validate:"required,min=1,dive,required"`
}

// ErrStep50AgentPassMismatch mirrors ErrStep20AgentPassMismatch for pass=2.
var ErrStep50AgentPassMismatch = errors.New("stepio: step50: agents do not match TaskPackage.Worktrees[pass=2]")

// Validate enforces tag-based validation + pass-2 agent set-equality invariant.
// Embedded TaskPackage is validated via its own Validate() so the 3×2 matrix
// (finding #5) guarantees set-equality is not trivially satisfied by a subset
// (finding #7).
func (r Step50Request) Validate() error {
	if err := validation.Instance().Struct(r); err != nil {
		return err
	}
	if err := r.TaskPackage.Validate(); err != nil {
		return err
	}
	if err := validateAgentsAgainstPass(r.Agents, r.TaskPackage, 2); err != nil {
		// Rewrap to step50-specific sentinel for caller discrimination.
		if errors.Is(err, ErrStep20AgentPassMismatch) {
			return errors.Join(ErrStep50AgentPassMismatch, err)
		}
		return err
	}
	return nil
}

func (r *Step50Request) UnmarshalJSON(data []byte) error {
	type alias Step50Request
	var a alias
	if err := contracts.DecodeStrictJSON(data, &a); err != nil {
		return err
	}
	*r = Step50Request(a)
	return r.Validate()
}

// Step50Response is the output envelope for step 50 (mirrors Step20Response).
type Step50Response struct {
	RunID           contracts.RunID     `json:"run_id" validate:"required,run_id_fmt"`
	Pass            int                 `json:"pass" validate:"required,eq=2"` // 固定 2
	Results         []Step20AgentResult `json:"results"`
	RescueExhausted []RescueExhausted   `json:"rescue_exhausted,omitempty"`
}

func (r *Step50Response) UnmarshalJSON(data []byte) error {
	type alias Step50Response
	var a alias
	if err := contracts.DecodeStrictJSON(data, &a); err != nil {
		return err
	}
	*r = Step50Response(a)
	return r.Validate()
}

func (r Step50Response) Validate() error {
	if err := validation.Instance().Struct(r); err != nil {
		return err
	}
	return validateImplementationResponse(r.RunID, r.Pass, r.Results, r.RescueExhausted)
}

// DecodeAndValidateStep50Response applies the response-local strict decode and
// then enforces the request-aware partition contract:
// results ∩ rescue_exhausted == ∅ and results ∪ rescue_exhausted == req.Agents.
func DecodeAndValidateStep50Response(data []byte, req Step50Request) (Step50Response, error) {
	var resp Step50Response
	if err := resp.UnmarshalJSON(data); err != nil {
		return Step50Response{}, err
	}
	if err := req.Validate(); err != nil {
		return Step50Response{}, err
	}
	if err := validateImplementationPartition(resp.Results, resp.RescueExhausted, req.Agents); err != nil {
		return Step50Response{}, err
	}
	if resp.RunID != req.TaskPackage.RunID {
		return Step50Response{}, fmt.Errorf("%w: response.run_id=%s request.run_id=%s", ErrResponseRunIDMismatch, resp.RunID, req.TaskPackage.RunID)
	}
	return resp, nil
}
