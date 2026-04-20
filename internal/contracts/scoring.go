package contracts

import (
	"errors"
	"fmt"
	"time"
)

// Dimension is one of the 5 harness rubric axes (io-contracts.md §step30/60).
// Phase 0 は固定 enum. Rubric 自体の差し替えは rubric_version で記録する.
type Dimension string

const (
	DimensionFidelity        Dimension = "fidelity"
	DimensionCorrectness     Dimension = "correctness"
	DimensionMaintainability Dimension = "maintainability"
	DimensionDiscipline      Dimension = "discipline"
	DimensionCommunication   Dimension = "communication"
)

// VerdictPath records how the panel resolved (single / agreement / arbitrated /
// arbiter_overruled). io-contracts.md §step30/60.
type VerdictPath string

const (
	VerdictPathSingle           VerdictPath = "single"
	VerdictPathAgreement        VerdictPath = "agreement"
	VerdictPathArbitrated       VerdictPath = "arbitrated"
	VerdictPathArbiterOverruled VerdictPath = "arbiter_overruled"
)

type JudgeRole string

const (
	JudgeRolePrimary   JudgeRole = "primary"
	JudgeRoleSecondary JudgeRole = "secondary"
	JudgeRoleArbiter   JudgeRole = "arbiter"
)

// OverflowRef is a reference to a sidecar file when a capped free-text field
// overflows its 4KB-safe cap. Both fields required together.
type OverflowRef struct {
	Path   string `json:"path" validate:"required"`
	Sha256 string `json:"sha256" validate:"required,sha256_hex"`
}

type RawJudgeRef struct {
	Role   JudgeRole `json:"role" validate:"required,oneof=primary secondary"`
	Sha256 string    `json:"sha256" validate:"required,sha256_hex"`
}

// ScoreEntry is one row appended to `<run>/{30|60}/scores-{A,B}.jsonl` (final
// layer). Raw layer (`scores-{A,B}-raw.jsonl`) uses a separate RawScoreEntry.
//
// Reasons は 1000 字/次元 cap、超過時 sidecar + reasons_overflow_ref
// (io-contracts.md §4KB overflow 棚卸し).
type ScoreEntry struct {
	SchemaVersion string  `json:"schema_version" validate:"required,oneof=1"`
	RunID         RunID   `json:"run_id" validate:"required,run_id_fmt"`
	Pass          int     `json:"pass" validate:"required,oneof=1 2"`
	Agent         AgentID `json:"agent" validate:"required,agent_id_fmt"`

	Dimension Dimension `json:"dimension" validate:"required,oneof=fidelity correctness maintainability discipline communication"`

	// Score: 0..100 integer (float 禁止, hash 安定性のため).
	Score int `json:"score" validate:"gte=0,lte=100"`

	// Reasons: 1000 字 cap (io-contracts.md §step30/60).
	Reasons            string       `json:"reasons,omitempty" validate:"omitempty,max=1000"`
	ReasonsOverflowRef *OverflowRef `json:"reasons_overflow_ref,omitempty" validate:"omitempty"`

	VerdictPath   VerdictPath `json:"verdict_path" validate:"required,oneof=single agreement arbitrated arbiter_overruled"`
	RubricVersion string      `json:"rubric_version" validate:"required"`
	PromptVersion string      `json:"prompt_version" validate:"required"`

	ResolvedAt time.Time `json:"resolved_at" validate:"required"`
}

type RawScoreEntry struct {
	SchemaVersion string    `json:"schema_version" validate:"required,oneof=1"`
	RunID         RunID     `json:"run_id" validate:"required,run_id_fmt"`
	Pass          int       `json:"pass" validate:"required,oneof=1 2"`
	Agent         AgentID   `json:"agent" validate:"required,agent_id_fmt"`
	JudgeRole     JudgeRole `json:"judge_role" validate:"required,oneof=primary secondary arbiter"`
	Dimension     Dimension `json:"dimension" validate:"required,oneof=fidelity correctness maintainability discipline communication"`
	Score         int       `json:"score" validate:"gte=0,lte=100"`

	Reasons            string       `json:"reasons,omitempty" validate:"omitempty,max=1000"`
	ReasonsOverflowRef *OverflowRef `json:"reasons_overflow_ref,omitempty" validate:"omitempty"`
	OutputSha256       string       `json:"output_sha256" validate:"required,sha256_hex"`
	PrimaryRef         *RawJudgeRef `json:"primary_ref,omitempty" validate:"omitempty"`
	SecondaryRef       *RawJudgeRef `json:"secondary_ref,omitempty" validate:"omitempty"`

	RubricVersion string    `json:"rubric_version" validate:"required"`
	PromptVersion string    `json:"prompt_version" validate:"required"`
	ResolvedAt    time.Time `json:"resolved_at" validate:"required"`
}

type RawComplianceEntry struct {
	SchemaVersion string            `json:"schema_version" validate:"required,oneof=1"`
	RunID         RunID             `json:"run_id" validate:"required,run_id_fmt"`
	Pass          int               `json:"pass" validate:"required,oneof=1 2"`
	Agent         AgentID           `json:"agent" validate:"required,agent_id_fmt"`
	JudgeRole     JudgeRole         `json:"judge_role" validate:"required,oneof=primary secondary arbiter"`
	RuleID        string            `json:"rule_id" validate:"required"`
	Verdict       ComplianceVerdict `json:"verdict" validate:"required,oneof=compliant violated valid_exception invalid_exception missed n_a"`

	Rationale            string       `json:"rationale,omitempty" validate:"omitempty,max=500"`
	RationaleOverflowRef *OverflowRef `json:"rationale_overflow_ref,omitempty" validate:"omitempty"`
	OutputSha256         string       `json:"output_sha256" validate:"required,sha256_hex"`
	PrimaryRef           *RawJudgeRef `json:"primary_ref,omitempty" validate:"omitempty"`
	SecondaryRef         *RawJudgeRef `json:"secondary_ref,omitempty" validate:"omitempty"`

	RubricVersion string    `json:"rubric_version" validate:"required"`
	PromptVersion string    `json:"prompt_version" validate:"required"`
	ResolvedAt    time.Time `json:"resolved_at" validate:"required"`
}

// ComplianceVerdict enumerates the 6 possible verdicts per rule per agent
// (io-contracts.md §step30/60).
type ComplianceVerdict string

const (
	ComplianceVerdictCompliant        ComplianceVerdict = "compliant"
	ComplianceVerdictViolated         ComplianceVerdict = "violated"
	ComplianceVerdictValidException   ComplianceVerdict = "valid_exception"
	ComplianceVerdictInvalidException ComplianceVerdict = "invalid_exception"
	ComplianceVerdictMissed           ComplianceVerdict = "missed"
	ComplianceVerdictNA               ComplianceVerdict = "n_a"
)

// ComplianceEntry is one row appended to `<run>/{30|60}/compliance-{A,B}.jsonl`.
type ComplianceEntry struct {
	SchemaVersion string  `json:"schema_version" validate:"required,oneof=1"`
	RunID         RunID   `json:"run_id" validate:"required,run_id_fmt"`
	Pass          int     `json:"pass" validate:"required,oneof=1 2"`
	Agent         AgentID `json:"agent" validate:"required,agent_id_fmt"`

	RuleID  string            `json:"rule_id" validate:"required"`
	Verdict ComplianceVerdict `json:"verdict" validate:"required,oneof=compliant violated valid_exception invalid_exception missed n_a"`

	// Rationale: 500 字 cap (io-contracts.md §4KB overflow 棚卸し).
	Rationale            string       `json:"rationale,omitempty" validate:"omitempty,max=500"`
	RationaleOverflowRef *OverflowRef `json:"rationale_overflow_ref,omitempty" validate:"omitempty"`

	VerdictPath   VerdictPath `json:"verdict_path" validate:"required,oneof=single agreement arbitrated arbiter_overruled"`
	RubricVersion string      `json:"rubric_version" validate:"required"`
	PromptVersion string      `json:"prompt_version" validate:"required"`

	ResolvedAt time.Time `json:"resolved_at" validate:"required"`
}

// PairwiseWinner: A / B / tie (step60 pairwise.jsonl).
type PairwiseWinner string

const (
	PairwiseWinnerA   PairwiseWinner = "A"
	PairwiseWinnerB   PairwiseWinner = "B"
	PairwiseWinnerTie PairwiseWinner = "tie"
)

// PairwiseMargin: judge が付けた自信度 (decisive > clear > slight).
type PairwiseMargin string

const (
	PairwiseMarginDecisive PairwiseMargin = "decisive"
	PairwiseMarginClear    PairwiseMargin = "clear"
	PairwiseMarginSlight   PairwiseMargin = "slight"
)

// PairwiseEntry is one row appended to `<run>/60/pairwise.jsonl`.
// pass1 best candidate vs pass2 best candidate の per-agent 比較を記録する.
type PairwiseEntry struct {
	SchemaVersion string `json:"schema_version" validate:"required,oneof=1"`
	RunID         RunID  `json:"run_id" validate:"required,run_id_fmt"`

	// AgentA / AgentB: 比較対象 agent (同 PR 内).
	AgentA AgentID `json:"agent_a" validate:"required,agent_id_fmt"`
	AgentB AgentID `json:"agent_b" validate:"required,agent_id_fmt"`

	Winner PairwiseWinner `json:"winner" validate:"required,oneof=A B tie"`
	Margin PairwiseMargin `json:"margin" validate:"required,oneof=decisive clear slight"`

	// Justification: 500 字 cap (io-contracts.md §4KB overflow 棚卸し).
	Justification            string       `json:"justification,omitempty" validate:"omitempty,max=500"`
	JustificationOverflowRef *OverflowRef `json:"justification_overflow_ref,omitempty" validate:"omitempty"`

	VerdictPath   VerdictPath `json:"verdict_path" validate:"required,oneof=single agreement arbitrated arbiter_overruled"`
	RubricVersion string      `json:"rubric_version" validate:"required"`
	PromptVersion string      `json:"prompt_version" validate:"required"`

	ResolvedAt time.Time `json:"resolved_at" validate:"required"`
}

var (
	ErrRawJudgeRefsRequired  = errors.New("contracts: raw scoring: arbiter rows require primary_ref and secondary_ref")
	ErrRawJudgeRefsForbidden = errors.New("contracts: raw scoring: primary/secondary rows must not carry arbiter refs")
	ErrRawJudgePrimaryRole   = errors.New("contracts: raw scoring: primary_ref.role must be primary")
	ErrRawJudgeSecondaryRole = errors.New("contracts: raw scoring: secondary_ref.role must be secondary")
)

func (e RawScoreEntry) Validate() error {
	if err := validateStruct(e); err != nil {
		return err
	}
	return validateRawJudgeRefs(e.JudgeRole, e.PrimaryRef, e.SecondaryRef)
}

func (e RawComplianceEntry) Validate() error {
	if err := validateStruct(e); err != nil {
		return err
	}
	return validateRawJudgeRefs(e.JudgeRole, e.PrimaryRef, e.SecondaryRef)
}

func validateRawJudgeRefs(role JudgeRole, primaryRef, secondaryRef *RawJudgeRef) error {
	switch role {
	case JudgeRoleArbiter:
		if primaryRef == nil || secondaryRef == nil {
			return ErrRawJudgeRefsRequired
		}
		if primaryRef.Role != JudgeRolePrimary {
			return fmt.Errorf("%w: got=%s", ErrRawJudgePrimaryRole, primaryRef.Role)
		}
		if secondaryRef.Role != JudgeRoleSecondary {
			return fmt.Errorf("%w: got=%s", ErrRawJudgeSecondaryRole, secondaryRef.Role)
		}
	case JudgeRolePrimary, JudgeRoleSecondary:
		if primaryRef != nil || secondaryRef != nil {
			return ErrRawJudgeRefsForbidden
		}
	}
	return nil
}

type Step30DoneMarker struct {
	CompletedAgents []AgentID               `json:"completed_agents" validate:"required,unique,dive,agent_id_fmt"`
	Dimensions      []Dimension             `json:"dimensions" validate:"required,unique,dive,oneof=fidelity correctness maintainability discipline communication"`
	ExpectedCounts  Step30ExpectedCounts    `json:"expected_counts" validate:"required"`
	ContentHashes   Step30DoneContentHashes `json:"content_hashes" validate:"required"`
	RawHashes       StepDoneRawHashes       `json:"raw_hashes" validate:"required"`
	ResolvedAt      time.Time               `json:"resolved_at" validate:"required"`
}

type Step30ExpectedCounts struct {
	Scores     int64 `json:"scores" validate:"gte=0"`
	Compliance int64 `json:"compliance" validate:"gte=0"`
}

type Step30DoneContentHashes struct {
	ScoresFinal     string `json:"scores_final" validate:"required,sha256_hex"`
	ComplianceFinal string `json:"compliance_final" validate:"required,sha256_hex"`
}

type StepDoneRawHashes struct {
	ScoresRaw     string `json:"scores_raw" validate:"required,sha256_hex"`
	ComplianceRaw string `json:"compliance_raw" validate:"required,sha256_hex"`
}

func (m Step30DoneMarker) Validate() error {
	return validateStruct(m)
}

type Step60DoneMarker struct {
	CompletedAgents []AgentID               `json:"completed_agents" validate:"required,unique,dive,agent_id_fmt"`
	Dimensions      []Dimension             `json:"dimensions" validate:"required,unique,dive,oneof=fidelity correctness maintainability discipline communication"`
	ExpectedCounts  Step60ExpectedCounts    `json:"expected_counts" validate:"required"`
	ContentHashes   Step60DoneContentHashes `json:"content_hashes" validate:"required"`
	RawHashes       StepDoneRawHashes       `json:"raw_hashes" validate:"required"`
	ResolvedAt      time.Time               `json:"resolved_at" validate:"required"`
}

type Step60ExpectedCounts struct {
	Scores     int64 `json:"scores" validate:"gte=0"`
	Compliance int64 `json:"compliance" validate:"gte=0"`
	Pairwise   int64 `json:"pairwise" validate:"gte=0"`
}

type Step60DoneContentHashes struct {
	ScoresFinal     string `json:"scores_final" validate:"required,sha256_hex"`
	ComplianceFinal string `json:"compliance_final" validate:"required,sha256_hex"`
	PairwiseFinal   string `json:"pairwise_final" validate:"required,sha256_hex"`
}

func (m Step60DoneMarker) Validate() error {
	return validateStruct(m)
}
