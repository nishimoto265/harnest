package stepio

import (
	"encoding/json"
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
type step20ResponsePayload struct {
	RunID   contracts.RunID     `json:"run_id" validate:"required,run_id_fmt"`
	Pass    int                 `json:"pass" validate:"required,eq=1"` // 固定 1 だが共通 field として持つ
	Results []Step20AgentResult `json:"results"`
	// RescueExhausted: worktree rescue が retry 上限 (3) に達した agent 一覧。
	// orchestrator が processed.jsonl に needs_manual_recovery を append する契約
	// (step 自身は append しない、single-writer invariant 維持)。
	RescueExhausted []RescueExhausted `json:"rescue_exhausted,omitempty"`
}

// Step20Response is the opaque output envelope for step 20. Direct strict JSON
// decoding still enforces response-local invariants, but request-bound access is
// only enabled through DecodeAndValidateStep20Response / NewStep20Response.
type Step20Response struct {
	payload             step20ResponsePayload
	requestBoundChecked bool
}

var ErrStep20ResponseNotBound = errors.New("stepio: step20: response is not bound to a request; use DecodeAndValidateStep20Response")

// RescueExhausted: step20/50 agent の rescue 上限到達通知。
type RescueExhausted struct {
	Agent      contracts.AgentID `json:"agent" validate:"required,agent_id_fmt"`
	RetryCount int               `json:"retry_count" validate:"gte=3"`
}

func (r *Step20Response) UnmarshalJSON(data []byte) error {
	var payload step20ResponsePayload
	if err := contracts.DecodeStrictJSON(data, &payload); err != nil {
		return err
	}
	if err := payload.validate(); err != nil {
		return err
	}
	*r = newStep20Response(payload, false)
	return nil
}

func (r Step20Response) Validate() error {
	if !r.requestBoundChecked {
		return ErrStep20ResponseNotBound
	}
	return r.payload.validate()
}

func (r Step20Response) MarshalJSON() ([]byte, error) {
	if err := r.requireBound(); err != nil {
		return nil, err
	}
	return json.Marshal(r.payload)
}

func (r Step20Response) RunID() (contracts.RunID, error) {
	if err := r.requireBound(); err != nil {
		return "", err
	}
	return r.payload.RunID, nil
}

func (r Step20Response) Pass() (int, error) {
	if err := r.requireBound(); err != nil {
		return 0, err
	}
	return r.payload.Pass, nil
}

func (r Step20Response) Results() ([]Step20AgentResult, error) {
	if err := r.requireBound(); err != nil {
		return nil, err
	}
	return cloneImplementationResults(r.payload.Results), nil
}

func (r Step20Response) RescueExhausted() ([]RescueExhausted, error) {
	if err := r.requireBound(); err != nil {
		return nil, err
	}
	return cloneRescueExhausted(r.payload.RescueExhausted), nil
}

func (r Step20Response) RequestBound() bool {
	return r.requestBoundChecked
}

func (r Step20Response) DecodedAndBound() bool {
	return r.RequestBound()
}

func (r Step20Response) requireBound() error {
	return r.Validate()
}

func (p step20ResponsePayload) validate() error {
	if err := validation.Instance().Struct(p); err != nil {
		return err
	}
	return validateImplementationResponse(p.RunID, p.Pass, p.Results, p.RescueExhausted)
}

func (p step20ResponsePayload) validateAgainstRequest(req Step20Request) error {
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

func newStep20Response(payload step20ResponsePayload, requestBound bool) Step20Response {
	return Step20Response{
		payload: step20ResponsePayload{
			RunID:           payload.RunID,
			Pass:            payload.Pass,
			Results:         cloneImplementationResults(payload.Results),
			RescueExhausted: cloneRescueExhausted(payload.RescueExhausted),
		},
		requestBoundChecked: requestBound,
	}
}

// NewStep20Response constructs a request-bound step20 response for writers.
func NewStep20Response(results []Step20AgentResult, rescueExhausted []RescueExhausted, req Step20Request) (Step20Response, error) {
	payload := step20ResponsePayload{
		RunID:           req.TaskPackage.RunID,
		Pass:            1,
		Results:         cloneImplementationResults(results),
		RescueExhausted: cloneRescueExhausted(rescueExhausted),
	}
	if err := payload.validateAgainstRequest(req); err != nil {
		return Step20Response{}, err
	}
	return newStep20Response(payload, true), nil
}

// DecodeAndValidateStep20Response applies the response-local strict decode and
// then enforces the request-aware partition contract:
// results ∩ rescue_exhausted == ∅ and results ∪ rescue_exhausted == req.Agents.
func DecodeAndValidateStep20Response(data []byte, req Step20Request) (Step20Response, error) {
	var resp Step20Response
	if err := resp.UnmarshalJSON(data); err != nil {
		return Step20Response{}, err
	}
	if err := resp.payload.validateAgainstRequest(req); err != nil {
		return Step20Response{}, err
	}
	return newStep20Response(resp.payload, true), nil
}
