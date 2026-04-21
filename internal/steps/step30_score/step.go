// Package step30_score implements Phase 0 step 30 — pass-1 panel scoring —
// orchestrated over the scorecore primitives. It is intentionally decoupled
// from the orchestrator package; a thin adapter in
// `internal/orchestrator/stub_steps.go` wraps Run(...) into an
// orchestrator.Step value. This keeps the import graph one-way
// (orchestrator -> step30_score) and lets tests drive the package directly.
package step30_score

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/judges"
	"github.com/nishimoto265/auto-improve/internal/steps/scorecore"
)

// Defaults chosen so the Phase 0 stub judges (primary=84..78, secondary one
// point lower per dim) always fall under the threshold and resolve to
// "agreement". Phase 1 will lift these into config.
const (
	defaultDisagreementThreshold = 5
	defaultRubricVersion         = "default"
	defaultPromptVersion         = "phase0-stub"
)

// PanelProvider returns the primary/secondary/arbiter Judge trio for a given
// per-agent JudgeInput. Splitting this from Step lets tests inject fixture
// judges without touching the judges stub constructors.
type PanelProvider interface {
	Judges(input judges.JudgeInput) (primary, secondary, arbiter judges.Judge, err error)
}

// Step implements the pass-1 scoring step. Safe to reuse across runs.
type Step struct {
	panel    PanelProvider
	resolver *scorecore.PanelResolver
	now      func() time.Time

	// RubricPath resolver override (tests only).
	rubricPathFn func(runCtx internalio.RunContext) (string, error)

	// Scoring knobs.
	threshold     int
	rubricVersion string
	promptVersion string
}

// Option configures Step at construction time. Callers typically only need
// WithPanelProvider; the rest are test knobs.
type Option func(*Step)

func WithPanelProvider(p PanelProvider) Option { return func(s *Step) { s.panel = p } }
func WithNow(fn func() time.Time) Option       { return func(s *Step) { s.now = fn } }
func WithDisagreementThreshold(v int) Option   { return func(s *Step) { s.threshold = v } }
func WithRubricVersion(v string) Option        { return func(s *Step) { s.rubricVersion = v } }
func WithPromptVersion(v string) Option        { return func(s *Step) { s.promptVersion = v } }

// New returns a Step configured with the supplied options; defaults cover the
// Phase 0 stub panel (primary + secondary + arbiter all from judges.NewStub).
func New(opts ...Option) *Step {
	s := &Step{
		panel:         DefaultPanelProvider(),
		resolver:      scorecore.NewPanelResolver(),
		now:           func() time.Time { return time.Now().UTC() },
		threshold:     defaultDisagreementThreshold,
		rubricVersion: defaultRubricVersion,
		promptVersion: defaultPromptVersion,
	}
	for _, opt := range opts {
		opt(s)
	}
	if s.resolver == nil {
		s.resolver = scorecore.NewPanelResolver()
	}
	return s
}

// Request captures the minimum run-scoped inputs Run needs. This mirrors the
// orchestrator.StepRunContext surface without importing orchestrator; the
// adapter in internal/orchestrator wraps StepRunContext into Request.
type Request struct {
	RunContext  internalio.RunContext
	TaskPackage *contracts.TaskPackage
}

// Errors surfaced by Run.
var (
	ErrNoTaskPackage        = errors.New("step30_score: task package is required")
	ErrNoScorableAgents     = errors.New("step30_score: no scorable agents found in task_package.worktrees[pass=1]")
	ErrCardinalityMismatch  = errors.New("step30_score: cardinality mismatch between scorable agents and reduced jsonl rows")
	ErrRubricPathUnresolved = errors.New("step30_score: rubric path could not be resolved")
)

// Run executes the step. Idempotent: a second call after a valid done.marker
// is a no-op; an invalid marker is removed and the step restarts.
func (s *Step) Run(ctx context.Context, req Request) error {
	if req.TaskPackage == nil {
		return ErrNoTaskPackage
	}

	paths, err := stepPaths(req.RunContext)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(paths.MarkerPath), 0o755); err != nil {
		return err
	}

	// Short-circuit on a pre-existing valid marker (resume path).
	valid, err := scorecore.VerifyStep30DoneMarker(req.RunContext, paths.MarkerPaths)
	if err != nil {
		return err
	}
	if valid {
		return nil
	}
	// Marker exists but no longer matches the underlying jsonl — remove it
	// so BuildStep30DoneMarker can re-assert the invariant.
	_ = os.Remove(paths.MarkerPath)

	scorableAgents, err := resolveScorableAgents(req)
	if err != nil {
		return err
	}
	if len(scorableAgents) == 0 {
		return ErrNoScorableAgents
	}

	rubricPath, err := s.resolveRubricPath(req.RunContext)
	if err != nil {
		return err
	}

	for _, agent := range scorableAgents {
		if err := ctx.Err(); err != nil {
			return err
		}
		manifest, err := internalio.LoadScorableManifest(req.RunContext, 1, agent.agent)
		if err != nil {
			return fmt.Errorf("step30_score: load manifest agent=%s: %w", agent.agent, err)
		}
		if manifest == nil {
			return fmt.Errorf("step30_score: nil manifest agent=%s", agent.agent)
		}

		diffAbs, err := req.RunContext.ResolveRunRelative(manifest.DiffPath)
		if err != nil {
			return err
		}
		outputSha, err := fileSha256(diffAbs)
		if err != nil {
			return fmt.Errorf("step30_score: hash diff agent=%s: %w", agent.agent, err)
		}

		judgeInput := judges.JudgeInput{
			RunID:      req.RunContext.RunID,
			Pass:       1,
			Agent:      agent.agent,
			OutputPath: diffAbs,
			RubricPath: rubricPath,
		}
		primary, secondary, arbiter, err := s.panel.Judges(judgeInput)
		if err != nil {
			return fmt.Errorf("step30_score: panel agent=%s: %w", agent.agent, err)
		}

		result, err := s.resolver.Resolve(ctx, scorecore.PanelInput{
			Primary:               primary,
			Secondary:             secondary,
			Arbiter:               arbiter,
			JudgeInput:            judgeInput,
			OutputSha256:          outputSha,
			DisagreementThreshold: s.threshold,
			RunContext:            req.RunContext,
			StepDir:               "30",
		})
		if err != nil {
			return fmt.Errorf("step30_score: resolve agent=%s: %w", agent.agent, err)
		}

		// Raw layer first, then final. Within each layer keep the order
		// produced by the resolver so CollapseByKey yields the expected
		// "last wins" shape.
		for _, row := range result.RawScores {
			if err := internalio.AppendJSONL(paths.ScoreRaw, row); err != nil {
				return err
			}
		}
		for _, row := range result.RawCompliance {
			if err := internalio.AppendJSONL(paths.ComplianceRaw, row); err != nil {
				return err
			}
		}
		for _, row := range result.FinalScores {
			if err := internalio.AppendJSONL(paths.ScoreFinal, row); err != nil {
				return err
			}
		}
		for _, row := range result.FinalCompliance {
			if err := internalio.AppendJSONL(paths.ComplianceFinal, row); err != nil {
				return err
			}
		}
	}

	agentIDs := make([]contracts.AgentID, 0, len(scorableAgents))
	for _, a := range scorableAgents {
		agentIDs = append(agentIDs, a.agent)
	}

	marker, err := scorecore.BuildStep30DoneMarker(scorecore.Step30MarkerInputs{
		Agents: agentIDs,
		Paths:  paths.MarkerPaths,
		ResolvedAt: s.now(),
	})
	if err != nil {
		return err
	}

	expectedScores := int64(len(agentIDs) * 5)
	expectedCompliance := int64(len(agentIDs))
	if marker.ExpectedCounts.Scores != expectedScores || marker.ExpectedCounts.Compliance != expectedCompliance {
		return fmt.Errorf(
			"%w: agents=%d scores=%d/%d compliance=%d/%d",
			ErrCardinalityMismatch,
			len(agentIDs),
			marker.ExpectedCounts.Scores, expectedScores,
			marker.ExpectedCounts.Compliance, expectedCompliance,
		)
	}

	return scorecore.WriteStep30DoneMarker(req.RunContext, marker)
}

type scorableAgent struct {
	agent contracts.AgentID
}

func resolveScorableAgents(req Request) ([]scorableAgent, error) {
	if req.TaskPackage == nil {
		return nil, ErrNoTaskPackage
	}
	seen := make(map[contracts.AgentID]struct{}, len(req.TaskPackage.Worktrees))
	out := make([]scorableAgent, 0, len(req.TaskPackage.Worktrees))
	for _, wt := range req.TaskPackage.Worktrees {
		if wt.Pass != 1 {
			continue
		}
		if _, dup := seen[wt.Agent]; dup {
			continue
		}
		manifest, err := internalio.LoadScorableManifest(req.RunContext, 1, wt.Agent)
		if err != nil {
			if errors.Is(err, internalio.ErrNotScorable) {
				continue
			}
			if os.IsNotExist(err) {
				// Missing manifest == not scorable yet; skip.
				continue
			}
			return nil, fmt.Errorf("step30_score: manifest agent=%s: %w", wt.Agent, err)
		}
		if manifest == nil {
			continue
		}
		seen[wt.Agent] = struct{}{}
		out = append(out, scorableAgent{agent: wt.Agent})
	}
	return out, nil
}

type stepPathsResult struct {
	MarkerPath      string
	ScoreFinal      string
	ComplianceFinal string
	ScoreRaw        string
	ComplianceRaw   string
	MarkerPaths     scorecore.Step30MarkerPaths
}

func stepPaths(runCtx internalio.RunContext) (stepPathsResult, error) {
	marker, err := runCtx.ResolveRunRelative("30/done.marker")
	if err != nil {
		return stepPathsResult{}, err
	}
	scoreFinal, err := runCtx.ResolveRunRelative("30/scores-A.jsonl")
	if err != nil {
		return stepPathsResult{}, err
	}
	complianceFinal, err := runCtx.ResolveRunRelative("30/compliance-A.jsonl")
	if err != nil {
		return stepPathsResult{}, err
	}
	scoreRaw, err := runCtx.ResolveRunRelative("30/scores-A-raw.jsonl")
	if err != nil {
		return stepPathsResult{}, err
	}
	complianceRaw, err := runCtx.ResolveRunRelative("30/compliance-A-raw.jsonl")
	if err != nil {
		return stepPathsResult{}, err
	}
	return stepPathsResult{
		MarkerPath:      marker,
		ScoreFinal:      scoreFinal,
		ComplianceFinal: complianceFinal,
		ScoreRaw:        scoreRaw,
		ComplianceRaw:   complianceRaw,
		MarkerPaths: scorecore.Step30MarkerPaths{
			ScoreFinal:      scoreFinal,
			ComplianceFinal: complianceFinal,
			ScoreRaw:        scoreRaw,
			ComplianceRaw:   complianceRaw,
		},
	}, nil
}

func (s *Step) resolveRubricPath(runCtx internalio.RunContext) (string, error) {
	if s.rubricPathFn != nil {
		return s.rubricPathFn(runCtx)
	}
	// Phase 0: use a placeholder rubric path under RunsBase so validation
	// passes. Rubric loading is tracked in Phase 1.
	path := filepath.Join(runCtx.RunsBase, ".rubrics", "default.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.WriteFile(path, []byte("# phase0 stub rubric\n"), 0o644); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	return path, nil
}

func fileSha256(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
