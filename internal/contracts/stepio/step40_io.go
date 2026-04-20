package stepio

import (
	"errors"
	"fmt"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/validation"
)

// Step40Request is the input envelope for step 40 (extract-rules).
// io-contracts.md §step 40.
type Step40Request struct {
	TaskPackage contracts.TaskPackage `json:"task_package"`
	// RegistryPath: `<runs_base>/rules-registry.jsonl` への絶対 path。
	RegistryPath string `json:"registry_path" validate:"required"`
}

// Step40Response is the output envelope for step 40.
// 完了マーカー `<run>/40/candidates.json` が書かれている前提で返す。
type Step40Response struct {
	RunID      contracts.RunID      `json:"run_id" validate:"required,run_id_fmt"`
	Candidates contracts.Candidates `json:"candidates"`
	// CandidatesCount: 便利 accessor。len(Candidates.Candidates) と等価。
	CandidatesCount int `json:"candidates_count" validate:"gte=0"`
}

var (
	ErrStep40ResponseRunIDMismatch   = errors.New("stepio: step40: response.run_id must equal candidates.run_id")
	ErrStep40CandidatesCountMismatch = errors.New("stepio: step40: candidates_count must equal len(candidates.candidates)")
)

func (r *Step40Request) UnmarshalJSON(data []byte) error {
	type alias Step40Request
	var a alias
	if err := contracts.DecodeStrictJSON(data, &a); err != nil {
		return err
	}
	*r = Step40Request(a)
	return r.Validate()
}

func (r Step40Request) Validate() error {
	if err := validation.Instance().Struct(r); err != nil {
		return err
	}
	if err := validateRegistryPath(r.RegistryPath); err != nil {
		return err
	}
	return r.TaskPackage.Validate()
}

func (r *Step40Response) UnmarshalJSON(data []byte) error {
	type alias Step40Response
	var a alias
	if err := contracts.DecodeStrictJSON(data, &a); err != nil {
		return err
	}
	*r = Step40Response(a)
	return r.Validate()
}

func (r Step40Response) Validate() error {
	if err := validation.Instance().Struct(r); err != nil {
		return err
	}
	if err := r.Candidates.Validate(); err != nil {
		return err
	}
	if r.RunID != r.Candidates.RunID {
		return fmt.Errorf("%w: response.run_id=%s candidates.run_id=%s", ErrStep40ResponseRunIDMismatch, r.RunID, r.Candidates.RunID)
	}
	if r.CandidatesCount != len(r.Candidates.Candidates) {
		return fmt.Errorf("%w: candidates_count=%d len(candidates)=%d", ErrStep40CandidatesCountMismatch, r.CandidatesCount, len(r.Candidates.Candidates))
	}
	return nil
}

func DecodeAndValidateStep40Response(data []byte, req Step40Request) (Step40Response, error) {
	var resp Step40Response
	if err := resp.UnmarshalJSON(data); err != nil {
		return Step40Response{}, err
	}
	if err := req.Validate(); err != nil {
		return Step40Response{}, err
	}
	if resp.RunID != req.TaskPackage.RunID {
		return Step40Response{}, fmt.Errorf("%w: response.run_id=%s request.run_id=%s", ErrResponseRunIDMismatch, resp.RunID, req.TaskPackage.RunID)
	}
	return resp, nil
}
