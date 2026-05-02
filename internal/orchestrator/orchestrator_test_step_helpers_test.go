package orchestrator

import (
	"context"
	"fmt"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/contracts/stepio"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/steps/step20_implement"
	"sync"
	"time"
)

func stubPipelineSteps(step10 Step, step70 Step) Steps {
	if step10 == nil {
		step10 = stubStep10{}
	}
	if step70 == nil {
		step70 = stubStep70{}
	}
	return Steps{
		Step10: step10,
		Step20: map[contracts.AgentID]Step{
			"a1": stubImplementStep{},
			"a2": stubImplementStep{},
			"a3": stubImplementStep{},
		},
		Step30: stubMarkerStep{path: "30/done.marker"},
		Step40: duplicateOnlyCandidateStep{},
		Step50: map[contracts.AgentID]Step{
			"a1": stubImplementStep{},
			"a2": stubImplementStep{},
			"a3": stubImplementStep{},
		},
		Step60:  step60Step{},
		Step70:  step70,
		Archive: stubArchiveStep{},
	}
}

type forcedCandidateStep struct{}

func (forcedCandidateStep) Run(ctx context.Context, run *StepRunContext) error {
	_ = ctx
	body := "# Forced rule\n\nUse explicit resource cleanup.\n"
	candidate := contracts.Candidate{
		CandidateID:        "cand-forced-001",
		Kind:               contracts.CandidateKindNew,
		Title:              "Forced rule",
		Problem:            "problem",
		Rationale:          "rationale",
		ProposedBodyPath:   "40/candidates/cand-forced-001.md",
		ProposedBodySha256: sha256String(body),
	}
	if err := candidate.Validate(); err != nil {
		return err
	}
	candidates := &contracts.Candidates{
		SchemaVersion:  "1",
		RunID:          run.IO.RunID,
		Candidates:     []contracts.Candidate{candidate},
		CandidatesHash: contracts.CanonicalCandidatesHash([]contracts.Candidate{candidate}),
		CreatedAt:      time.Now().UTC(),
	}
	sidecarPath, err := run.IO.ResolveRunRelative(candidate.ProposedBodyPath)
	if err != nil {
		return err
	}
	if err := internalio.WriteAtomic(sidecarPath, []byte(body)); err != nil {
		return err
	}
	candidatesPath, err := run.IO.ResolveRunRelative("40/candidates.json")
	if err != nil {
		return err
	}
	if err := internalio.WriteJSONAtomic(candidatesPath, candidates); err != nil {
		return err
	}
	run.Candidates = candidates
	return nil
}

type duplicateOnlyCandidateStep struct{}

func (duplicateOnlyCandidateStep) Run(ctx context.Context, run *StepRunContext) error {
	_ = ctx
	body := "# Duplicate rule\n\n- source_rule_id: rule-existing\n- classification: duplicate\n"
	candidate := contracts.Candidate{
		CandidateID:        "cand-dup-only",
		Kind:               contracts.CandidateKindDuplicate,
		TargetRuleID:       "rule-existing",
		Title:              "Duplicate rule",
		Problem:            "problem",
		Rationale:          "rationale",
		ProposedBodyPath:   "40/candidates/cand-dup-only.md",
		ProposedBodySha256: sha256String(body),
	}
	candidates := &contracts.Candidates{
		SchemaVersion:  "1",
		RunID:          run.IO.RunID,
		Candidates:     []contracts.Candidate{candidate},
		CandidatesHash: contracts.CanonicalCandidatesHash([]contracts.Candidate{candidate}),
		CreatedAt:      time.Now().UTC(),
	}
	sidecarPath, err := run.IO.ResolveRunRelative(candidate.ProposedBodyPath)
	if err != nil {
		return err
	}
	if err := internalio.WriteAtomic(sidecarPath, []byte(body)); err != nil {
		return err
	}
	candidatesPath, err := run.IO.ResolveRunRelative("40/candidates.json")
	if err != nil {
		return err
	}
	if err := internalio.WriteJSONAtomic(candidatesPath, candidates); err != nil {
		return err
	}
	run.Candidates = candidates
	return nil
}

type rescueExhaustedStep struct{}

func (rescueExhaustedStep) Run(ctx context.Context, run *StepRunContext) error {
	_ = ctx
	return &step20_implement.RescueExhaustedError{
		Rescue: stepio.RescueExhausted{
			Agent:      run.Agent,
			RetryCount: 3,
		},
	}
}

type leaseContendedOnceStep struct {
	mu   sync.Mutex
	seen bool
}

func (s *leaseContendedOnceStep) Run(ctx context.Context, run *StepRunContext) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.seen {
		s.seen = true
		return fmt.Errorf("%w: agent %s", step20_implement.ErrAgentLeaseContended, run.Agent)
	}
	return stubImplementStep{}.Run(ctx, run)
}

func forcedCandidate(runID contracts.RunID) *contracts.Candidates {
	body := "# Forced rule\n\nUse explicit resource cleanup.\n"
	candidate := contracts.Candidate{
		CandidateID:        "cand-forced-001",
		Kind:               contracts.CandidateKindNew,
		Title:              "Forced rule",
		Problem:            "problem",
		Rationale:          "rationale",
		ProposedBodyPath:   "40/candidates/cand-forced-001.md",
		ProposedBodySha256: sha256String(body),
	}
	return &contracts.Candidates{
		SchemaVersion:  "1",
		RunID:          runID,
		Candidates:     []contracts.Candidate{candidate},
		CandidatesHash: contracts.CanonicalCandidatesHash([]contracts.Candidate{candidate}),
		CreatedAt:      time.Now().UTC(),
	}
}
