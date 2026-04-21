// Package scorecore provides shared primitives used by step30 and step60 for
// panel-based scoring: primary + secondary + (optional) arbiter judge
// resolution, sidecar overflow handling, and cardinality-verified done.marker
// construction.
package scorecore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/judges"
)

// Default free-text caps (mirrors contracts.ScoreEntry / ComplianceEntry
// validators). Panel resolver spills to sidecar above these thresholds.
const (
	ReasonsMaxChars   = 1000
	RationaleMaxChars = 500
)

// Sentinel errors surfaced by panel resolution.
var (
	ErrPanelPrimaryRequired     = errors.New("scorecore: primary judge is required")
	ErrPanelOutputSha256        = errors.New("scorecore: output_sha256 is required and must be sha256 hex")
	ErrPanelStepDir             = errors.New("scorecore: stepDir must be \"30\" or \"60\"")
	ErrPanelThreshold           = errors.New("scorecore: disagreement threshold must be >= 0")
	ErrPanelDimensionMatch      = errors.New("scorecore: primary and secondary dimension sets must match")
	ErrPanelArbiterRequired     = errors.New("scorecore: arbiter judge is required for disagreement resolution")
	ErrPanelArbiterRuleCoverage = errors.New("scorecore: arbiter compliance rule coverage mismatch")
)

// PanelInput carries all per-agent inputs needed by Resolve.
type PanelInput struct {
	Primary   judges.Judge
	Secondary judges.Judge
	Arbiter   judges.Judge

	JudgeInput    judges.JudgeInput
	OutputSha256  string
	RubricVersion string
	PromptVersion string

	// DisagreementThreshold is the per-dimension |primary - secondary| tolerance.
	// If any dimension exceeds the threshold the arbiter fires (when set).
	DisagreementThreshold int

	// RunContext / StepDir are needed to write overflow sidecars under
	// <run>/<StepDir>/reasons/ when reasons or rationale exceed their cap.
	RunContext internalio.RunContext
	StepDir    string
}

func (in PanelInput) validate() error {
	if in.Primary == nil {
		return ErrPanelPrimaryRequired
	}
	return in.validateCommon()
}

// PanelResult is everything produced for one (pass, agent).
// Raw layer rows go to scores-{A,B}-raw.jsonl / compliance-{A,B}-raw.jsonl;
// final rows go to scores-{A,B}.jsonl / compliance-{A,B}.jsonl. VerdictPath
// reports which branch was taken (single / agreement / arbitrated /
// arbiter_overruled).
type PanelResult struct {
	RawScores       []contracts.RawScoreEntry
	RawCompliance   []contracts.RawComplianceEntry
	FinalScores     []contracts.ScoreEntry
	FinalCompliance []contracts.ComplianceEntry
	VerdictPath     contracts.VerdictPath
}

// PanelResolver runs a primary + secondary (+ arbiter) panel for one agent.
type PanelResolver struct{}

// NewPanelResolver returns a zero-value resolver. Stateless — safe to share.
func NewPanelResolver() *PanelResolver { return &PanelResolver{} }

// Resolve runs the panel. Side effects: may write overflow sidecar files under
// <run>/<StepDir>/reasons/ when primary/secondary/arbiter reasons or
// compliance rationales exceed their caps.
func (r *PanelResolver) Resolve(ctx context.Context, in PanelInput) (PanelResult, error) {
	if err := in.validate(); err != nil {
		return PanelResult{}, err
	}

	primaryOut, err := in.Primary.ScoreOutput(ctx, in.JudgeInput)
	if err != nil {
		return PanelResult{}, fmt.Errorf("scorecore: primary: %w", err)
	}
	if err := primaryOut.ValidateFor(in.JudgeInput); err != nil {
		return PanelResult{}, fmt.Errorf("scorecore: primary: %w", err)
	}

	primaryRaw, err := buildRawScoreEntries(primaryOut, in, contracts.JudgeRolePrimary, nil, nil)
	if err != nil {
		return PanelResult{}, err
	}
	primaryRawCompliance, err := buildRawComplianceEntries(primaryOut, in, contracts.JudgeRolePrimary, nil, nil)
	if err != nil {
		return PanelResult{}, err
	}

	if in.Secondary == nil {
		// Single-judge path.
		return assembleFinalFromRaw(primaryRaw, primaryRawCompliance, contracts.VerdictPathSingle), nil
	}

	secondaryOut, err := in.Secondary.ScoreOutput(ctx, in.JudgeInput)
	if err != nil {
		return PanelResult{}, fmt.Errorf("scorecore: secondary: %w", err)
	}
	if err := secondaryOut.ValidateFor(in.JudgeInput); err != nil {
		return PanelResult{}, fmt.Errorf("scorecore: secondary: %w", err)
	}

	secondaryRaw, err := buildRawScoreEntries(secondaryOut, in, contracts.JudgeRoleSecondary, nil, nil)
	if err != nil {
		return PanelResult{}, err
	}
	secondaryRawCompliance, err := buildRawComplianceEntries(secondaryOut, in, contracts.JudgeRoleSecondary, nil, nil)
	if err != nil {
		return PanelResult{}, err
	}

	disagree, err := PanelDisagrees(primaryRaw, secondaryRaw, primaryRawCompliance, secondaryRawCompliance, in.DisagreementThreshold)
	if err != nil {
		return PanelResult{}, err
	}

	if !disagree {
		verdict := contracts.VerdictPathAgreement
		result := PanelResult{
			RawScores:     append(append([]contracts.RawScoreEntry{}, primaryRaw...), secondaryRaw...),
			RawCompliance: append(append([]contracts.RawComplianceEntry{}, primaryRawCompliance...), secondaryRawCompliance...),
			VerdictPath:   verdict,
		}
		// Pick primary as canonical final row in agreement path.
		result.FinalScores = finalScoresFromRaw(primaryRaw, verdict)
		result.FinalCompliance = finalComplianceFromRaw(primaryRawCompliance, verdict)
		return result, nil
	}
	if in.Arbiter == nil {
		return PanelResult{}, ErrPanelArbiterRequired
	}

	// Arbiter path. Compute refs against primary/secondary rows.
	primaryRefs, err := refsByDimension(primaryRaw, contracts.JudgeRolePrimary)
	if err != nil {
		return PanelResult{}, err
	}
	secondaryRefs, err := refsByDimension(secondaryRaw, contracts.JudgeRoleSecondary)
	if err != nil {
		return PanelResult{}, err
	}
	primaryComplianceRefs, err := complianceRefsByRule(primaryRawCompliance, contracts.JudgeRolePrimary)
	if err != nil {
		return PanelResult{}, err
	}
	secondaryComplianceRefs, err := complianceRefsByRule(secondaryRawCompliance, contracts.JudgeRoleSecondary)
	if err != nil {
		return PanelResult{}, err
	}

	arbiterOut, err := in.Arbiter.ScoreOutput(ctx, in.JudgeInput)
	if err != nil {
		return PanelResult{}, fmt.Errorf("scorecore: arbiter: %w", err)
	}
	if err := arbiterOut.ValidateFor(in.JudgeInput); err != nil {
		return PanelResult{}, fmt.Errorf("scorecore: arbiter: %w", err)
	}
	arbiterRaw, err := buildRawScoreEntries(arbiterOut, in, contracts.JudgeRoleArbiter, primaryRefs, secondaryRefs)
	if err != nil {
		return PanelResult{}, err
	}
	arbiterRawCompliance, err := buildRawComplianceEntries(arbiterOut, in, contracts.JudgeRoleArbiter, primaryComplianceRefs, secondaryComplianceRefs)
	if err != nil {
		return PanelResult{}, err
	}
	if err := validateArbiterComplianceCoverage(primaryRawCompliance, secondaryRawCompliance, arbiterRawCompliance); err != nil {
		return PanelResult{}, err
	}

	verdict := classifyArbiterVerdict(primaryRaw, secondaryRaw, arbiterRaw, in.DisagreementThreshold)

	result := PanelResult{
		RawScores:       concatRawScores(primaryRaw, secondaryRaw, arbiterRaw),
		RawCompliance:   concatRawCompliance(primaryRawCompliance, secondaryRawCompliance, arbiterRawCompliance),
		FinalScores:     finalScoresFromRaw(arbiterRaw, verdict),
		FinalCompliance: finalComplianceFromRaw(arbiterRawCompliance, verdict),
		VerdictPath:     verdict,
	}
	return result, nil
}

func concatRawScores(xs ...[]contracts.RawScoreEntry) []contracts.RawScoreEntry {
	total := 0
	for _, x := range xs {
		total += len(x)
	}
	out := make([]contracts.RawScoreEntry, 0, total)
	for _, x := range xs {
		out = append(out, x...)
	}
	return out
}

func concatRawCompliance(xs ...[]contracts.RawComplianceEntry) []contracts.RawComplianceEntry {
	total := 0
	for _, x := range xs {
		total += len(x)
	}
	out := make([]contracts.RawComplianceEntry, 0, total)
	for _, x := range xs {
		out = append(out, x...)
	}
	return out
}

func assembleFinalFromRaw(
	rawScores []contracts.RawScoreEntry,
	rawCompliance []contracts.RawComplianceEntry,
	verdict contracts.VerdictPath,
) PanelResult {
	return PanelResult{
		RawScores:       append([]contracts.RawScoreEntry{}, rawScores...),
		RawCompliance:   append([]contracts.RawComplianceEntry{}, rawCompliance...),
		FinalScores:     finalScoresFromRaw(rawScores, verdict),
		FinalCompliance: finalComplianceFromRaw(rawCompliance, verdict),
		VerdictPath:     verdict,
	}
}

func finalScoresFromRaw(raws []contracts.RawScoreEntry, verdict contracts.VerdictPath) []contracts.ScoreEntry {
	out := make([]contracts.ScoreEntry, 0, len(raws))
	for _, raw := range raws {
		out = append(out, contracts.ScoreEntry{
			SchemaVersion:      raw.SchemaVersion,
			RunID:              raw.RunID,
			Pass:               raw.Pass,
			Agent:              raw.Agent,
			Dimension:          raw.Dimension,
			Score:              raw.Score,
			Reasons:            raw.Reasons,
			ReasonsOverflowRef: raw.ReasonsOverflowRef,
			VerdictPath:        verdict,
			RubricVersion:      raw.RubricVersion,
			PromptVersion:      raw.PromptVersion,
			ResolvedAt:         raw.ResolvedAt,
		})
	}
	return out
}

func finalComplianceFromRaw(raws []contracts.RawComplianceEntry, verdict contracts.VerdictPath) []contracts.ComplianceEntry {
	out := make([]contracts.ComplianceEntry, 0, len(raws))
	for _, raw := range raws {
		out = append(out, contracts.ComplianceEntry{
			SchemaVersion:        raw.SchemaVersion,
			RunID:                raw.RunID,
			Pass:                 raw.Pass,
			Agent:                raw.Agent,
			RuleID:               raw.RuleID,
			Verdict:              raw.Verdict,
			Rationale:            raw.Rationale,
			RationaleOverflowRef: raw.RationaleOverflowRef,
			VerdictPath:          verdict,
			RubricVersion:        raw.RubricVersion,
			PromptVersion:        raw.PromptVersion,
			ResolvedAt:           raw.ResolvedAt,
		})
	}
	return out
}

func anyDimensionDisagrees(primary, secondary []contracts.RawScoreEntry, threshold int) (bool, error) {
	if len(primary) != len(secondary) {
		return false, fmt.Errorf("%w: primary=%d secondary=%d", ErrPanelDimensionMatch, len(primary), len(secondary))
	}
	secondaryByDim := make(map[contracts.Dimension]int, len(secondary))
	for _, s := range secondary {
		secondaryByDim[s.Dimension] = s.Score
	}
	for _, p := range primary {
		s, ok := secondaryByDim[p.Dimension]
		if !ok {
			return false, fmt.Errorf("%w: secondary missing dimension=%s", ErrPanelDimensionMatch, p.Dimension)
		}
		if abs(p.Score-s) > threshold {
			return true, nil
		}
	}
	return false, nil
}

// classifyArbiterVerdict decides between arbitrated and arbiter_overruled.
// "arbiter_overruled" means the arbiter is decisively different from BOTH
// primary AND secondary (per any single dimension exceeds 2*threshold) — the
// arbiter actively overrode the panel rather than siding with either.
func classifyArbiterVerdict(primary, secondary, arbiter []contracts.RawScoreEntry, threshold int) contracts.VerdictPath {
	primByDim := map[contracts.Dimension]int{}
	for _, p := range primary {
		primByDim[p.Dimension] = p.Score
	}
	secByDim := map[contracts.Dimension]int{}
	for _, s := range secondary {
		secByDim[s.Dimension] = s.Score
	}
	decisive := 2 * threshold
	overruledAll := true
	for _, a := range arbiter {
		pDiff := abs(a.Score - primByDim[a.Dimension])
		sDiff := abs(a.Score - secByDim[a.Dimension])
		if pDiff <= decisive || sDiff <= decisive {
			overruledAll = false
			break
		}
	}
	if overruledAll && len(arbiter) > 0 {
		return contracts.VerdictPathArbiterOverruled
	}
	return contracts.VerdictPathArbitrated
}

func refsByDimension(raws []contracts.RawScoreEntry, role contracts.JudgeRole) (map[contracts.Dimension]*contracts.RawJudgeRef, error) {
	out := make(map[contracts.Dimension]*contracts.RawJudgeRef, len(raws))
	for _, raw := range raws {
		sum, err := rawScoreSha256(raw)
		if err != nil {
			return nil, err
		}
		out[raw.Dimension] = &contracts.RawJudgeRef{Role: role, Sha256: sum}
	}
	return out, nil
}

func complianceRefsByRule(raws []contracts.RawComplianceEntry, role contracts.JudgeRole) (map[string]*contracts.RawJudgeRef, error) {
	out := make(map[string]*contracts.RawJudgeRef, len(raws))
	for _, raw := range raws {
		sum, err := rawComplianceSha256(raw)
		if err != nil {
			return nil, err
		}
		out[raw.RuleID] = &contracts.RawJudgeRef{Role: role, Sha256: sum}
	}
	return out, nil
}

func rawScoreSha256(raw contracts.RawScoreEntry) (string, error) {
	data, err := contracts.CanonicalMarshal(raw)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func rawComplianceSha256(raw contracts.RawComplianceEntry) (string, error) {
	data, err := contracts.CanonicalMarshal(raw)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func isHexDigit(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}

const sha256HexLength = 64
