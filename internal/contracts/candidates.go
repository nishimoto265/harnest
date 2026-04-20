package contracts

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// CandidateKind: new / update / duplicate (io-contracts.md §step40).
// Phase 0 の candidate classification は flat 3-way enum (discriminator は持つが
// variant 別 field 分岐は無し、下流 step70 で共通 schema を読むだけ).
type CandidateKind string

const (
	CandidateKindNew       CandidateKind = "new"
	CandidateKindUpdate    CandidateKind = "update"
	CandidateKindDuplicate CandidateKind = "duplicate"
)

// Candidate is a LLM-generated rule proposal from step40.
type Candidate struct {
	// CandidateID: step40 が採番する一時 ID (最終 rule_id とは別).
	CandidateID string        `json:"candidate_id" validate:"required"`
	Kind        CandidateKind `json:"kind" validate:"required,oneof=new update duplicate"`

	// TargetRuleID: kind=update / duplicate のとき参照される既存 rule_id.
	// kind=new のときは空.
	TargetRuleID string `json:"target_rule_id,omitempty" validate:"omitempty"`

	// Title: 短い rule 見出し.
	Title string `json:"title" validate:"required,max=200"`

	// Problem: 500 字 cap (io-contracts.md §4KB overflow 棚卸し).
	Problem            string       `json:"problem,omitempty" validate:"omitempty,max=500"`
	ProblemOverflowRef *OverflowRef `json:"problem_overflow_ref,omitempty" validate:"omitempty"`

	// Rationale: 500 字 cap.
	Rationale            string       `json:"rationale,omitempty" validate:"omitempty,max=500"`
	RationaleOverflowRef *OverflowRef `json:"rationale_overflow_ref,omitempty" validate:"omitempty"`

	// ProposedBodyPath: `<run>/40/candidates/<candidate_id>.md` 等への相対 path.
	// rule 本体 (長文) は sidecar に置き registry からも参照する (rev6).
	ProposedBodyPath   string `json:"proposed_body_path" validate:"required"`
	ProposedBodySha256 string `json:"proposed_body_sha256" validate:"required,sha256_hex"`
}

// Candidate.Validate enforces kind-specific target_rule_id invariants
// (Phase 0-bootstrap-1 gate 3rd-round finding #6):
//
//   - kind == update    → target_rule_id is REQUIRED (the rule being updated)
//   - kind == duplicate → target_rule_id is REQUIRED (the existing rule this
//     candidate duplicates)
//   - kind == new       → target_rule_id MUST be empty (a new rule has no
//     pre-existing target; non-empty values here are a schema-level mixup
//     between new and update that would confuse step70's promotion logic)
//
// For update / duplicate, the format follows the same sha256-or-short-ID
// convention used elsewhere; we only validate non-emptiness here and leave
// the exact character-set check to `validateStruct` tag rules at caller
// level. If a future revision introduces a dedicated `rule_id_fmt` tag, wire
// it through here too.
var (
	ErrCandidateTargetRequired  = errors.New("contracts: candidate: target_rule_id is required for kind=update/duplicate")
	ErrCandidateTargetForbidden = errors.New("contracts: candidate: target_rule_id must be empty for kind=new")
)

// Validate runs tag-based struct validation + kind-specific invariants.
func (c Candidate) Validate() error {
	if err := validateStruct(c); err != nil {
		return err
	}
	switch c.Kind {
	case CandidateKindUpdate, CandidateKindDuplicate:
		if c.TargetRuleID == "" {
			return fmt.Errorf("%w: candidate_id=%s kind=%s", ErrCandidateTargetRequired, c.CandidateID, c.Kind)
		}
	case CandidateKindNew:
		if c.TargetRuleID != "" {
			return fmt.Errorf("%w: candidate_id=%s target_rule_id=%q", ErrCandidateTargetForbidden, c.CandidateID, c.TargetRuleID)
		}
	default:
		// Tag-level oneof=new update duplicate should have caught this already.
		return fmt.Errorf("%w: %s", ErrUnknownCandidateKind, c.Kind)
	}
	return nil
}

// Candidates is the `<run>/40/candidates.json` document.
// 完了マーカー: candidates.json 存在 (io-contracts.md §completion marker).
type Candidates struct {
	SchemaVersion string `json:"schema_version" validate:"required,oneof=1"`
	RunID         RunID  `json:"run_id" validate:"required,run_id_fmt"`

	// Candidates: 0 件 (step40 無収穫) も許容.
	Candidates []Candidate `json:"candidates" validate:"dive"`

	// CandidatesHash: sha256 over canonical-JSON of `candidates[]`
	// (step70 idempotency_key に組み込まれる).
	CandidatesHash string `json:"candidates_hash" validate:"required,sha256_hex"`

	CreatedAt time.Time `json:"created_at" validate:"required"`
}

func (c *Candidates) UnmarshalJSON(data []byte) error {
	type alias Candidates
	var a alias
	if err := decodeStrict(data, &a); err != nil {
		return err
	}
	*c = Candidates(a)
	return c.Validate()
}

// Validate runs tag-based validation + per-Candidate kind invariants
// (finding #6). Candidates_hash content verification (i.e. hash == sha256 of
// canonical-JSON(candidates[])) requires the canonical-JSON implementation
// that lands in bootstrap-2; the API surface is frozen here to make the
// integration point unambiguous.
//
// TODO(bootstrap-2): once internal/canonicaljson lands, wire
// CandidatesHash verification through this method so the contract is
// end-to-end enforced on every decode/encode.
func (c Candidates) Validate() error {
	if c.CandidatesHash == "" {
		return ErrCandidatesHashMismatch
	}
	if err := validateStruct(c); err != nil {
		return err
	}
	for i := range c.Candidates {
		if err := c.Candidates[i].Validate(); err != nil {
			return fmt.Errorf("candidates[%d]: %w", i, err)
		}
	}
	return c.VerifyCandidatesHash()
}

// CanonicalCandidatesHash returns the step40/step70 canonical candidates hash:
// sha256 over the JSON encoding of the Candidates slice using Go struct field
// order (no map[string]any). This is the shared producer/verifier algorithm
// for `<run>/40/candidates.json`.
func CanonicalCandidatesHash(items []Candidate) string {
	data, err := CanonicalMarshal(items)
	if err != nil {
		panic(fmt.Sprintf("contracts: unexpected CanonicalMarshal failure: %v", err))
	}
	sum := sha256Sum(data)
	return sum
}

func sha256Sum(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// VerifyCandidatesHash verifies that candidates_hash matches the canonical hash
// of candidates[]. The canonical form is the JSON serialization of the
// Candidates slice with deterministic struct field ordering (via
// encoding/json on typed structs only, never map[string]any), followed by
// sha256 hex encoding.
//
// This method fails closed: an empty candidates_hash or a mismatched digest
// returns ErrCandidatesHashMismatch.
func (c Candidates) VerifyCandidatesHash() error {
	if c.CandidatesHash == "" {
		return fmt.Errorf("%w: empty candidates_hash", ErrCandidatesHashMismatch)
	}
	want := CanonicalCandidatesHash(c.Candidates)
	if c.CandidatesHash != want {
		return fmt.Errorf("%w: got=%s want=%s", ErrCandidatesHashMismatch, c.CandidatesHash, want)
	}
	return nil
}

// ClassificationEntry is one row appended to `<run>/40/classification.jsonl`.
// 1 候補 1 行 (io-contracts.md §step40).
type ClassificationEntry struct {
	SchemaVersion string        `json:"schema_version" validate:"required,oneof=1"`
	RunID         RunID         `json:"run_id" validate:"required,run_id_fmt"`
	CandidateID   string        `json:"candidate_id" validate:"required"`
	Kind          CandidateKind `json:"kind" validate:"required,oneof=new update duplicate"`

	// SimilarityScore: 類似度 (0..100 integer, float 禁止).
	SimilarityScore int `json:"similarity_score" validate:"gte=0,lte=100"`

	// MatchedRuleID: kind=update / duplicate のとき参照される既存 rule_id.
	MatchedRuleID string `json:"matched_rule_id,omitempty" validate:"omitempty"`

	// Rationale: 500 字 cap.
	Rationale            string       `json:"rationale,omitempty" validate:"omitempty,max=500"`
	RationaleOverflowRef *OverflowRef `json:"rationale_overflow_ref,omitempty" validate:"omitempty"`

	ClassifiedAt time.Time `json:"classified_at" validate:"required"`
}
