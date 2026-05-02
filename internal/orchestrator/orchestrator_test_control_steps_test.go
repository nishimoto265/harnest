package orchestrator

import (
	"context"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"strings"
	"sync"
	"time"
)

type callRecorder struct {
	mu    sync.Mutex
	calls []string
}

type cancelAfterNoopStep struct {
	cancel context.CancelFunc
}

func (s *cancelAfterNoopStep) Run(ctx context.Context, run *StepRunContext) error {
	if err := (stubStep70{}).Run(ctx, run); err != nil {
		return err
	}
	if s.cancel != nil {
		s.cancel()
	}
	return nil
}

type cancelAfterTerminalPromoteStep struct {
	cancel context.CancelFunc
}

func (s *cancelAfterTerminalPromoteStep) Run(ctx context.Context, run *StepRunContext) error {
	if err := (terminalPromoteStep{}).Run(ctx, run); err != nil {
		return err
	}
	if s.cancel != nil {
		s.cancel()
	}
	return nil
}

type blockingStartStep struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingStartStep() *blockingStartStep {
	return &blockingStartStep{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (s *blockingStartStep) Run(ctx context.Context, run *StepRunContext) error {
	s.once.Do(func() {
		close(s.started)
	})
	<-s.release
	return stubStep10{}.Run(ctx, run)
}

func (r *callRecorder) add(call string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, call)
}

func (r *callRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

func eventKinds(events []contracts.StateEntry) []contracts.StateKind {
	kinds := make([]contracts.StateKind, 0, len(events))
	for _, event := range events {
		kinds = append(kinds, event.Kind)
	}
	return kinds
}

type recordingStep struct {
	label    string
	recorder *callRecorder
}

type failOnceStep struct {
	err  error
	seen bool
}

func (s *failOnceStep) Run(ctx context.Context, run *StepRunContext) error {
	_ = ctx
	_ = run
	if !s.seen {
		s.seen = true
		return s.err
	}
	return nil
}

func (s recordingStep) Run(ctx context.Context, run *StepRunContext) error {
	_ = ctx
	s.recorder.add(s.label)
	switch s.label {
	case "10":
		return stubStep10{}.Run(ctx, run)
	case "30":
		return stubMarkerStep{path: "30/done.marker"}.Run(ctx, run)
	case "40":
		if err := (stubStep40{}).Run(ctx, run); err != nil {
			return err
		}
		if run.Candidates == nil || len(run.Candidates.Candidates) == 0 {
			candidates := []contracts.Candidate{{
				CandidateID:        "cand-test-001",
				Kind:               contracts.CandidateKindNew,
				Title:              "stub candidate",
				Problem:            "problem",
				Rationale:          "rationale",
				ProposedBodyPath:   "40/candidates/cand-test-001.md",
				ProposedBodySha256: strings.Repeat("a", 64),
			}}
			run.Candidates = &contracts.Candidates{
				SchemaVersion:  "1",
				RunID:          run.IO.RunID,
				Candidates:     candidates,
				CandidatesHash: contracts.CanonicalCandidatesHash(candidates),
				CreatedAt:      time.Now().UTC(),
			}
			candidatesPath, err := run.IO.ResolveRunRelative("40/candidates.json")
			if err != nil {
				return err
			}
			if err := internalio.WriteJSONAtomic(candidatesPath, run.Candidates); err != nil {
				return err
			}
		}
		return nil
	case "70":
		return stubStep70{}.Run(ctx, run)
	default:
		return nil
	}
}

type recordingAgentStep struct {
	prefix   string
	recorder *callRecorder
}

func (s recordingAgentStep) Run(ctx context.Context, run *StepRunContext) error {
	s.recorder.add(s.prefix + ":" + string(run.Agent))
	return stubImplementStep{}.Run(ctx, run)
}

func recordingAgentSteps(prefix string, recorder *callRecorder) map[contracts.AgentID]Step {
	steps := make(map[contracts.AgentID]Step, len(defaultAgents))
	for _, agent := range defaultAgents {
		steps[agent] = recordingAgentStep{
			prefix:   prefix,
			recorder: recorder,
		}
	}
	return steps
}

func stubAgentSteps() map[contracts.AgentID]Step {
	steps := make(map[contracts.AgentID]Step, len(defaultAgents))
	for _, agent := range defaultAgents {
		steps[agent] = stubImplementStep{}
	}
	return steps
}

type cancelingStep struct {
	cancel context.CancelFunc
}

func (s cancelingStep) Run(ctx context.Context, run *StepRunContext) error {
	_ = run
	s.cancel()
	<-ctx.Done()
	return ctx.Err()
}
