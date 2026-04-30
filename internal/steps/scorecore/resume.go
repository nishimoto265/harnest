package scorecore

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/judges"
)

func sortRuleIDs(ids []string) { sort.Strings(ids) }

var (
	ErrPanelJudgeRequired       = errors.New("scorecore: judge is required")
	ErrPanelArbiterRowsRequired = errors.New("scorecore: arbiter rows are required for disagreement resolution")
)

// RoleResult captures raw rows produced by a single judge role.
type RoleResult struct {
	RawScores     []contracts.RawScoreEntry
	RawCompliance []contracts.RawComplianceEntry
	Issues        []contracts.IssueEntry
}

func (in PanelInput) validateCommon() error {
	if len(in.OutputSha256) != sha256HexLength {
		return fmt.Errorf("%w: len=%d", ErrPanelOutputSha256, len(in.OutputSha256))
	}
	for _, r := range in.OutputSha256 {
		if !isHexDigit(r) {
			return fmt.Errorf("%w: value=%q", ErrPanelOutputSha256, in.OutputSha256)
		}
	}
	if in.DisagreementThreshold < 0 {
		return fmt.Errorf("%w: %d", ErrPanelThreshold, in.DisagreementThreshold)
	}
	if in.StepDir != "30" && in.StepDir != "60" {
		return fmt.Errorf("%w: step_dir=%q", ErrPanelStepDir, in.StepDir)
	}
	return nil
}

// ResolveRole runs exactly one judge role and converts its output into raw
// rows. Arbiter refs are derived from the provided latest primary/secondary
// raw rows.
func (r *PanelResolver) ResolveRole(
	ctx context.Context,
	in PanelInput,
	role contracts.JudgeRole,
	judge judges.Judge,
	primaryScores, secondaryScores []contracts.RawScoreEntry,
	primaryCompliance, secondaryCompliance []contracts.RawComplianceEntry,
) (RoleResult, error) {
	if err := in.validateCommon(); err != nil {
		return RoleResult{}, err
	}
	if judge == nil {
		return RoleResult{}, fmt.Errorf("%w: role=%s", ErrPanelJudgeRequired, role)
	}

	out, err := judge.ScoreOutput(ctx, in.JudgeInput)
	if err != nil {
		return RoleResult{}, fmt.Errorf("scorecore: %s: %w", role, err)
	}
	if err := out.ValidateFor(in.JudgeInput); err != nil {
		return RoleResult{}, fmt.Errorf("scorecore: %s: %w", role, err)
	}

	var (
		primaryRefs             map[contracts.Dimension]*contracts.RawJudgeRef
		secondaryRefs           map[contracts.Dimension]*contracts.RawJudgeRef
		primaryComplianceRefs   map[string]*contracts.RawJudgeRef
		secondaryComplianceRefs map[string]*contracts.RawJudgeRef
	)
	if role == contracts.JudgeRoleArbiter {
		primaryRefs, err = refsByDimension(primaryScores, contracts.JudgeRolePrimary)
		if err != nil {
			return RoleResult{}, err
		}
		secondaryRefs, err = refsByDimension(secondaryScores, contracts.JudgeRoleSecondary)
		if err != nil {
			return RoleResult{}, err
		}
		primaryComplianceRefs, err = complianceRefsByRule(primaryCompliance, contracts.JudgeRolePrimary)
		if err != nil {
			return RoleResult{}, err
		}
		secondaryComplianceRefs, err = complianceRefsByRule(secondaryCompliance, contracts.JudgeRoleSecondary)
		if err != nil {
			return RoleResult{}, err
		}
	}

	rawScores, err := buildRawScoreEntries(out, in, role, primaryRefs, secondaryRefs)
	if err != nil {
		return RoleResult{}, err
	}
	rawCompliance, err := buildRawComplianceEntries(out, in, role, primaryComplianceRefs, secondaryComplianceRefs)
	if err != nil {
		return RoleResult{}, err
	}
	issues, err := buildIssueEntries(out, in, role)
	if err != nil {
		return RoleResult{}, err
	}
	return RoleResult{
		RawScores:     rawScores,
		RawCompliance: rawCompliance,
		Issues:        issues,
	}, nil
}

// BuildFinalResultFromRaw derives the final layer from the latest raw rows for
// one agent. Callers are expected to supply already-collapsed fresh raw rows.
func BuildFinalResultFromRaw(
	primaryScores, secondaryScores, arbiterScores []contracts.RawScoreEntry,
	primaryCompliance, secondaryCompliance, arbiterCompliance []contracts.RawComplianceEntry,
	threshold int,
	secondaryPresent, arbiterPresent bool,
) (PanelResult, error) {
	if !secondaryPresent {
		return assembleFinalFromRaw(primaryScores, primaryCompliance, contracts.VerdictPathSingle), nil
	}

	disagree, err := PanelDisagrees(primaryScores, secondaryScores, primaryCompliance, secondaryCompliance, threshold)
	if err != nil {
		return PanelResult{}, err
	}
	if !disagree {
		return assembleFinalFromRaw(primaryScores, primaryCompliance, contracts.VerdictPathAgreement), nil
	}
	if !arbiterPresent {
		return PanelResult{}, ErrPanelArbiterRequired
	}
	// Arbiter scores are always required (one row per dimension). Arbiter
	// compliance rows are only required when the primary/secondary panel
	// actually disagrees on at least one rule — see F5 for the rationale
	// (step60 switched to disputed-only arbiter contract in r15; step30
	// shares the same primitives via scorecore).
	if len(arbiterScores) == 0 {
		return PanelResult{}, ErrPanelArbiterRowsRequired
	}
	disputed := disputedComplianceRuleIDsFromRaw(primaryCompliance, secondaryCompliance)
	if len(disputed) > 0 && len(arbiterCompliance) == 0 {
		return PanelResult{}, ErrPanelArbiterRowsRequired
	}
	if err := validateArbiterComplianceCoverage(primaryCompliance, secondaryCompliance, arbiterCompliance); err != nil {
		return PanelResult{}, err
	}

	verdict := classifyArbiterVerdict(
		primaryScores,
		secondaryScores,
		arbiterScores,
		primaryCompliance,
		secondaryCompliance,
		arbiterCompliance,
		threshold,
	)
	return PanelResult{
		RawScores:       concatRawScores(primaryScores, secondaryScores, arbiterScores),
		RawCompliance:   concatRawCompliance(primaryCompliance, secondaryCompliance, arbiterCompliance),
		FinalScores:     finalScoresFromRaw(arbiterScores, verdict),
		FinalCompliance: finalizeComplianceDisputedOnly(primaryCompliance, secondaryCompliance, arbiterCompliance, verdict),
		VerdictPath:     verdict,
	}, nil
}

// finalizeComplianceDisputedOnly builds the final compliance set for the
// disputed-only arbiter contract: agreement rules finalize from primary,
// disputed rules finalize from arbiter. This keeps the contract identical
// to step60's rebuildFinalComplianceFromRaw path.
func finalizeComplianceDisputedOnly(
	primary, secondary, arbiter []contracts.RawComplianceEntry,
	verdict contracts.VerdictPath,
) []contracts.ComplianceEntry {
	primaryByRule := make(map[string]contracts.RawComplianceEntry, len(primary))
	for _, row := range primary {
		primaryByRule[row.RuleID] = row
	}
	secondaryByRule := make(map[string]contracts.RawComplianceEntry, len(secondary))
	for _, row := range secondary {
		secondaryByRule[row.RuleID] = row
	}
	arbiterByRule := make(map[string]contracts.RawComplianceEntry, len(arbiter))
	for _, row := range arbiter {
		arbiterByRule[row.RuleID] = row
	}

	allRules := make(map[string]struct{}, len(primaryByRule)+len(secondaryByRule)+len(arbiterByRule))
	for id := range primaryByRule {
		allRules[id] = struct{}{}
	}
	for id := range secondaryByRule {
		allRules[id] = struct{}{}
	}
	for id := range arbiterByRule {
		allRules[id] = struct{}{}
	}
	sortedIDs := make([]string, 0, len(allRules))
	for id := range allRules {
		sortedIDs = append(sortedIDs, id)
	}
	sortRuleIDs(sortedIDs)

	out := make([]contracts.ComplianceEntry, 0, len(sortedIDs))
	for _, ruleID := range sortedIDs {
		pRow, pOK := primaryByRule[ruleID]
		sRow, sOK := secondaryByRule[ruleID]
		aRow, aOK := arbiterByRule[ruleID]
		switch {
		case pOK && sOK && pRow.Verdict == sRow.Verdict:
			// Agreement — arbiter row (if present) is dead weight; finalize
			// from primary with the shared verdict path.
			out = append(out, finalComplianceFromRaw([]contracts.RawComplianceEntry{pRow}, verdict)...)
		case aOK:
			// Disputed or single-side rule with arbiter coverage.
			out = append(out, finalComplianceFromRaw([]contracts.RawComplianceEntry{aRow}, verdict)...)
		case pOK:
			out = append(out, finalComplianceFromRaw([]contracts.RawComplianceEntry{pRow}, verdict)...)
		case sOK:
			out = append(out, finalComplianceFromRaw([]contracts.RawComplianceEntry{sRow}, verdict)...)
		}
	}
	return out
}
