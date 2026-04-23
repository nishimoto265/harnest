package stepio

import (
	"encoding/json"
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
type step50ResponsePayload struct {
	RunID           contracts.RunID     `json:"run_id" validate:"required,run_id_fmt"`
	Pass            int                 `json:"pass" validate:"required,eq=2"` // 固定 2
	Results         []Step20AgentResult `json:"results"`
	RescueExhausted []RescueExhausted   `json:"rescue_exhausted,omitempty"`
}

// Step50Response is the opaque output envelope for step 50. Direct strict JSON
// decoding remains response-local only; request-bound access is enabled only
// through DecodeAndValidateStep50Response / NewStep50Response.
type Step50Response struct {
	payload             step50ResponsePayload
	requestBoundChecked bool
}

var ErrStep50ResponseNotBound = errors.New("stepio: step50: response is not bound to a request; use DecodeAndValidateStep50Response")

func (r *Step50Response) UnmarshalJSON(data []byte) error {
	var payload step50ResponsePayload
	if err := contracts.DecodeStrictJSON(data, &payload); err != nil {
		return err
	}
	if err := payload.validate(); err != nil {
		return err
	}
	*r = newStep50Response(payload, false)
	return nil
}

func (r Step50Response) Validate() error {
	if !r.requestBoundChecked {
		return ErrStep50ResponseNotBound
	}
	return r.payload.validate()
}

func (r Step50Response) MarshalJSON() ([]byte, error) {
	if err := r.requireBound(); err != nil {
		return nil, err
	}
	return json.Marshal(r.payload)
}

func (r Step50Response) RunID() (contracts.RunID, error) {
	if err := r.requireBound(); err != nil {
		return "", err
	}
	return r.payload.RunID, nil
}

func (r Step50Response) Pass() (int, error) {
	if err := r.requireBound(); err != nil {
		return 0, err
	}
	return r.payload.Pass, nil
}

func (r Step50Response) Results() ([]Step20AgentResult, error) {
	if err := r.requireBound(); err != nil {
		return nil, err
	}
	return cloneImplementationResults(r.payload.Results), nil
}

func (r Step50Response) RescueExhausted() ([]RescueExhausted, error) {
	if err := r.requireBound(); err != nil {
		return nil, err
	}
	return cloneRescueExhausted(r.payload.RescueExhausted), nil
}

func (r Step50Response) RequestBound() bool {
	return r.requestBoundChecked
}

func (r Step50Response) DecodedAndBound() bool {
	return r.RequestBound()
}

func (r Step50Response) requireBound() error {
	return r.Validate()
}

func (p step50ResponsePayload) validate() error {
	if err := validation.Instance().Struct(p); err != nil {
		return err
	}
	return validateImplementationResponse(p.RunID, p.Pass, p.Results, p.RescueExhausted)
}

func (p step50ResponsePayload) validateAgainstRequest(req Step50Request) error {
	if err := req.Validate(); err != nil {
		return err
	}
	if err := p.validate(); err != nil {
		return err
	}
	if err := validateImplementationPartition(p.Results, p.RescueExhausted, req.Agents); err != nil {
		return err
	}
	if p.RunID != req.TaskPackage.RunID {
		return fmt.Errorf("%w: response.run_id=%s request.run_id=%s", ErrResponseRunIDMismatch, p.RunID, req.TaskPackage.RunID)
	}
	return nil
}

func newStep50Response(payload step50ResponsePayload, requestBound bool) Step50Response {
	return Step50Response{
		payload: step50ResponsePayload{
			RunID:           payload.RunID,
			Pass:            payload.Pass,
			Results:         cloneImplementationResults(payload.Results),
			RescueExhausted: cloneRescueExhausted(payload.RescueExhausted),
		},
		requestBoundChecked: requestBound,
	}
}

// NewStep50Response constructs a request-bound step50 response for writers.
func NewStep50Response(results []Step20AgentResult, rescueExhausted []RescueExhausted, req Step50Request) (Step50Response, error) {
	payload := step50ResponsePayload{
		RunID:           req.TaskPackage.RunID,
		Pass:            2,
		Results:         cloneImplementationResults(results),
		RescueExhausted: cloneRescueExhausted(rescueExhausted),
	}
	if err := payload.validateAgainstRequest(req); err != nil {
		return Step50Response{}, err
	}
	return newStep50Response(payload, true), nil
}

// DecodeAndValidateStep50Response applies the response-local strict decode and
// then enforces the request-aware partition contract:
// results ∩ rescue_exhausted == ∅ and results ∪ rescue_exhausted == req.Agents.
func DecodeAndValidateStep50Response(data []byte, req Step50Request) (Step50Response, error) {
	var resp Step50Response
	if err := resp.UnmarshalJSON(data); err != nil {
		return Step50Response{}, err
	}
	if err := resp.payload.validateAgainstRequest(req); err != nil {
		return Step50Response{}, err
	}
	return newStep50Response(resp.payload, true), nil
}
