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
type Step70Response struct {
	RunID    contracts.RunID    `json:"run_id" validate:"required,run_id_fmt"`
	Decision contracts.Decision `json:"decision"`
	// Promoted: Decision.Action == adopt かつ best_branch push + registry append
	// まで完走した場合のみ true。orchestrator は true のときに promoted event を
	// state に append 可 (step70 自身が既に append 済みなので重複 append しない)。
	Promoted bool `json:"promoted"`
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
	ErrStep70PromotedRequiresAdopt  = errors.New("stepio: step70: promoted=true requires Decision.Action=adopt")
	ErrStep70AdoptRequiresPromoted  = errors.New("stepio: step70: Decision.Action=adopt requires promoted=true")
	ErrStep70RollbackMustNotPromote = errors.New("stepio: step70: Decision.Action=rollback must have promoted=false")
	ErrStep70RejectMustNotPromote   = errors.New("stepio: step70: Decision.Action=reject must have promoted=false")
	ErrStep70NoopMustNotPromote     = errors.New("stepio: step70: Decision.Action=noop must have promoted=false")
	ErrStep70DecisionMissing        = errors.New("stepio: step70: Decision.Value must be populated")
	ErrStep70RequestRunIDMismatch   = errors.New("stepio: step70: task_package.run_id must equal candidates.run_id")
	ErrStep70ResponseRunIDMismatch  = errors.New("stepio: step70: response.run_id must equal decision.run_id")
)

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

// Validate enforces tag-based validation + Decision internal validation +
// Promoted/Action consistency (finding #5).
func (r Step70Response) Validate() error {
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

func contractsDecisionMetadata(d contracts.Decision) (contracts.DecisionAction, contracts.DecisionAction, contracts.RunID, error) {
	if d.Value == nil {
		return "", "", "", ErrStep70DecisionMissing
	}
	switch v := d.Value.(type) {
	case contracts.DecisionAdopt:
		return contracts.DecisionActionAdopt, v.Action, v.RunID, nil
	case contracts.DecisionReject:
		return contracts.DecisionActionReject, v.Action, v.RunID, nil
	case contracts.DecisionNoop:
		return contracts.DecisionActionNoop, v.Action, v.RunID, nil
	case contracts.DecisionRollback:
		return contracts.DecisionActionRollback, v.Action, v.RunID, nil
	default:
		return "", "", "", contracts.ErrUnknownDecisionAction
	}
}
