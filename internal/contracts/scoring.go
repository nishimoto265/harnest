package contracts

import "time"

// Dimension is one of the 5 harness rubric axes (io-contracts.md §step30/60).
// Phase 0 は固定 enum. Rubric 自体の差し替えは rubric_version で記録する.
type Dimension string

const (
	DimensionFidelity         Dimension = "fidelity"
	DimensionCorrectness      Dimension = "correctness"
	DimensionMaintainability  Dimension = "maintainability"
	DimensionDiscipline       Dimension = "discipline"
	DimensionCommunication    Dimension = "communication"
)

// VerdictPath records how the panel resolved (single / agreement / arbitrated /
// arbiter_overruled). io-contracts.md §step30/60.
type VerdictPath string

const (
	VerdictPathSingle            VerdictPath = "single"
	VerdictPathAgreement         VerdictPath = "agreement"
	VerdictPathArbitrated        VerdictPath = "arbitrated"
	VerdictPathArbiterOverruled  VerdictPath = "arbiter_overruled"
)

// OverflowRef is a reference to a sidecar file when a capped free-text field
// overflows its 4KB-safe cap. Both fields required together.
type OverflowRef struct {
	Path   string `json:"path" validate:"required"`
	Sha256 string `json:"sha256" validate:"required,sha256_hex"`
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
	Reasons             string       `json:"reasons,omitempty" validate:"omitempty,max=1000"`
	ReasonsOverflowRef  *OverflowRef `json:"reasons_overflow_ref,omitempty" validate:"omitempty"`

	VerdictPath   VerdictPath `json:"verdict_path" validate:"required,oneof=single agreement arbitrated arbiter_overruled"`
	RubricVersion string      `json:"rubric_version" validate:"required"`
	PromptVersion string      `json:"prompt_version" validate:"required"`

	ResolvedAt time.Time `json:"resolved_at" validate:"required"`
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
