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
	"reflect"
	"sort"
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

	state, err := loadResumeState(paths)
	if err != nil {
		return err
	}

	for _, agent := range scorableAgents {
		if err := ctx.Err(); err != nil {
			return err
		}
		agentState := state.agent(agent.agent)
		result, reusable, err := s.reusablePanelResult(agentState)
		if err != nil {
			return fmt.Errorf("step30_score: resume state agent=%s: %w", agent.agent, err)
		}
		if reusable {
			if err := appendExpectedFinal(paths, agentState, result); err != nil {
				return err
			}
			continue
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

		panel := lazyPanel{}
		loadPanel := func() error {
			if panel.loaded {
				return nil
			}
			primary, secondary, arbiter, err := s.panel.Judges(judgeInput)
			if err != nil {
				return fmt.Errorf("step30_score: panel agent=%s: %w", agent.agent, err)
			}
			panel.primary = primary
			panel.secondary = secondary
			panel.arbiter = arbiter
			panel.loaded = true
			return nil
		}

		if !agentState.roleComplete(contracts.JudgeRolePrimary) {
			if err := loadPanel(); err != nil {
				return err
			}
			if err := s.appendJudgeRole(
				ctx,
				req.RunContext,
				paths,
				agentState,
				judgeInput,
				outputSha,
				contracts.JudgeRolePrimary,
				panel.primary,
			); err != nil {
				return err
			}
		}

		secondaryEnabled := agentState.roleComplete(contracts.JudgeRoleSecondary)
		if !secondaryEnabled {
			if err := loadPanel(); err != nil {
				return err
			}
			secondaryEnabled = panel.secondary != nil
		}
		if secondaryEnabled && !agentState.roleComplete(contracts.JudgeRoleSecondary) {
			if err := s.appendJudgeRole(
				ctx,
				req.RunContext,
				paths,
				agentState,
				judgeInput,
				outputSha,
				contracts.JudgeRoleSecondary,
				panel.secondary,
			); err != nil {
				return err
			}
		}

		arbiterEnabled := agentState.roleComplete(contracts.JudgeRoleArbiter)
		if !arbiterEnabled {
			if err := loadPanel(); err != nil {
				return err
			}
			arbiterEnabled = panel.arbiter != nil
		}

		if secondaryEnabled {
			disagree, err := scorecore.PanelDisagrees(
				agentState.rawScoreRows(contracts.JudgeRolePrimary),
				agentState.rawScoreRows(contracts.JudgeRoleSecondary),
				agentState.rawComplianceRows(contracts.JudgeRolePrimary),
				agentState.rawComplianceRows(contracts.JudgeRoleSecondary),
				s.threshold,
			)
			if err != nil {
				return fmt.Errorf("step30_score: panel disagreement agent=%s: %w", agent.agent, err)
			}
			if disagree && arbiterEnabled && !agentState.roleComplete(contracts.JudgeRoleArbiter) {
				if err := s.appendJudgeRole(
					ctx,
					req.RunContext,
					paths,
					agentState,
					judgeInput,
					outputSha,
					contracts.JudgeRoleArbiter,
					panel.arbiter,
				); err != nil {
					return err
				}
				result, err = s.expectedPanelResult(agentState, secondaryEnabled, arbiterEnabled)
				if err != nil {
					return fmt.Errorf("step30_score: refresh final agent=%s: %w", agent.agent, err)
				}
			}
		}
		result, err = s.expectedPanelResult(agentState, secondaryEnabled, arbiterEnabled)
		if err != nil {
			return fmt.Errorf("step30_score: assemble final agent=%s: %w", agent.agent, err)
		}

		if err := appendExpectedFinal(paths, agentState, result); err != nil {
			return err
		}
	}

	agentIDs := make([]contracts.AgentID, 0, len(scorableAgents))
	for _, a := range scorableAgents {
		agentIDs = append(agentIDs, a.agent)
	}

	marker, err := scorecore.BuildStep30DoneMarker(scorecore.Step30MarkerInputs{
		Agents:     agentIDs,
		Paths:      paths.MarkerPaths,
		ResolvedAt: s.now(),
	})
	if err != nil {
		return err
	}

	expectedScores := int64(len(agentIDs) * len(step30Dimensions))
	if marker.ExpectedCounts.Scores != expectedScores {
		return fmt.Errorf(
			"%w: agents=%d scores=%d/%d",
			ErrCardinalityMismatch,
			len(agentIDs),
			marker.ExpectedCounts.Scores, expectedScores,
		)
	}
	if err := validateComplianceCoverage(state, agentIDs); err != nil {
		return err
	}

	return scorecore.WriteStep30DoneMarker(req.RunContext, marker)
}

var step30Dimensions = []contracts.Dimension{
	contracts.DimensionFidelity,
	contracts.DimensionCorrectness,
	contracts.DimensionMaintainability,
	contracts.DimensionDiscipline,
	contracts.DimensionCommunication,
}

type lazyPanel struct {
	loaded    bool
	primary   judges.Judge
	secondary judges.Judge
	arbiter   judges.Judge
}

type resumeState struct {
	agents map[contracts.AgentID]*agentResumeState
}

type agentResumeState struct {
	rawScores       map[contracts.JudgeRole]map[contracts.Dimension]contracts.RawScoreEntry
	rawCompliance   map[contracts.JudgeRole]map[string]contracts.RawComplianceEntry
	finalScores     map[contracts.Dimension]contracts.ScoreEntry
	finalCompliance map[string]contracts.ComplianceEntry
}

func loadResumeState(paths stepPathsResult) (*resumeState, error) {
	scoreRaw, err := internalio.ReadJSONL[contracts.RawScoreEntry](paths.ScoreRaw)
	if err != nil {
		return nil, fmt.Errorf("step30_score: read raw scores: %w", err)
	}
	complianceRaw, err := internalio.ReadJSONL[contracts.RawComplianceEntry](paths.ComplianceRaw)
	if err != nil {
		return nil, fmt.Errorf("step30_score: read raw compliance: %w", err)
	}
	scoreFinal, err := internalio.ReadJSONL[contracts.ScoreEntry](paths.ScoreFinal)
	if err != nil {
		return nil, fmt.Errorf("step30_score: read final scores: %w", err)
	}
	complianceFinal, err := internalio.ReadJSONL[contracts.ComplianceEntry](paths.ComplianceFinal)
	if err != nil {
		return nil, fmt.Errorf("step30_score: read final compliance: %w", err)
	}

	state := &resumeState{agents: map[contracts.AgentID]*agentResumeState{}}
	for _, row := range scorecore.ReduceRawScoreEntries(scoreRaw) {
		state.agent(row.Agent).setRawScore(row)
	}
	for _, row := range scorecore.ReduceRawComplianceEntries(complianceRaw) {
		state.agent(row.Agent).setRawCompliance(row)
	}
	for _, row := range internalio.CollapseByKey(scoreFinal, func(e contracts.ScoreEntry) [2]string {
		return [2]string{string(e.Agent), string(e.Dimension)}
	}) {
		state.agent(row.Agent).setFinalScore(row)
	}
	for _, row := range internalio.CollapseByKey(complianceFinal, func(e contracts.ComplianceEntry) [2]string {
		return [2]string{string(e.Agent), e.RuleID}
	}) {
		state.agent(row.Agent).setFinalCompliance(row)
	}
	return state, nil
}

func (s *resumeState) agent(agent contracts.AgentID) *agentResumeState {
	if s.agents == nil {
		s.agents = map[contracts.AgentID]*agentResumeState{}
	}
	if state, ok := s.agents[agent]; ok {
		return state
	}
	state := &agentResumeState{
		rawScores:       map[contracts.JudgeRole]map[contracts.Dimension]contracts.RawScoreEntry{},
		rawCompliance:   map[contracts.JudgeRole]map[string]contracts.RawComplianceEntry{},
		finalScores:     map[contracts.Dimension]contracts.ScoreEntry{},
		finalCompliance: map[string]contracts.ComplianceEntry{},
	}
	s.agents[agent] = state
	return state
}

func (a *agentResumeState) setRawScore(row contracts.RawScoreEntry) {
	if a.rawScores[row.JudgeRole] == nil {
		a.rawScores[row.JudgeRole] = map[contracts.Dimension]contracts.RawScoreEntry{}
	}
	a.rawScores[row.JudgeRole][row.Dimension] = row
}

func (a *agentResumeState) setRawCompliance(row contracts.RawComplianceEntry) {
	if a.rawCompliance[row.JudgeRole] == nil {
		a.rawCompliance[row.JudgeRole] = map[string]contracts.RawComplianceEntry{}
	}
	a.rawCompliance[row.JudgeRole][row.RuleID] = row
}

func (a *agentResumeState) setFinalScore(row contracts.ScoreEntry) {
	a.finalScores[row.Dimension] = row
}

func (a *agentResumeState) setFinalCompliance(row contracts.ComplianceEntry) {
	a.finalCompliance[row.RuleID] = row
}

func (a *agentResumeState) roleComplete(role contracts.JudgeRole) bool {
	if len(a.rawCompliance[role]) == 0 {
		return false
	}
	rows := a.rawScores[role]
	if len(rows) != len(step30Dimensions) {
		return false
	}
	for _, dimension := range step30Dimensions {
		if _, ok := rows[dimension]; !ok {
			return false
		}
	}
	return true
}

func (a *agentResumeState) rawScoreRows(role contracts.JudgeRole) []contracts.RawScoreEntry {
	rows := make([]contracts.RawScoreEntry, 0, len(a.rawScores[role]))
	for _, dimension := range step30Dimensions {
		row, ok := a.rawScores[role][dimension]
		if ok {
			rows = append(rows, row)
		}
	}
	return rows
}

func (a *agentResumeState) rawComplianceRows(role contracts.JudgeRole) []contracts.RawComplianceEntry {
	keys := make([]string, 0, len(a.rawCompliance[role]))
	for key := range a.rawCompliance[role] {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	rows := make([]contracts.RawComplianceEntry, 0, len(keys))
	for _, key := range keys {
		rows = append(rows, a.rawCompliance[role][key])
	}
	return rows
}

func (a *agentResumeState) refreshArbiterFreshness() {
	scoreRows := make([]contracts.RawScoreEntry, 0, len(step30Dimensions)*3)
	for _, role := range []contracts.JudgeRole{
		contracts.JudgeRolePrimary,
		contracts.JudgeRoleSecondary,
		contracts.JudgeRoleArbiter,
	} {
		scoreRows = append(scoreRows, a.rawScoreRows(role)...)
	}
	complianceRows := make([]contracts.RawComplianceEntry, 0)
	for _, role := range []contracts.JudgeRole{
		contracts.JudgeRolePrimary,
		contracts.JudgeRoleSecondary,
		contracts.JudgeRoleArbiter,
	} {
		complianceRows = append(complianceRows, a.rawComplianceRows(role)...)
	}

	a.rawScores = map[contracts.JudgeRole]map[contracts.Dimension]contracts.RawScoreEntry{}
	for _, row := range scorecore.ReduceRawScoreEntries(scoreRows) {
		a.setRawScore(row)
	}
	a.rawCompliance = map[contracts.JudgeRole]map[string]contracts.RawComplianceEntry{}
	for _, row := range scorecore.ReduceRawComplianceEntries(complianceRows) {
		a.setRawCompliance(row)
	}
}

func (a *agentResumeState) finalDiff(result scorecore.PanelResult) ([]contracts.ScoreEntry, []contracts.ComplianceEntry) {
	scoreRows := make([]contracts.ScoreEntry, 0, len(result.FinalScores))
	for _, row := range result.FinalScores {
		existing, ok := a.finalScores[row.Dimension]
		if !ok || !reflect.DeepEqual(existing, row) {
			scoreRows = append(scoreRows, row)
		}
	}

	complianceRows := make([]contracts.ComplianceEntry, 0, len(result.FinalCompliance))
	for _, row := range result.FinalCompliance {
		existing, ok := a.finalCompliance[row.RuleID]
		if !ok || !reflect.DeepEqual(existing, row) {
			complianceRows = append(complianceRows, row)
		}
	}
	return scoreRows, complianceRows
}

func (s *Step) reusablePanelResult(agentState *agentResumeState) (scorecore.PanelResult, bool, error) {
	if !agentState.roleComplete(contracts.JudgeRolePrimary) || !agentState.roleComplete(contracts.JudgeRoleSecondary) {
		return scorecore.PanelResult{}, false, nil
	}

	disagree, err := scorecore.PanelDisagrees(
		agentState.rawScoreRows(contracts.JudgeRolePrimary),
		agentState.rawScoreRows(contracts.JudgeRoleSecondary),
		agentState.rawComplianceRows(contracts.JudgeRolePrimary),
		agentState.rawComplianceRows(contracts.JudgeRoleSecondary),
		s.threshold,
	)
	if err != nil {
		return scorecore.PanelResult{}, false, err
	}
	if disagree && !agentState.roleComplete(contracts.JudgeRoleArbiter) {
		return scorecore.PanelResult{}, false, nil
	}

	result, err := s.expectedPanelResult(agentState, true, agentState.roleComplete(contracts.JudgeRoleArbiter))
	if err != nil {
		return scorecore.PanelResult{}, false, err
	}
	return result, true, nil
}

func (s *Step) expectedPanelResult(
	agentState *agentResumeState,
	secondaryEnabled bool,
	arbiterEnabled bool,
) (scorecore.PanelResult, error) {
	primaryScores := agentState.rawScoreRows(contracts.JudgeRolePrimary)
	primaryCompliance := agentState.rawComplianceRows(contracts.JudgeRolePrimary)
	if len(primaryScores) == 0 {
		return scorecore.PanelResult{}, fmt.Errorf("%w: primary=%d secondary=%d", scorecore.ErrPanelDimensionMatch, 0, 0)
	}
	if !secondaryEnabled {
		return scorecore.AssemblePanelResultFromRaw(primaryScores, nil, nil, primaryCompliance, nil, nil, s.threshold)
	}
	if !agentState.roleComplete(contracts.JudgeRoleSecondary) {
		return scorecore.PanelResult{}, fmt.Errorf("%w: secondary missing", ErrCardinalityMismatch)
	}

	secondaryScores := agentState.rawScoreRows(contracts.JudgeRoleSecondary)
	secondaryCompliance := agentState.rawComplianceRows(contracts.JudgeRoleSecondary)
	disagree, err := scorecore.PanelDisagrees(primaryScores, secondaryScores, primaryCompliance, secondaryCompliance, s.threshold)
	if err != nil {
		return scorecore.PanelResult{}, err
	}
	if !disagree {
		return scorecore.AssemblePanelResultFromRaw(primaryScores, secondaryScores, nil, primaryCompliance, secondaryCompliance, nil, s.threshold)
	}

	if !arbiterEnabled {
		return scorecore.AssemblePanelResultFromRaw(primaryScores, secondaryScores, nil, primaryCompliance, secondaryCompliance, nil, s.threshold)
	}
	if !agentState.roleComplete(contracts.JudgeRoleArbiter) {
		return scorecore.PanelResult{}, fmt.Errorf("%w: arbiter missing", ErrCardinalityMismatch)
	}
	return scorecore.AssemblePanelResultFromRaw(
		primaryScores,
		secondaryScores,
		agentState.rawScoreRows(contracts.JudgeRoleArbiter),
		primaryCompliance,
		secondaryCompliance,
		agentState.rawComplianceRows(contracts.JudgeRoleArbiter),
		s.threshold,
	)
}

func (s *Step) appendJudgeRole(
	ctx context.Context,
	runCtx internalio.RunContext,
	paths stepPathsResult,
	agentState *agentResumeState,
	judgeInput judges.JudgeInput,
	outputSha string,
	role contracts.JudgeRole,
	judge judges.Judge,
) error {
	if judge == nil {
		if role == contracts.JudgeRolePrimary {
			return scorecore.ErrPanelPrimaryRequired
		}
		return fmt.Errorf("step30_score: missing %s judge", role)
	}

	out, err := judge.ScoreOutput(ctx, judgeInput)
	if err != nil {
		return fmt.Errorf("step30_score: %s agent=%s: %w", role, judgeInput.Agent, err)
	}

	var (
		primaryRefs             map[contracts.Dimension]*contracts.RawJudgeRef
		secondaryRefs           map[contracts.Dimension]*contracts.RawJudgeRef
		primaryComplianceRefs   map[string]*contracts.RawJudgeRef
		secondaryComplianceRefs map[string]*contracts.RawJudgeRef
	)
	if role == contracts.JudgeRoleArbiter {
		primaryRefs, err = scorecore.RefsByDimension(agentState.rawScoreRows(contracts.JudgeRolePrimary), contracts.JudgeRolePrimary)
		if err != nil {
			return err
		}
		secondaryRefs, err = scorecore.RefsByDimension(agentState.rawScoreRows(contracts.JudgeRoleSecondary), contracts.JudgeRoleSecondary)
		if err != nil {
			return err
		}
		primaryComplianceRefs, err = scorecore.ComplianceRefsByRule(agentState.rawComplianceRows(contracts.JudgeRolePrimary), contracts.JudgeRolePrimary)
		if err != nil {
			return err
		}
		secondaryComplianceRefs, err = scorecore.ComplianceRefsByRule(agentState.rawComplianceRows(contracts.JudgeRoleSecondary), contracts.JudgeRoleSecondary)
		if err != nil {
			return err
		}
	}

	panelInput := scorecore.PanelInput{
		JudgeInput:            judgeInput,
		OutputSha256:          outputSha,
		DisagreementThreshold: s.threshold,
		RunContext:            runCtx,
		StepDir:               "30",
	}
	rawScores, err := scorecore.BuildRawScoreEntries(out, panelInput, role, primaryRefs, secondaryRefs)
	if err != nil {
		return err
	}
	rawCompliance, err := scorecore.BuildRawComplianceEntries(out, panelInput, role, primaryComplianceRefs, secondaryComplianceRefs)
	if err != nil {
		return err
	}

	for _, row := range rawScores {
		if err := internalio.AppendJSONL(paths.ScoreRaw, row); err != nil {
			return err
		}
	}
	for _, row := range rawCompliance {
		if err := internalio.AppendJSONL(paths.ComplianceRaw, row); err != nil {
			return err
		}
	}
	for _, row := range rawScores {
		agentState.setRawScore(row)
	}
	for _, row := range rawCompliance {
		agentState.setRawCompliance(row)
	}
	agentState.refreshArbiterFreshness()
	return nil
}

func appendExpectedFinal(paths stepPathsResult, agentState *agentResumeState, result scorecore.PanelResult) error {
	scoreRows, complianceRows := agentState.finalDiff(result)
	for _, row := range scoreRows {
		if err := internalio.AppendJSONL(paths.ScoreFinal, row); err != nil {
			return err
		}
		agentState.setFinalScore(row)
	}
	for _, row := range complianceRows {
		if err := internalio.AppendJSONL(paths.ComplianceFinal, row); err != nil {
			return err
		}
		agentState.setFinalCompliance(row)
	}
	return nil
}

func validateComplianceCoverage(state *resumeState, agentIDs []contracts.AgentID) error {
	expected := make(map[contracts.AgentID]struct{}, len(agentIDs))
	for _, agent := range agentIDs {
		expected[agent] = struct{}{}
	}

	actual := make(map[contracts.AgentID]struct{}, len(agentIDs))
	for agent, agentState := range state.agents {
		if len(agentState.finalCompliance) == 0 {
			continue
		}
		actual[agent] = struct{}{}
	}

	if len(actual) != len(expected) {
		return fmt.Errorf(
			"%w: compliance_agents=%d/%d",
			ErrCardinalityMismatch,
			len(actual),
			len(expected),
		)
	}
	for agent := range expected {
		if _, ok := actual[agent]; !ok {
			return fmt.Errorf("%w: missing compliance agent=%s", ErrCardinalityMismatch, agent)
		}
	}
	for agent := range actual {
		if _, ok := expected[agent]; !ok {
			return fmt.Errorf("%w: unexpected compliance agent=%s", ErrCardinalityMismatch, agent)
		}
	}
	return nil
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
