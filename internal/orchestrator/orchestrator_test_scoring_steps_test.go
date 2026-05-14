package orchestrator

import (
	"context"
	"github.com/nishimoto265/harnest/internal/contracts"
	"github.com/nishimoto265/harnest/internal/contracts/stepio"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/nishimoto265/harnest/internal/judges"
	"github.com/nishimoto265/harnest/internal/steps/step60_scorepairwise"
	"time"
)

type scriptedStep60Step struct {
	decode func([]byte, any) (any, error)
}

func (s scriptedStep60Step) Run(ctx context.Context, run *StepRunContext) error {
	pass1RubricVersion := step30RubricVersionForStep60(run.IO)
	if err := step60_scorepairwise.Run(ctx, step60_scorepairwise.Input{
		IO:            run.IO,
		TaskPackage:   run.TaskPackage,
		RubricVersion: pass1RubricVersion,
		Primary:       orchestratorJudge{score: 95},
		Secondary:     orchestratorJudge{score: 94},
		Arbiter:       orchestratorJudge{score: 95},
	}); err != nil {
		return err
	}
	if s.decode == nil {
		return nil
	}
	scorableAgents, err := step60ScorableAgents(run.IO, run.TaskPackage)
	if err != nil {
		return err
	}
	versions, err := step60ScoringVersions(run.IO)
	if err != nil {
		return err
	}
	req := stepio.Step60Request{
		TaskPackage:    *run.TaskPackage,
		ScorableAgents: scorableAgents,
		RubricVersion:  versions.RubricVersion,
		PromptVersion:  versions.PromptVersion,
	}
	markerPath, err := run.IO.ResolveRunRelative("60/done.marker")
	if err != nil {
		return err
	}
	marker, err := internalio.ReadJSON[contracts.Step60DoneMarker](markerPath)
	if err != nil {
		return err
	}
	resp := stepio.Step60Response{
		RunID:           run.IO.RunID,
		ScoresCount:     int(marker.ExpectedCounts.Scores),
		ComplianceCount: int(marker.ExpectedCounts.Compliance),
		PairwiseCount:   int(marker.ExpectedCounts.Pairwise),
		ResolvedAt:      marker.ResolvedAt,
	}
	payload, err := contracts.MarshalStrict(resp)
	if err != nil {
		return err
	}
	_, err = s.decode(payload, req)
	return err
}

type orchestratorJudge struct {
	score int
}

func (j orchestratorJudge) ScoreOutput(ctx context.Context, input judges.JudgeInput) (judges.JudgeOutput, error) {
	if err := input.Validate(); err != nil {
		return judges.JudgeOutput{}, err
	}
	select {
	case <-ctx.Done():
		return judges.JudgeOutput{}, ctx.Err()
	default:
	}
	scores := make([]contracts.ScoreEntry, 0, 5)
	dimensions := []contracts.Dimension{
		contracts.DimensionFidelity,
		contracts.DimensionCorrectness,
		contracts.DimensionMaintainability,
		contracts.DimensionDiscipline,
		contracts.DimensionCommunication,
	}
	for _, dimension := range dimensions {
		scores = append(scores, contracts.ScoreEntry{
			SchemaVersion: "1",
			RunID:         input.RunID,
			Pass:          input.Pass,
			Agent:         input.Agent,
			Dimension:     dimension,
			Score:         j.score,
			Reasons:       "scripted adopt score",
			VerdictPath:   contracts.VerdictPathSingle,
			RubricVersion: "default",
			PromptVersion: "phase0-stub",
			ResolvedAt:    time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
		})
	}
	ruleIDs := input.ExpectedComplianceRuleIDs
	if len(ruleIDs) == 0 {
		ruleIDs = []string{"shared"}
	}
	compliance := make([]contracts.ComplianceEntry, 0, len(ruleIDs))
	for _, ruleID := range ruleIDs {
		compliance = append(compliance, contracts.ComplianceEntry{
			SchemaVersion: "1",
			RunID:         input.RunID,
			Pass:          input.Pass,
			Agent:         input.Agent,
			RuleID:        ruleID,
			Verdict:       contracts.ComplianceVerdictCompliant,
			Rationale:     "scripted adopt compliance",
			VerdictPath:   contracts.VerdictPathSingle,
			RubricVersion: "default",
			PromptVersion: "phase0-stub",
			ResolvedAt:    time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
		})
	}
	output := judges.JudgeOutput{
		Scores:     scores,
		Compliance: compliance,
	}
	return output, output.ValidateFor(input)
}

type nonScorableImplementStep struct {
	kind     contracts.ManifestKind
	exitCode int
	reason   string
	detail   string
}

func (s nonScorableImplementStep) Run(ctx context.Context, run *StepRunContext) error {
	_ = ctx
	manifestPath, err := run.IO.ManifestPath(run.Pass, run.Agent)
	if err != nil {
		return err
	}
	startedAt := time.Now().UTC()
	switch s.kind {
	case contracts.ManifestKindTimeout:
		return internalio.WriteJSONAtomic(manifestPath, contracts.Manifest{
			Kind: contracts.ManifestKindTimeout,
			Value: contracts.ManifestTimeout{
				Kind:           contracts.ManifestKindTimeout,
				SchemaVersion:  "1",
				RunID:          run.IO.RunID,
				Pass:           run.Pass,
				Agent:          run.Agent,
				TimeoutSeconds: 300,
				StartedAt:      startedAt,
				FinishedAt:     startedAt,
			},
		})
	default:
		reason := s.reason
		if reason == "" {
			reason = "unknown"
		}
		detail := s.detail
		if detail == "" {
			detail = "fixture non-scorable manifest"
		}
		return internalio.WriteJSONAtomic(manifestPath, contracts.Manifest{
			Kind: contracts.ManifestKindError,
			Value: contracts.ManifestError{
				Kind:          contracts.ManifestKindError,
				SchemaVersion: "1",
				RunID:         run.IO.RunID,
				Pass:          run.Pass,
				Agent:         run.Agent,
				ExitCode:      s.exitCode,
				Reason:        reason,
				Detail:        detail,
				StartedAt:     startedAt,
				FinishedAt:    startedAt,
			},
		})
	}
}

func nonScorableAgentSteps(kind contracts.ManifestKind) map[contracts.AgentID]Step {
	steps := make(map[contracts.AgentID]Step, len(defaultAgents))
	for _, agent := range defaultAgents {
		steps[agent] = nonScorableImplementStep{kind: kind, exitCode: 1}
	}
	return steps
}

func providerInterruptedAgentSteps(reason string) map[contracts.AgentID]Step {
	steps := make(map[contracts.AgentID]Step, len(defaultAgents))
	for _, agent := range defaultAgents {
		steps[agent] = nonScorableImplementStep{kind: contracts.ManifestKindError, exitCode: 1, reason: reason}
	}
	return steps
}

func noChangeAgentSteps() map[contracts.AgentID]Step {
	steps := make(map[contracts.AgentID]Step, len(defaultAgents))
	for _, agent := range defaultAgents {
		steps[agent] = nonScorableImplementStep{
			kind:     contracts.ManifestKindError,
			exitCode: 0,
			reason:   "unknown",
			detail:   "agent produced no diff",
		}
	}
	return steps
}
