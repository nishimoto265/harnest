// Package step30_score implements Phase 0 step 30 — pass-1 absolute scoring —
// orchestrated over the scorecore primitives. It is intentionally decoupled
// from the orchestrator package; a thin adapter in
// `internal/orchestrator/stub_steps.go` wraps Run(...) into an
// orchestrator.Step value. This keeps the import graph one-way
// (orchestrator -> step30_score) and lets tests drive the package directly.
package step30_score

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/nishimoto265/harnest/internal/judges"
	"github.com/nishimoto265/harnest/internal/steps/scorecore"
)

const (
	defaultRubricVersion = "default"
	defaultPromptVersion = "phase0-stub"
)

// PanelProvider returns the single primary judge for a per-agent JudgeInput.
// The name is retained for existing adapters, but step30 no longer runs a
// secondary/arbiter panel.
type PanelProvider interface {
	Judge(input judges.JudgeInput) (judges.Judge, error)
}

type PanelVersionProvider interface {
	PanelPromptVersion(base string) string
}

// Step implements the pass-1 scoring step. Safe to reuse across runs.
type Step struct {
	panel    PanelProvider
	resolver *scorecore.PanelResolver
	now      func() time.Time

	// RubricPath resolver override (tests only).
	rubricPathFn func(runCtx internalio.RunContext) (string, error)

	// Scoring version knobs.
	rubricVersion string
	promptVersion string
}

// Option configures Step at construction time. Callers typically only need
// WithPanelProvider; the rest are test knobs.
type Option func(*Step)

func WithPanelProvider(p PanelProvider) Option { return func(s *Step) { s.panel = p } }
func WithNow(fn func() time.Time) Option       { return func(s *Step) { s.now = fn } }
func WithRubricVersion(v string) Option        { return func(s *Step) { s.rubricVersion = v } }
func WithPromptVersion(v string) Option        { return func(s *Step) { s.promptVersion = v } }

// New returns a Step configured with the supplied options; defaults cover the
// primary stub judge.
func New(opts ...Option) *Step {
	s := &Step{
		panel:         DefaultPanelProvider(),
		resolver:      scorecore.NewPanelResolver(),
		now:           func() time.Time { return time.Now().UTC() },
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
	RunContext    internalio.RunContext
	TaskPackage   *contracts.TaskPackage
	PanelProvider PanelProvider
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
func (s *Step) Run(ctx context.Context, req Request) (err error) {
	if req.TaskPackage == nil {
		return ErrNoTaskPackage
	}
	if req.TaskPackage.RunID != req.RunContext.RunID {
		return fmt.Errorf("step30_score: task package run_id mismatch: task_package=%s io=%s", req.TaskPackage.RunID, req.RunContext.RunID)
	}
	panel := s.panel
	if req.PanelProvider != nil {
		panel = req.PanelProvider
	}

	paths, err := stepPaths(req.RunContext)
	if err != nil {
		return err
	}

	lock, err := internalio.AcquireFileLock(paths.LockPath)
	if err != nil {
		return err
	}
	defer func() {
		if unlockErr := lock.Unlock(); err == nil && unlockErr != nil {
			err = unlockErr
		}
	}()

	if err := os.MkdirAll(filepath.Dir(paths.MarkerPath), 0o755); err != nil {
		return err
	}

	rubricPath, err := s.resolveRubricPath(req.RunContext)
	if err != nil {
		return err
	}
	rubricVersion, err := effectiveRubricVersion(s.rubricVersion, rubricPath)
	if err != nil {
		return err
	}
	expectedComplianceRuleIDs, err := expectedStep30ComplianceRuleIDs(rubricPath)
	if err != nil {
		return err
	}
	expectedComplianceRules := expectedComplianceRuleSet(expectedComplianceRuleIDs)

	scorableAgents, err := resolveScorableAgents(req)
	if err != nil {
		return err
	}
	if len(scorableAgents) == 0 {
		return ErrNoScorableAgents
	}
	agentIDs := scorableAgentIDs(scorableAgents)

	// Short-circuit on a pre-existing valid marker (resume path).
	markerExists, err := fileExists(paths.MarkerPath)
	if err != nil {
		return err
	}
	valid, err := scorecore.VerifyStep30DoneMarker(req.RunContext, paths.MarkerPaths)
	if err != nil {
		return err
	}
	if valid {
		promptVersion := s.promptVersion
		if versioned, ok := panel.(PanelVersionProvider); ok {
			promptVersion = versioned.PanelPromptVersion(s.promptVersion)
		}
		versionsMatch, err := step30VersionsMatch(paths, agentIDs, rubricVersion, promptVersion, expectedComplianceRules)
		if err != nil {
			return err
		}
		scopeComplete, err := step30FinalRowsCompleteForCurrentScope(paths, agentIDs, expectedComplianceRules)
		if err != nil {
			return err
		}
		markerScopeMatches, err := step30DoneMarkerAgentsMatch(paths.MarkerPath, agentIDs)
		if err != nil {
			return err
		}
		if versionsMatch && scopeComplete && markerScopeMatches {
			return nil
		}
	}
	forcedRebuild := markerExists
	if markerExists {
		// Marker exists but no longer matches the underlying jsonl/version —
		// remove it so BuildStep30DoneMarker can re-assert the invariant.
		_ = os.Remove(paths.MarkerPath)
	}

	finalScopeCurrent, err := step30FinalRowsWithinCurrentScope(paths, agentIDs, expectedComplianceRules)
	if err != nil {
		return err
	}

	state, err := loadResumeState(paths, expectedComplianceRules)
	if err != nil {
		return err
	}

	rebuildFinals := forcedRebuild || !finalScopeCurrent
	if rebuildFinals {
		if err := resetStep30FinalFiles(paths); err != nil {
			return err
		}
		state.clearFinals()
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
		// F16: hash and snapshot in one read. The judge receives the
		// snapshot path instead of the live diff, so a concurrent rename
		// or symlink swap cannot make `outputSha` disagree with the bytes
		// the judge actually scores. The snapshot lives under the run
		// directory (cleaned up by step cleanup) and is only used to
		// stabilise the hash boundary.
		snapshotPath, outputSha, err := snapshotAndHashDiff(req.RunContext, agent.agent, diffAbs)
		if err != nil {
			return fmt.Errorf("step30_score: snapshot diff agent=%s: %w", agent.agent, err)
		}

		judgeInput := judges.JudgeInput{
			RunID:                     req.RunContext.RunID,
			Pass:                      1,
			Agent:                     agent.agent,
			OutputPath:                snapshotPath,
			RubricPath:                rubricPath,
			ExpectedComplianceRuleIDs: expectedComplianceRuleIDs,
			EnforceExpectedCompliance: true,
		}
		primary, err := panel.Judge(judgeInput)
		if err != nil {
			return fmt.Errorf("step30_score: judge agent=%s: %w", agent.agent, err)
		}
		if primary == nil {
			return fmt.Errorf("step30_score: primary judge is required for agent=%s", agent.agent)
		}
		promptVersion := judges.PanelPromptVersion(s.promptVersion, primary, nil, nil)

		panelInput := scorecore.PanelInput{
			Primary:       primary,
			JudgeInput:    judgeInput,
			OutputSha256:  outputSha,
			RubricVersion: rubricVersion,
			PromptVersion: promptVersion,
			RunContext:    req.RunContext,
			StepDir:       "30",
		}
		agentState := state.agent(agent.agent)

		if !agentState.roleComplete(contracts.JudgeRolePrimary, panelInput.OutputSha256, rubricVersion, promptVersion, expectedComplianceRules) {
			if err := s.runPrimary(ctx, paths, panelInput, agentState, primary); err != nil {
				return fmt.Errorf("step30_score: resolve primary agent=%s: %w", agent.agent, err)
			}
		}

		primaryScores := agentState.rawScoreSlice(contracts.JudgeRolePrimary)
		primaryCompliance := agentState.rawComplianceSlice(contracts.JudgeRolePrimary)
		result, err := scorecore.BuildFinalResultFromRaw(
			primaryScores,
			nil,
			nil,
			primaryCompliance,
			nil,
			nil,
			0,
			false,
			false,
		)
		if err != nil {
			return fmt.Errorf("step30_score: resolve primary agent=%s: %w", agent.agent, err)
		}
		if rebuildFinals {
			agentState.clearFinal()
		}
		if err := appendExpectedFinalScores(paths, agentState, result); err != nil {
			return fmt.Errorf("step30_score: append final scores agent=%s: %w", agent.agent, err)
		}
		if err := appendExpectedFinalCompliance(paths, agentState, result); err != nil {
			return fmt.Errorf("step30_score: append final compliance agent=%s: %w", agent.agent, err)
		}
	}

	marker, err := scorecore.BuildStep30DoneMarker(scorecore.Step30MarkerInputs{
		Agents:     agentIDs,
		Paths:      paths.MarkerPaths,
		ResolvedAt: s.now(),
	})
	if err != nil {
		return err
	}

	expectedScores := int64(len(agentIDs) * 5)
	if marker.ExpectedCounts.Scores != expectedScores {
		return fmt.Errorf(
			"%w: agents=%d scores=%d/%d",
			ErrCardinalityMismatch,
			len(agentIDs),
			marker.ExpectedCounts.Scores, expectedScores,
		)
	}

	return scorecore.WriteStep30DoneMarker(req.RunContext, marker)
}

func (s *Step) runPrimary(
	ctx context.Context,
	paths stepPathsResult,
	panelInput scorecore.PanelInput,
	agentState *resumeAgentState,
	judge judges.Judge,
) error {
	result, err := s.resolver.ResolveRole(
		ctx,
		panelInput,
		contracts.JudgeRolePrimary,
		judge,
		agentState.rawScoreSlice(contracts.JudgeRolePrimary),
		nil,
		agentState.rawComplianceSlice(contracts.JudgeRolePrimary),
		nil,
	)
	if err != nil {
		return err
	}
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
	if err := appendIssueEntries(paths, result.Issues); err != nil {
		return err
	}
	agentState.replaceRawScores(contracts.JudgeRolePrimary, result.RawScores)
	agentState.replaceRawCompliance(contracts.JudgeRolePrimary, result.RawCompliance)
	return nil
}

type scorableAgent struct {
	agent contracts.AgentID
}

func scorableAgentIDs(agents []scorableAgent) []contracts.AgentID {
	out := make([]contracts.AgentID, 0, len(agents))
	for _, agent := range agents {
		out = append(out, agent.agent)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
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
			if shouldSkipManifest(err) {
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

func shouldSkipManifest(err error) bool {
	return errors.Is(err, internalio.ErrNotScorable)
}
