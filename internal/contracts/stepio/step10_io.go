package stepio

import (
	"errors"
	"fmt"

	"github.com/nishimoto265/harnest/internal/contracts"
	"github.com/nishimoto265/harnest/internal/validation"
)

// Step10Request is the input envelope for step 10 (restore-base).
// io-contracts.md §step 10: restore-base.
type Step10Request struct {
	// PR: target GitHub PR number (merged).
	PR int `json:"pr" validate:"required,gt=0"`
	// BestBranch: 現行 best 設定 branch (= remote に push してある promotion 結果).
	BestBranch string `json:"best_branch" validate:"required"`
	// ExpectedRunID: optional stale-replay guard. When present, the step10
	// response must bind back to this orchestrator-issued run_id.
	ExpectedRunID contracts.RunID `json:"expected_run_id,omitempty" validate:"omitempty,run_id_fmt"`
	// HarnessFiles: best_branch 上の harness rule files を適用するかどうか。
	// Phase 0 では常に true 想定。
	HarnessFiles bool `json:"harness_files"`
}

// Step10Response is the output envelope for step 10.
// TaskPackage は `<run>/task-package.json` に atomic write 済み前提で返す。
type Step10Response struct {
	RunID       contracts.RunID       `json:"run_id" validate:"required,run_id_fmt"`
	TaskPackage contracts.TaskPackage `json:"task_package"`
	BaseSHA     string                `json:"base_sha" validate:"required,sha1_hex"`
	// WorktreesCreated: step10 が新規作成した worktree 数 (resume 時は 0 もあり)。
	WorktreesCreated int `json:"worktrees_created" validate:"gte=0,lte=6"`
}

var (
	ErrStep10ResponseRunIDMismatch   = errors.New("stepio: step10: response.run_id must equal task_package.run_id")
	ErrStep10ResponseBaseSHAMismatch = errors.New("stepio: step10: response.base_sha must equal task_package.base_sha")
	ErrStep10ExpectedRunIDMismatch   = errors.New("stepio: step10: response.run_id must equal request.expected_run_id")
	ErrStep10RequestPRMismatch       = errors.New("stepio: step10: response.task_package.pr must equal request.pr")
	ErrStep10RequestBestBranch       = errors.New("stepio: step10: response.task_package.best_branch must equal request.best_branch")
)

func (r *Step10Request) UnmarshalJSON(data []byte) error {
	type alias Step10Request
	var a alias
	if err := contracts.DecodeStrictJSON(data, &a); err != nil {
		return err
	}
	*r = Step10Request(a)
	return r.Validate()
}

func (r Step10Request) Validate() error {
	return validation.Instance().Struct(r)
}

func (r *Step10Response) UnmarshalJSON(data []byte) error {
	type alias Step10Response
	var a alias
	if err := contracts.DecodeStrictJSON(data, &a); err != nil {
		return err
	}
	*r = Step10Response(a)
	return r.Validate()
}

func (r Step10Response) Validate() error {
	if err := validation.Instance().Struct(r); err != nil {
		return err
	}
	if err := r.TaskPackage.Validate(); err != nil {
		return err
	}
	if r.RunID != r.TaskPackage.RunID {
		return fmt.Errorf("%w: response.run_id=%s task_package.run_id=%s", ErrStep10ResponseRunIDMismatch, r.RunID, r.TaskPackage.RunID)
	}
	if r.BaseSHA != r.TaskPackage.BaseSHA {
		return fmt.Errorf("%w: response.base_sha=%s task_package.base_sha=%s", ErrStep10ResponseBaseSHAMismatch, r.BaseSHA, r.TaskPackage.BaseSHA)
	}
	return nil
}

// DecodeAndValidateStep10Response is the sanctioned read boundary for step10
// responses when the original request is available. Step10Request does not
// carry run_id/base_sha, so the request-bound invariants are the request-derived
// task metadata that step10 rehydrates into task_package.
func DecodeAndValidateStep10Response(data []byte, req Step10Request) (Step10Response, error) {
	var resp Step10Response
	if err := resp.UnmarshalJSON(data); err != nil {
		return Step10Response{}, err
	}
	if err := req.Validate(); err != nil {
		return Step10Response{}, err
	}
	if resp.TaskPackage.PR != req.PR {
		return Step10Response{}, fmt.Errorf("%w: response.pr=%d request.pr=%d", ErrStep10RequestPRMismatch, resp.TaskPackage.PR, req.PR)
	}
	if resp.TaskPackage.BestBranch != req.BestBranch {
		return Step10Response{}, fmt.Errorf("%w: response.best_branch=%q request.best_branch=%q", ErrStep10RequestBestBranch, resp.TaskPackage.BestBranch, req.BestBranch)
	}
	if req.ExpectedRunID != "" && resp.RunID != req.ExpectedRunID {
		return Step10Response{}, fmt.Errorf("%w: response.run_id=%s request.expected_run_id=%s", ErrStep10ExpectedRunIDMismatch, resp.RunID, req.ExpectedRunID)
	}
	return resp, nil
}
