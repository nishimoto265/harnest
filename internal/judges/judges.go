package judges

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nishimoto265/harnest/internal/contracts"
	"github.com/nishimoto265/harnest/internal/validation"
)

type Role = contracts.JudgeRole

const (
	RolePrimary   = contracts.JudgeRolePrimary
	RoleSecondary = contracts.JudgeRoleSecondary
	RoleArbiter   = contracts.JudgeRoleArbiter
)

const (
	stubPromptVersion = "phase0-stub"
	stubRubricVersion = "default"
	stubRuleID        = "stub-rubric-rule"
)

var (
	ErrDefaultRubricUnresolved         = errors.New("judges: default rubric path could not be resolved")
	ErrUnknownJudgeRole                = errors.New("judges: unknown judge role")
	ErrJudgeOutputMissingScores        = errors.New("judges: output must contain one score per dimension")
	ErrJudgeOutputDuplicateScore       = errors.New("judges: output contains duplicate dimension score")
	ErrJudgeOutputDuplicateCompliance  = errors.New("judges: output contains duplicate compliance rule")
	ErrJudgeOutputMissingCompliance    = errors.New("judges: output missing expected compliance rule")
	ErrJudgeOutputUnexpectedCompliance = errors.New("judges: output contains unexpected compliance rule")
	ErrJudgeOutputIdentity             = errors.New("judges: output row identity mismatch")
	ErrJudgeOutputMissingInput         = errors.New("judges: output does not match input")
)

var (
	allDimensions = []contracts.Dimension{
		contracts.DimensionFidelity,
		contracts.DimensionCorrectness,
		contracts.DimensionMaintainability,
		contracts.DimensionDiscipline,
		contracts.DimensionCommunication,
	}
	stubResolvedAt = time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC)
)

// Judge produces scoring output for one agent output artifact.
// Phase 0 stubs return deterministic fixtures; Phase 1 will wire actual LLMs.
type Judge interface {
	ScoreOutput(ctx context.Context, input JudgeInput) (JudgeOutput, error)
}

type JudgeInput struct {
	RunID                     contracts.RunID `json:"run_id"`
	Pass                      int             `json:"pass"`
	Agent                     contracts.AgentID
	OutputPath                string          `json:"output_path"`
	RubricPath                string          `json:"rubric_path"`
	ExpectedComplianceRuleIDs []string        `json:"expected_compliance_rule_ids,omitempty"`
	EnforceExpectedCompliance bool            `json:"enforce_expected_compliance,omitempty"`
	CandidateRules            []CandidateRule `json:"candidate_rules,omitempty"`
}

type CandidateRule struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	TargetRuleID string `json:"target_rule_id,omitempty"`
	Title        string `json:"title"`
	Body         string `json:"body"`
}

type Issue struct {
	Severity       contracts.IssueSeverity `json:"severity"`
	Category       string                  `json:"category"`
	Title          string                  `json:"title"`
	Evidence       string                  `json:"evidence"`
	ProposedLesson string                  `json:"proposed_lesson"`
	ChecklistItem  string                  `json:"checklist_item"`
}

func (in JudgeInput) Validate() error {
	if err := validation.Instance().Var(in.RunID, "required,run_id_fmt"); err != nil {
		return err
	}
	if in.Pass != 1 && in.Pass != 2 {
		return fmt.Errorf("judges: pass must be 1 or 2: %d", in.Pass)
	}
	if err := validation.Instance().Var(in.Agent, "required,agent_id_fmt"); err != nil {
		return err
	}
	if err := contracts.EnsureCleanAbsolutePath(in.OutputPath); err != nil {
		return err
	}
	if err := contracts.EnsureCleanAbsolutePath(in.RubricPath); err != nil {
		return err
	}
	for _, ruleID := range in.ExpectedComplianceRuleIDs {
		if ruleID == "" {
			return errors.New("judges: expected compliance rule_id is empty")
		}
	}
	for _, rule := range in.CandidateRules {
		if rule.ID == "" {
			return errors.New("judges: candidate rule id is empty")
		}
	}
	return nil
}

type JudgeOutput struct {
	Scores     []contracts.ScoreEntry      `json:"scores"`
	Compliance []contracts.ComplianceEntry `json:"compliance"`
	Arbiter    bool                        `json:"arbiter"`
	Issues     []Issue                     `json:"issues,omitempty"`
}

func (out JudgeOutput) Validate() error {
	if len(out.Scores) != len(allDimensions) {
		return fmt.Errorf("%w: got=%d want=%d", ErrJudgeOutputMissingScores, len(out.Scores), len(allDimensions))
	}

	dimensions := make(map[contracts.Dimension]struct{}, len(allDimensions))
	var (
		runID contracts.RunID
		pass  int
		agent contracts.AgentID
	)
	for i, score := range out.Scores {
		if err := score.Validate(); err != nil {
			return err
		}
		if i == 0 {
			runID = score.RunID
			pass = score.Pass
			agent = score.Agent
		} else if score.RunID != runID || score.Pass != pass || score.Agent != agent {
			return fmt.Errorf("%w: score dimension=%s", ErrJudgeOutputIdentity, score.Dimension)
		}
		if _, exists := dimensions[score.Dimension]; exists {
			return fmt.Errorf("%w: dimension=%s", ErrJudgeOutputDuplicateScore, score.Dimension)
		}
		dimensions[score.Dimension] = struct{}{}
	}
	for _, dimension := range allDimensions {
		if _, ok := dimensions[dimension]; !ok {
			return fmt.Errorf("%w: dimension=%s", ErrJudgeOutputMissingScores, dimension)
		}
	}
	complianceRuleIDs := make(map[string]struct{}, len(out.Compliance))
	for _, compliance := range out.Compliance {
		if err := compliance.Validate(); err != nil {
			return err
		}
		if compliance.RunID != runID || compliance.Pass != pass || compliance.Agent != agent {
			return fmt.Errorf("%w: compliance rule_id=%s", ErrJudgeOutputIdentity, compliance.RuleID)
		}
		if _, exists := complianceRuleIDs[compliance.RuleID]; exists {
			return fmt.Errorf("%w: rule_id=%s", ErrJudgeOutputDuplicateCompliance, compliance.RuleID)
		}
		complianceRuleIDs[compliance.RuleID] = struct{}{}
	}
	return nil
}

func (out JudgeOutput) ValidateFor(input JudgeInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	if err := out.Validate(); err != nil {
		return err
	}
	for _, score := range out.Scores {
		if score.RunID != input.RunID || score.Pass != input.Pass || score.Agent != input.Agent {
			return fmt.Errorf("%w: score dimension=%s", ErrJudgeOutputMissingInput, score.Dimension)
		}
	}
	for _, compliance := range out.Compliance {
		if compliance.RunID != input.RunID || compliance.Pass != input.Pass || compliance.Agent != input.Agent {
			return fmt.Errorf("%w: compliance rule_id=%s", ErrJudgeOutputMissingInput, compliance.RuleID)
		}
	}
	if input.EnforceExpectedCompliance || len(input.ExpectedComplianceRuleIDs) > 0 {
		seen := make(map[string]struct{}, len(out.Compliance))
		for _, compliance := range out.Compliance {
			seen[compliance.RuleID] = struct{}{}
		}
		expected := make(map[string]struct{}, len(input.ExpectedComplianceRuleIDs))
		for _, ruleID := range input.ExpectedComplianceRuleIDs {
			expected[ruleID] = struct{}{}
		}
		for _, compliance := range out.Compliance {
			if _, ok := expected[compliance.RuleID]; !ok {
				return fmt.Errorf("%w: rule_id=%s", ErrJudgeOutputUnexpectedCompliance, compliance.RuleID)
			}
		}
		for _, ruleID := range input.ExpectedComplianceRuleIDs {
			if _, ok := seen[ruleID]; !ok {
				return fmt.Errorf("%w: rule_id=%s", ErrJudgeOutputMissingCompliance, ruleID)
			}
		}
	}
	return nil
}

type stubJudge struct {
	role Role
}

type stubViolationJudge struct {
	role Role
}

type stubAdoptJudge struct {
	role Role
}

func NewStub(role Role) (Judge, error) {
	switch role {
	case RolePrimary, RoleSecondary, RoleArbiter:
		return stubJudge{role: role}, nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnknownJudgeRole, role)
	}
}

func NewPrimaryStub() Judge {
	judge, _ := NewStub(RolePrimary)
	return judge
}

func NewSecondaryStub() Judge {
	judge, _ := NewStub(RoleSecondary)
	return judge
}

func NewArbiterStub() Judge {
	judge, _ := NewStub(RoleArbiter)
	return judge
}

func NewViolationStub(role Role) (Judge, error) {
	switch role {
	case RolePrimary, RoleSecondary, RoleArbiter:
		return stubViolationJudge{role: role}, nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnknownJudgeRole, role)
	}
}

func NewAdoptStub(role Role) (Judge, error) {
	switch role {
	case RolePrimary, RoleSecondary, RoleArbiter:
		return stubAdoptJudge{role: role}, nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnknownJudgeRole, role)
	}
}

func (j stubJudge) ScoreOutput(ctx context.Context, input JudgeInput) (JudgeOutput, error) {
	if err := input.Validate(); err != nil {
		return JudgeOutput{}, err
	}
	select {
	case <-ctx.Done():
		return JudgeOutput{}, ctx.Err()
	default:
	}

	verdictPath := contracts.VerdictPathSingle
	if j.role == RoleArbiter {
		verdictPath = contracts.VerdictPathArbitrated
	}

	scores := make([]contracts.ScoreEntry, 0, len(allDimensions))
	for _, dimension := range allDimensions {
		scores = append(scores, contracts.ScoreEntry{
			SchemaVersion: "1",
			RunID:         input.RunID,
			Pass:          input.Pass,
			Agent:         input.Agent,
			Dimension:     dimension,
			Score:         stubScoreFor(j.role, dimension),
			Reasons:       stubReasonFor(j.role, dimension),
			VerdictPath:   verdictPath,
			RubricVersion: stubRubricVersion,
			PromptVersion: stubPromptVersion,
			ResolvedAt:    stubResolvedAt,
		})
	}

	output := JudgeOutput{
		Scores:     scores,
		Compliance: makeStubComplianceEntries(input, verdictPath, contracts.ComplianceVerdictCompliant, fmt.Sprintf("stub %s fixture marks the output compliant.", j.role), stubRuleID),
		Arbiter:    j.role == RoleArbiter,
	}
	if err := output.ValidateFor(input); err != nil {
		return JudgeOutput{}, err
	}
	return output, nil
}

func makeStubComplianceEntries(input JudgeInput, verdictPath contracts.VerdictPath, verdict contracts.ComplianceVerdict, rationale string, fallbackRuleID string) []contracts.ComplianceEntry {
	ruleIDs := input.ExpectedComplianceRuleIDs
	if len(ruleIDs) == 0 {
		if input.EnforceExpectedCompliance {
			return nil
		}
		ruleIDs = []string{fallbackRuleID}
	}
	entries := make([]contracts.ComplianceEntry, 0, len(ruleIDs))
	for _, ruleID := range ruleIDs {
		entries = append(entries, contracts.ComplianceEntry{
			SchemaVersion: "1",
			RunID:         input.RunID,
			Pass:          input.Pass,
			Agent:         input.Agent,
			RuleID:        ruleID,
			Verdict:       verdict,
			Rationale:     rationale,
			VerdictPath:   verdictPath,
			RubricVersion: stubRubricVersion,
			PromptVersion: stubPromptVersion,
			ResolvedAt:    stubResolvedAt,
		})
	}
	return entries
}

func (j stubViolationJudge) ScoreOutput(ctx context.Context, input JudgeInput) (JudgeOutput, error) {
	if err := input.Validate(); err != nil {
		return JudgeOutput{}, err
	}
	select {
	case <-ctx.Done():
		return JudgeOutput{}, ctx.Err()
	default:
	}

	verdictPath := contracts.VerdictPathSingle
	if j.role == RoleArbiter {
		verdictPath = contracts.VerdictPathArbitrated
	}

	scores := make([]contracts.ScoreEntry, 0, len(allDimensions))
	for _, dimension := range allDimensions {
		scores = append(scores, contracts.ScoreEntry{
			SchemaVersion: "1",
			RunID:         input.RunID,
			Pass:          input.Pass,
			Agent:         input.Agent,
			Dimension:     dimension,
			Score:         60,
			Reasons:       fmt.Sprintf("agent output still violates e2e stub rule on %s", dimension),
			VerdictPath:   verdictPath,
			RubricVersion: stubRubricVersion,
			PromptVersion: stubPromptVersion,
			ResolvedAt:    stubResolvedAt,
		})
	}

	output := JudgeOutput{
		Scores:     scores,
		Compliance: makeStubComplianceEntries(input, verdictPath, contracts.ComplianceVerdictViolated, fmt.Sprintf("stub violation fixture for %s requires explicit remediation", j.role), "e2e-violation-rule"),
		Arbiter:    j.role == RoleArbiter,
	}
	if err := output.ValidateFor(input); err != nil {
		return JudgeOutput{}, err
	}
	return output, nil
}

func (j stubAdoptJudge) ScoreOutput(ctx context.Context, input JudgeInput) (JudgeOutput, error) {
	if err := input.Validate(); err != nil {
		return JudgeOutput{}, err
	}
	select {
	case <-ctx.Done():
		return JudgeOutput{}, ctx.Err()
	default:
	}
	verdictPath := contracts.VerdictPathSingle
	if j.role == RoleArbiter {
		verdictPath = contracts.VerdictPathArbitrated
	}
	score := 55
	verdict := contracts.ComplianceVerdictViolated
	rationale := "pass1 intentionally fails the deterministic adopt stub rule"
	if input.Pass == 2 {
		score = 95
		verdict = contracts.ComplianceVerdictCompliant
		rationale = "pass2 satisfies the deterministic adopt stub rule"
	}
	scores := make([]contracts.ScoreEntry, 0, len(allDimensions))
	for _, dimension := range allDimensions {
		scores = append(scores, contracts.ScoreEntry{
			SchemaVersion: "1",
			RunID:         input.RunID,
			Pass:          input.Pass,
			Agent:         input.Agent,
			Dimension:     dimension,
			Score:         score,
			Reasons:       fmt.Sprintf("deterministic adopt stub %s score for %s", j.role, dimension),
			VerdictPath:   verdictPath,
			RubricVersion: stubRubricVersion,
			PromptVersion: stubPromptVersion,
			ResolvedAt:    stubResolvedAt,
		})
	}
	output := JudgeOutput{
		Scores:     scores,
		Compliance: makeStubComplianceEntries(input, verdictPath, verdict, rationale, "adopt-stub-rule"),
		Arbiter:    j.role == RoleArbiter,
	}
	if err := output.ValidateFor(input); err != nil {
		return JudgeOutput{}, err
	}
	return output, nil
}

func stubScoreFor(role Role, dimension contracts.Dimension) int {
	switch role {
	case RolePrimary:
		switch dimension {
		case contracts.DimensionFidelity:
			return 84
		case contracts.DimensionCorrectness:
			return 82
		case contracts.DimensionMaintainability:
			return 80
		case contracts.DimensionDiscipline:
			return 86
		case contracts.DimensionCommunication:
			return 78
		}
	case RoleSecondary:
		switch dimension {
		case contracts.DimensionFidelity:
			return 83
		case contracts.DimensionCorrectness:
			return 81
		case contracts.DimensionMaintainability:
			return 79
		case contracts.DimensionDiscipline:
			return 85
		case contracts.DimensionCommunication:
			return 77
		}
	case RoleArbiter:
		switch dimension {
		case contracts.DimensionFidelity:
			return 85
		case contracts.DimensionCorrectness:
			return 84
		case contracts.DimensionMaintainability:
			return 82
		case contracts.DimensionDiscipline:
			return 87
		case contracts.DimensionCommunication:
			return 80
		}
	}
	return 0
}

func stubReasonFor(role Role, dimension contracts.Dimension) string {
	return fmt.Sprintf("stub %s fixture evaluated %s with deterministic phase-0 scoring.", role, dimension)
}
