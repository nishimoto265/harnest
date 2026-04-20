package stepio

import (
	"errors"
	"fmt"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/validation"
)

// Step60Request is the input envelope for step 60 (score pass2 + pairwise).
type Step60Request struct {
	TaskPackage    contracts.TaskPackage `json:"task_package"`
	ScorableAgents []contracts.AgentID   `json:"scorable_agents" validate:"required,min=1,unique,dive,agent_id_fmt"`
	RubricVersion  string                `json:"rubric_version" validate:"required"`
	PromptVersion  string                `json:"prompt_version" validate:"required"`
}

// Step60Response is the output envelope for step 60.
// 完了マーカー `<run>/60/done.marker` が書かれている前提。pairwise も含む。
type Step60Response struct {
	RunID           contracts.RunID `json:"run_id" validate:"required,run_id_fmt"`
	ScoresCount     int             `json:"scores_count" validate:"gte=0"`
	ComplianceCount int             `json:"compliance_count" validate:"gte=0"`
	PairwiseCount   int             `json:"pairwise_count" validate:"gte=0"`
	ResolvedAt      time.Time       `json:"resolved_at" validate:"required"`
}

var ErrStep60ScorableAgentPassMismatch = errors.New("stepio: step60: scorable_agents do not match TaskPackage.Worktrees[pass=2]")

func (r *Step60Request) UnmarshalJSON(data []byte) error {
	type alias Step60Request
	var a alias
	if err := contracts.DecodeStrictJSON(data, &a); err != nil {
		return err
	}
	*r = Step60Request(a)
	return r.Validate()
}

func (r Step60Request) Validate() error {
	if err := validation.Instance().Struct(r); err != nil {
		return err
	}
	if err := r.TaskPackage.Validate(); err != nil {
		return err
	}
	return validateAgentsWithinPass(r.ScorableAgents, r.TaskPackage, 2, ErrStep60ScorableAgentPassMismatch)
}

func (r *Step60Response) UnmarshalJSON(data []byte) error {
	type alias Step60Response
	var a alias
	if err := contracts.DecodeStrictJSON(data, &a); err != nil {
		return err
	}
	*r = Step60Response(a)
	return r.Validate()
}

func (r Step60Response) Validate() error {
	return validation.Instance().Struct(r)
}

func DecodeAndValidateStep60Response(data []byte, req Step60Request) (Step60Response, error) {
	var resp Step60Response
	if err := resp.UnmarshalJSON(data); err != nil {
		return Step60Response{}, err
	}
	if err := req.Validate(); err != nil {
		return Step60Response{}, err
	}
	if resp.RunID != req.TaskPackage.RunID {
		return Step60Response{}, fmt.Errorf("%w: response.run_id=%s request.run_id=%s", ErrResponseRunIDMismatch, resp.RunID, req.TaskPackage.RunID)
	}
	return resp, nil
}
