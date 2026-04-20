package contracts

// ChecklistItemVerdict is the 3-symbol outcome an agent records for each
// harness checklist item (io-contracts.md §step20 / checklist-result.json).
//
//   - compliant: 項目を満たしている
//   - n_a:       本 PR には該当しない (明示的非該当)
//   - exception: あえて違反した (rationale 必須、exception_reason で正当化)
type ChecklistItemVerdict string

const (
	ChecklistItemCompliant ChecklistItemVerdict = "compliant"
	ChecklistItemNA        ChecklistItemVerdict = "n_a"
	ChecklistItemException ChecklistItemVerdict = "exception"
)

// ChecklistItem is one agent-recorded entry from checklist-result.json.
type ChecklistItem struct {
	// RuleID: 対応する rules-registry の rule_id.
	RuleID string `json:"rule_id" validate:"required"`
	// Verdict: compliant / n_a / exception の 3-symbol.
	Verdict ChecklistItemVerdict `json:"verdict" validate:"required,oneof=compliant n_a exception"`
	// Rationale: 任意の人間向け説明。exception 時は必須。
	// 500 字 cap (io-contracts.md §4KB overflow 棚卸し).
	Rationale string `json:"rationale,omitempty" validate:"omitempty,max=500"`
	// ExceptionReason: verdict=exception のときに何故違反したかの短い理由。
	ExceptionReason string `json:"exception_reason,omitempty" validate:"omitempty,max=300"`
}

// ChecklistResult is the top-level artifact at
// `<run>/{20-pass1|50-pass2}/<agent>/checklist-result.json`.
type ChecklistResult struct {
	SchemaVersion string          `json:"schema_version" validate:"required,oneof=1"`
	RunID         RunID           `json:"run_id" validate:"required,run_id_fmt"`
	Pass          int             `json:"pass" validate:"required,oneof=1 2"`
	Agent         AgentID         `json:"agent" validate:"required,agent_id_fmt"`
	Items         []ChecklistItem `json:"items" validate:"required,dive"`
}
