package stepio

import (
	"errors"
	"fmt"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/validation"
)

// Step70Request is the input envelope for step 70 (decide + apply).
// io-contracts.md §step 70。`<runs_base>/promotion.lock` は step 実装側で
// acquire/release する契約で、orchestrator は lock 状態を意識しない。
//
// `best_sha_before` は step70 が promotion.lock 取得後に自身で remote
// `best_branch` を読む契約 (orchestrator が pre-read した値は渡さない)。
// `candidates_hash` は Candidates.CandidatesHash が source of truth のため
// 重複させない。
type Step70Request struct {
	TaskPackage contracts.TaskPackage `json:"task_package"`
	// Candidates: step40 の出力を読み込み済みで渡す。
	// Candidates.CandidatesHash が intention.IdempotencyKey 計算の source of truth。
	Candidates contracts.Candidates `json:"candidates"`
	// RegistryPath: `<runs_base>/rules-registry.jsonl` の絶対 path。
	RegistryPath string `json:"registry_path" validate:"required"`
}

// Step70Response is the output envelope for step 70.
// Decision は `<run>/70/decision.json` に atomic write 済みで渡す前提。
//
// Direct stdlib decode must still fail closed on malformed payloads because
// this is an exported struct with exported fields. UnmarshalJSON therefore
// enforces strict JSON + response-local invariants and leaves the value marked
// as "not yet request-bound". Callers that need to consume the response as a
// real step70 boundary must use DecodeAndValidateStep70Response(data, req),
// which performs the second-stage request binding and flips DecodedAndBound().
type Step70Response struct {
	RunID    contracts.RunID    `json:"run_id" validate:"required,run_id_fmt"`
	Decision contracts.Decision `json:"decision"`
	// Promoted: Decision.Action == adopt かつ best_branch push + registry append
	// まで完走した場合のみ true。orchestrator は true のときに promoted event を
	// state に append 可 (step70 自身が既に append 済みなので重複 append しない)。
	Promoted bool `json:"promoted"`

	requestBoundChecked bool
}

// Step70Response consistency errors (Phase 0-bootstrap-1 gate 3rd-round
// finding #5). Promoted and Decision.Action are not independent — they must
// agree on whether the promotion succeeded. Allowed combinations:
//   - Action == adopt    → Promoted == true  (success path)
//   - Action == reject   → Promoted == false
//   - Action == noop     → Promoted == false
//   - Action == rollback → Promoted == false (adopt aborted / rolled back)
//
// Any other combination is a contract violation on the write path and would
// mislead the orchestrator into double-appending `promoted` / contradicting
// the persisted decision.json.
var (
	ErrStep70PromotedRequiresAdopt        = errors.New("stepio: step70: promoted=true requires Decision.Action=adopt")
	ErrStep70AdoptRequiresPromoted        = errors.New("stepio: step70: Decision.Action=adopt requires promoted=true")
	ErrStep70RollbackMustNotPromote       = errors.New("stepio: step70: Decision.Action=rollback must have promoted=false")
	ErrStep70RejectMustNotPromote         = errors.New("stepio: step70: Decision.Action=reject must have promoted=false")
	ErrStep70NoopMustNotPromote           = errors.New("stepio: step70: Decision.Action=noop must have promoted=false")
	ErrStep70DecisionMissing              = errors.New("stepio: step70: Decision.Value must be populated")
	ErrStep70RequestRunIDMismatch         = errors.New("stepio: step70: task_package.run_id must equal candidates.run_id")
	ErrStep70ResponseRunIDMismatch        = errors.New("stepio: step70: response.run_id must equal decision.run_id")
	ErrStep70AdoptIdempotencyKeyMismatch  = errors.New("stepio: step70: adopt idempotency_key does not match derived value")
	ErrStep70AdoptCandidatesHashMismatch  = errors.New("stepio: step70: adopt candidates_hash must match request.candidates_hash")
	ErrStep70RequestResponseRunIDMismatch = errors.New("stepio: step70: response.run_id must equal request.run_id")
)

func (r *Step70Request) UnmarshalJSON(data []byte) error {
	type alias Step70Request
	var a alias
	if err := contracts.DecodeStrictJSON(data, &a); err != nil {
		return err
	}
	*r = Step70Request(a)
	return r.Validate()
}

func (r Step70Request) Validate() error {
	if err := validation.Instance().Struct(r); err != nil {
		return err
	}
	if err := r.TaskPackage.Validate(); err != nil {
		return err
	}
	if err := r.Candidates.Validate(); err != nil {
		return err
	}
	if err := r.Candidates.VerifyCandidatesHash(); err != nil {
		return err
	}
	if r.TaskPackage.RunID != r.Candidates.RunID {
		return fmt.Errorf("%w: task_package.run_id=%s candidates.run_id=%s", ErrStep70RequestRunIDMismatch, r.TaskPackage.RunID, r.Candidates.RunID)
	}
	return nil
}

// DecodeAndValidateStep70Response is the sanctioned read boundary for a step70
// response when the originating request is available. It enforces strict JSON
// decode, response-local invariants, and request-bound cross-checks in one
// place so callers cannot accidentally skip the request-aware validation.
func DecodeAndValidateStep70Response(data []byte, req Step70Request) (Step70Response, error) {
	var resp Step70Response
	if err := resp.UnmarshalJSON(data); err != nil {
		return Step70Response{}, err
	}
	if err := resp.validateAgainstRequest(req); err != nil {
		return Step70Response{}, err
	}
	resp.requestBoundChecked = true
	return resp, nil
}

func (r *Step70Response) UnmarshalJSON(data []byte) error {
	type alias Step70Response
	var a alias
	if err := contracts.DecodeStrictJSON(data, &a); err != nil {
		return err
	}
	*r = Step70Response(a)
	r.requestBoundChecked = false
	if err := r.Validate(); err != nil {
		return err
	}
	return nil
}

func (r Step70Response) Validate() error {
	return r.validate()
}

// DecodedAndBound reports whether DecodeAndValidateStep70Response completed the
// request-bound second-stage validation for this value.
func (r Step70Response) DecodedAndBound() bool {
	return r.requestBoundChecked
}

func (r Step70Response) validate() error {
	if err := validation.Instance().Var(r.RunID, "required,run_id_fmt"); err != nil {
		return err
	}
	if r.Decision.Value == nil {
		return ErrStep70DecisionMissing
	}
	if err := r.Decision.Validate(); err != nil {
		return err
	}
	_, _, decisionRunID, err := contractsDecisionMetadata(r.Decision)
	if err != nil {
		return err
	}
	if r.RunID != decisionRunID {
		return fmt.Errorf("%w: response.run_id=%s decision.run_id=%s", ErrStep70ResponseRunIDMismatch, r.RunID, decisionRunID)
	}
	switch r.Decision.Action {
	case contracts.DecisionActionAdopt:
		adopt, err := contractsDecisionAdopt(r.Decision)
		if err != nil {
			return err
		}
		expected := contracts.ComputeAdoptIdempotencyKey(string(adopt.RunID), adopt.TargetSha, adopt.BestShaBefore, adopt.CandidatesHash)
		if adopt.IdempotencyKey != expected {
			return fmt.Errorf("%w: got=%s want=%s", ErrStep70AdoptIdempotencyKeyMismatch, adopt.IdempotencyKey, expected)
		}
		if !r.Promoted {
			return fmt.Errorf("%w: action=%s promoted=%t", ErrStep70AdoptRequiresPromoted, r.Decision.Action, r.Promoted)
		}
	case contracts.DecisionActionReject:
		if r.Promoted {
			return fmt.Errorf("%w: action=%s promoted=%t", ErrStep70RejectMustNotPromote, r.Decision.Action, r.Promoted)
		}
	case contracts.DecisionActionNoop:
		if r.Promoted {
			return fmt.Errorf("%w: action=%s promoted=%t", ErrStep70NoopMustNotPromote, r.Decision.Action, r.Promoted)
		}
	case contracts.DecisionActionRollback:
		if r.Promoted {
			return fmt.Errorf("%w: action=%s promoted=%t", ErrStep70RollbackMustNotPromote, r.Decision.Action, r.Promoted)
		}
	default:
		return fmt.Errorf("%w: action=%q", contracts.ErrUnknownDecisionAction, r.Decision.Action)
	}
	// Secondary sanity check: if Promoted==true but action is not adopt, fail
	// (this is equivalent to the per-action branches above but kept as a single
	// direction-reversed assertion for review clarity).
	if r.Promoted && r.Decision.Action != contracts.DecisionActionAdopt {
		return fmt.Errorf("%w: action=%s promoted=true", ErrStep70PromotedRequiresAdopt, r.Decision.Action)
	}
	return nil
}

func (r Step70Response) validateAgainstRequest(req Step70Request) error {
	if err := req.Validate(); err != nil {
		return err
	}
	if err := r.validate(); err != nil {
		return err
	}
	if r.RunID != req.TaskPackage.RunID {
		return fmt.Errorf("%w: response.run_id=%s request.run_id=%s", ErrStep70RequestResponseRunIDMismatch, r.RunID, req.TaskPackage.RunID)
	}
	if r.Decision.Action != contracts.DecisionActionAdopt {
		return nil
	}
	adopt, err := contractsDecisionAdopt(r.Decision)
	if err != nil {
		return err
	}
	if adopt.CandidatesHash != req.Candidates.CandidatesHash {
		return fmt.Errorf("%w: decision=%s request=%s", ErrStep70AdoptCandidatesHashMismatch, adopt.CandidatesHash, req.Candidates.CandidatesHash)
	}
	return nil
}

func contractsDecisionMetadata(d contracts.Decision) (contracts.DecisionAction, contracts.DecisionAction, contracts.RunID, error) {
	if d.Value == nil {
		return "", "", "", ErrStep70DecisionMissing
	}
	switch v := d.Value.(type) {
	case contracts.DecisionAdopt:
		return contracts.DecisionActionAdopt, v.Action, v.RunID, nil
	case *contracts.DecisionAdopt:
		if v == nil {
			return "", "", "", ErrStep70DecisionMissing
		}
		return contracts.DecisionActionAdopt, v.Action, v.RunID, nil
	case contracts.DecisionReject:
		return contracts.DecisionActionReject, v.Action, v.RunID, nil
	case *contracts.DecisionReject:
		if v == nil {
			return "", "", "", ErrStep70DecisionMissing
		}
		return contracts.DecisionActionReject, v.Action, v.RunID, nil
	case contracts.DecisionNoop:
		return contracts.DecisionActionNoop, v.Action, v.RunID, nil
	case *contracts.DecisionNoop:
		if v == nil {
			return "", "", "", ErrStep70DecisionMissing
		}
		return contracts.DecisionActionNoop, v.Action, v.RunID, nil
	case contracts.DecisionRollback:
		return contracts.DecisionActionRollback, v.Action, v.RunID, nil
	case *contracts.DecisionRollback:
		if v == nil {
			return "", "", "", ErrStep70DecisionMissing
		}
		return contracts.DecisionActionRollback, v.Action, v.RunID, nil
	default:
		return "", "", "", contracts.ErrUnknownDecisionAction
	}
}

func contractsDecisionAdopt(d contracts.Decision) (contracts.DecisionAdopt, error) {
	switch v := d.Value.(type) {
	case contracts.DecisionAdopt:
		return v, nil
	case *contracts.DecisionAdopt:
		if v == nil {
			return contracts.DecisionAdopt{}, ErrStep70DecisionMissing
		}
		return *v, nil
	default:
		return contracts.DecisionAdopt{}, contracts.ErrDecisionVariantTypeMismatch
	}
}
