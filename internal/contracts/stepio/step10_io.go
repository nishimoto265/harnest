package stepio

import (
	"errors"
	"fmt"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/validation"
)

// Step10Request is the input envelope for step 10 (restore-base).
// io-contracts.md §step 10: restore-base.
type Step10Request struct {
	// PR: target GitHub PR number (merged).
	PR int `json:"pr" validate:"required,gt=0"`
	// BestBranch: 現行 best 設定 branch (= remote に push してある promotion 結果).
	BestBranch string `json:"best_branch" validate:"required"`
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
