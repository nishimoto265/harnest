package judges

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/validation"
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
	ErrUnknownJudgeRole          = errors.New("judges: unknown judge role")
	ErrJudgeOutputMissingScores  = errors.New("judges: output must contain one score per dimension")
	ErrJudgeOutputDuplicateScore = errors.New("judges: output contains duplicate dimension score")
	ErrJudgeOutputIdentity       = errors.New("judges: output row identity mismatch")
	ErrJudgeOutputMissingInput   = errors.New("judges: output does not match input")
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
	RunID      contracts.RunID `json:"run_id"`
	Pass       int             `json:"pass"`
	Agent      contracts.AgentID
	OutputPath string `json:"output_path"`
	RubricPath string `json:"rubric_path"`
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
	return contracts.EnsureCleanAbsolutePath(in.RubricPath)
}

type JudgeOutput struct {
	Scores     []contracts.ScoreEntry      `json:"scores"`
	Compliance []contracts.ComplianceEntry `json:"compliance"`
	Arbiter    bool                        `json:"arbiter"`
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
	for _, compliance := range out.Compliance {
		if err := compliance.Validate(); err != nil {
			return err
		}
		if compliance.RunID != runID || compliance.Pass != pass || compliance.Agent != agent {
			return fmt.Errorf("%w: compliance rule_id=%s", ErrJudgeOutputIdentity, compliance.RuleID)
		}
	}
	return nil
}

func (out JudgeOutput) ValidateFor(input JudgeInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
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
	for _, compliance := range out.Compliance {
		if compliance.RunID != runID || compliance.Pass != pass || compliance.Agent != agent {
			return fmt.Errorf("%w: compliance rule_id=%s", ErrJudgeOutputIdentity, compliance.RuleID)
		}
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
	return nil
}

type stubJudge struct {
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
		Scores: scores,
		Compliance: []contracts.ComplianceEntry{
			{
				SchemaVersion: "1",
				RunID:         input.RunID,
				Pass:          input.Pass,
				Agent:         input.Agent,
				RuleID:        stubRuleID,
				Verdict:       contracts.ComplianceVerdictCompliant,
				Rationale:     fmt.Sprintf("stub %s fixture marks the output compliant.", j.role),
				VerdictPath:   verdictPath,
				RubricVersion: stubRubricVersion,
				PromptVersion: stubPromptVersion,
				ResolvedAt:    stubResolvedAt,
			},
		},
		Arbiter: j.role == RoleArbiter,
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
