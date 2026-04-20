package stepio

import (
	"errors"

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
	CandidateRuleIDs []string `json:"candidate_rule_ids"`
}

// ErrStep50AgentPassMismatch mirrors ErrStep20AgentPassMismatch for pass=2.
var ErrStep50AgentPassMismatch = errors.New("stepio: step50: agents do not match TaskPackage.Worktrees[pass=2]")

// Validate enforces tag-based validation + pass-2 agent subset invariant.
func (r Step50Request) Validate() error {
	if err := validation.Instance().Struct(r); err != nil {
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

// Step50Response is the output envelope for step 50 (mirrors Step20Response).
type Step50Response struct {
	RunID           contracts.RunID     `json:"run_id"`
	Pass            int                 `json:"pass"` // 固定 2
	Results         []Step20AgentResult `json:"results"`
	RescueExhausted []RescueExhausted   `json:"rescue_exhausted,omitempty"`
}
