package scorecore

import (
	"context"
	"errors"
	"fmt"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/judges"
)

var (
	ErrPanelJudgeRequired       = errors.New("scorecore: judge is required")
	ErrPanelArbiterRowsRequired = errors.New("scorecore: arbiter rows are required for disagreement resolution")
)

// RoleResult captures raw rows produced by a single judge role.
type RoleResult struct {
	RawScores     []contracts.RawScoreEntry
	RawCompliance []contracts.RawComplianceEntry
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
	return RoleResult{
		RawScores:     rawScores,
		RawCompliance: rawCompliance,
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
	if len(arbiterScores) == 0 || (len(primaryCompliance) > 0 && len(arbiterCompliance) == 0) {
		return PanelResult{}, ErrPanelArbiterRowsRequired
	}
	if err := validateArbiterComplianceCoverage(primaryCompliance, secondaryCompliance, arbiterCompliance); err != nil {
		return PanelResult{}, err
	}

	verdict := classifyArbiterVerdict(primaryScores, secondaryScores, arbiterScores, threshold)
	return PanelResult{
		RawScores:       concatRawScores(primaryScores, secondaryScores, arbiterScores),
		RawCompliance:   concatRawCompliance(primaryCompliance, secondaryCompliance, arbiterCompliance),
		FinalScores:     finalScoresFromRaw(arbiterScores, verdict),
		FinalCompliance: finalComplianceFromRaw(arbiterCompliance, verdict),
		VerdictPath:     verdict,
	}, nil
}
