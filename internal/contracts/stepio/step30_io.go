package stepio

import (
	"errors"
	"fmt"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/validation"
)

// Step30Request is the input envelope for step 30 (score pass1).
// io-contracts.md §step 30 / 60.
type Step30Request struct {
	TaskPackage contracts.TaskPackage `json:"task_package"`
	// ScorableAgents: success manifest を持ち採点対象となる agent 一覧。
	// LoadScorableManifest で filter 済みを前提 (error/timeout は除外)。
	ScorableAgents []contracts.AgentID `json:"scorable_agents" validate:"required,min=1,unique,dive,agent_id_fmt"`
	RubricVersion  string              `json:"rubric_version" validate:"required"`
	PromptVersion  string              `json:"prompt_version" validate:"required"`
}

// Step30Response is the output envelope for step 30.
// 完了マーカー `<run>/30/done.marker` が書かれている前提で返す。
type Step30Response struct {
	RunID           contracts.RunID `json:"run_id" validate:"required,run_id_fmt"`
	ScoresCount     int             `json:"scores_count" validate:"gte=0"`
	ComplianceCount int             `json:"compliance_count" validate:"gte=0"`
	ResolvedAt      time.Time       `json:"resolved_at" validate:"required"`
}

var (
	ErrStep30ScorableAgentPassMismatch = errors.New("stepio: step30: scorable_agents do not match TaskPackage.Worktrees[pass=1]")
	ErrStep30ScoresCountMismatch       = errors.New("stepio: step30: scores_count must match request.scorable_agents x rubric dimensions")
)

func (r *Step30Request) UnmarshalJSON(data []byte) error {
	type alias Step30Request
	var a alias
	if err := contracts.DecodeStrictJSON(data, &a); err != nil {
		return err
	}
	*r = Step30Request(a)
	return r.Validate()
}

func (r Step30Request) Validate() error {
	if err := validation.Instance().Struct(r); err != nil {
		return err
	}
	if err := r.TaskPackage.Validate(); err != nil {
		return err
	}
	return validateAgentsWithinPass(r.ScorableAgents, r.TaskPackage, 1, ErrStep30ScorableAgentPassMismatch)
}

func (r *Step30Response) UnmarshalJSON(data []byte) error {
	type alias Step30Response
	var a alias
	if err := contracts.DecodeStrictJSON(data, &a); err != nil {
		return err
	}
	*r = Step30Response(a)
	return r.Validate()
}

func (r Step30Response) Validate() error {
	return validation.Instance().Struct(r)
}

func DecodeAndValidateStep30Response(data []byte, req Step30Request) (Step30Response, error) {
	var resp Step30Response
	if err := resp.UnmarshalJSON(data); err != nil {
		return Step30Response{}, err
	}
	if err := req.Validate(); err != nil {
		return Step30Response{}, err
	}
	if resp.RunID != req.TaskPackage.RunID {
		return Step30Response{}, fmt.Errorf("%w: response.run_id=%s request.run_id=%s", ErrResponseRunIDMismatch, resp.RunID, req.TaskPackage.RunID)
	}
	expectedScores := expectedScoresCountForScorableAgents(req.ScorableAgents)
	if resp.ScoresCount != expectedScores {
		return Step30Response{}, fmt.Errorf("%w: scores_count=%d expected=%d scorable_agents=%d", ErrStep30ScoresCountMismatch, resp.ScoresCount, expectedScores, len(req.ScorableAgents))
	}
	return resp, nil
}
