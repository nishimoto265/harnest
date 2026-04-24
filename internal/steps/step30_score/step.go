// Package step30_score implements Phase 0 step 30 — pass-1 panel scoring —
// orchestrated over the scorecore primitives. It is intentionally decoupled
// from the orchestrator package; a thin adapter in
// `internal/orchestrator/stub_steps.go` wraps Run(...) into an
// orchestrator.Step value. This keeps the import graph one-way
// (orchestrator -> step30_score) and lets tests drive the package directly.
package step30_score

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
func (s *Step) Run(ctx context.Context, req Request) (err error) {
	if req.TaskPackage == nil {
		return ErrNoTaskPackage
	}
	if req.TaskPackage.RunID != req.RunContext.RunID {
		return fmt.Errorf("step30_score: task package run_id mismatch: task_package=%s io=%s", req.TaskPackage.RunID, req.RunContext.RunID)
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
		if versioned, ok := s.panel.(PanelVersionProvider); ok {
			promptVersion = versioned.PanelPromptVersion(s.promptVersion)
		}
		versionsMatch, err := step30VersionsMatch(paths, rubricVersion, promptVersion, expectedComplianceRules)
		if err != nil {
			return err
		}
		if versionsMatch {
			return nil
		}
	}
	forcedRebuild := markerExists
	if markerExists {
		// Marker exists but no longer matches the underlying jsonl/version —
		// remove it so BuildStep30DoneMarker can re-assert the invariant.
		_ = os.Remove(paths.MarkerPath)
	}

	scorableAgents, err := resolveScorableAgents(req)
	if err != nil {
		return err
	}
	if len(scorableAgents) == 0 {
		return ErrNoScorableAgents
	}

	state, err := loadResumeState(paths, expectedComplianceRules)
	if err != nil {
		return err
	}

	if forcedRebuild {
		if err := resetStep30FinalFiles(paths); err != nil {
			return err
		}
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
		primary, secondary, arbiter, err := s.panel.Judges(judgeInput)
		if err != nil {
			return fmt.Errorf("step30_score: panel agent=%s: %w", agent.agent, err)
		}
		promptVersion := judges.PanelPromptVersion(s.promptVersion, primary, secondary, arbiter)

		panelInput := scorecore.PanelInput{
			Primary:               primary,
			Secondary:             secondary,
			Arbiter:               arbiter,
			JudgeInput:            judgeInput,
			OutputSha256:          outputSha,
			RubricVersion:         rubricVersion,
			PromptVersion:         promptVersion,
			DisagreementThreshold: s.threshold,
			RunContext:            req.RunContext,
			StepDir:               "30",
		}
		agentState := state.agent(agent.agent)

		if err := s.ensureRole(ctx, paths, panelInput, agentState, contracts.JudgeRolePrimary, primary, rubricVersion, promptVersion, expectedComplianceRules); err != nil {
			return fmt.Errorf("step30_score: resolve primary agent=%s: %w", agent.agent, err)
		}
		if secondary != nil {
			if err := s.ensureRole(ctx, paths, panelInput, agentState, contracts.JudgeRoleSecondary, secondary, rubricVersion, promptVersion, expectedComplianceRules); err != nil {
				return fmt.Errorf("step30_score: resolve secondary agent=%s: %w", agent.agent, err)
			}
			if err := s.refreshPrimarySecondaryIfNeeded(ctx, paths, panelInput, agentState, primary, secondary); err != nil {
				return fmt.Errorf("step30_score: refresh panel agent=%s: %w", agent.agent, err)
			}
		}

		primaryScores := agentState.rawScoreSlice(contracts.JudgeRolePrimary)
		secondaryScores := agentState.rawScoreSlice(contracts.JudgeRoleSecondary)
		primaryCompliance := agentState.rawComplianceSlice(contracts.JudgeRolePrimary)
		secondaryCompliance := agentState.rawComplianceSlice(contracts.JudgeRoleSecondary)

		disagree := false
		if secondary != nil {
			disagree, err = scorecore.PanelDisagrees(primaryScores, secondaryScores, primaryCompliance, secondaryCompliance, s.threshold)
			if err != nil {
				return fmt.Errorf("step30_score: panel disagree agent=%s: %w", agent.agent, err)
			}
		}
		if secondary != nil && disagree && arbiter != nil {
			arbiterExpectedRuleIDs := disputedComplianceRuleIDs(primaryCompliance, secondaryCompliance)
			arbiterInput := panelInput
			arbiterInput.JudgeInput.ExpectedComplianceRuleIDs = arbiterExpectedRuleIDs
			arbiterInput.JudgeInput.EnforceExpectedCompliance = true
			if !agentState.arbiterCompleteFor(expectedComplianceRuleSet(arbiterExpectedRuleIDs), panelInput.OutputSha256, rubricVersion, promptVersion) {
				if err := s.runRole(ctx, paths, arbiterInput, agentState, contracts.JudgeRoleArbiter, arbiter); err != nil {
					return fmt.Errorf("step30_score: resolve arbiter agent=%s: %w", agent.agent, err)
				}
			}
		} else if secondary != nil && !disagree {
			agentState.clearArbiter()
		}

		result, err := scorecore.BuildFinalResultFromRaw(
			primaryScores,
			secondaryScores,
			agentState.rawScoreSlice(contracts.JudgeRoleArbiter),
			primaryCompliance,
			secondaryCompliance,
			agentState.rawComplianceSlice(contracts.JudgeRoleArbiter),
			s.threshold,
			secondary != nil,
			arbiter != nil,
		)
		if err != nil {
			return fmt.Errorf("step30_score: resolve final agent=%s: %w", agent.agent, err)
		}
		if forcedRebuild {
			agentState.clearFinal()
		}
		if err := appendExpectedFinalScores(paths, agentState, result); err != nil {
			return fmt.Errorf("step30_score: append final scores agent=%s: %w", agent.agent, err)
		}
		if err := appendExpectedFinalCompliance(paths, agentState, result); err != nil {
			return fmt.Errorf("step30_score: append final compliance agent=%s: %w", agent.agent, err)
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

func (s *Step) ensureRole(
	ctx context.Context,
	paths stepPathsResult,
	panelInput scorecore.PanelInput,
	agentState *resumeAgentState,
	role contracts.JudgeRole,
	judge judges.Judge,
	rubricVersion string,
	promptVersion string,
	expectedRules map[string]struct{},
) error {
	if agentState.roleComplete(role, panelInput.OutputSha256, rubricVersion, promptVersion, expectedRules) {
		return nil
	}
	return s.runRole(ctx, paths, panelInput, agentState, role, judge)
}

func (s *Step) refreshPrimarySecondaryIfNeeded(
	ctx context.Context,
	paths stepPathsResult,
	panelInput scorecore.PanelInput,
	agentState *resumeAgentState,
	primary, secondary judges.Judge,
) error {
	_, err := scorecore.PanelDisagrees(
		agentState.rawScoreSlice(contracts.JudgeRolePrimary),
		agentState.rawScoreSlice(contracts.JudgeRoleSecondary),
		agentState.rawComplianceSlice(contracts.JudgeRolePrimary),
		agentState.rawComplianceSlice(contracts.JudgeRoleSecondary),
		s.threshold,
	)
	if err == nil {
		return nil
	}

	if runErr := s.runRole(ctx, paths, panelInput, agentState, contracts.JudgeRolePrimary, primary); runErr != nil {
		return runErr
	}
	if runErr := s.runRole(ctx, paths, panelInput, agentState, contracts.JudgeRoleSecondary, secondary); runErr != nil {
		return runErr
	}
	_, err = scorecore.PanelDisagrees(
		agentState.rawScoreSlice(contracts.JudgeRolePrimary),
		agentState.rawScoreSlice(contracts.JudgeRoleSecondary),
		agentState.rawComplianceSlice(contracts.JudgeRolePrimary),
		agentState.rawComplianceSlice(contracts.JudgeRoleSecondary),
		s.threshold,
	)
	return err
}

func (s *Step) runRole(
	ctx context.Context,
	paths stepPathsResult,
	panelInput scorecore.PanelInput,
	agentState *resumeAgentState,
	role contracts.JudgeRole,
	judge judges.Judge,
) error {
	result, err := s.resolver.ResolveRole(
		ctx,
		panelInput,
		role,
		judge,
		agentState.rawScoreSlice(contracts.JudgeRolePrimary),
		agentState.rawScoreSlice(contracts.JudgeRoleSecondary),
		agentState.rawComplianceSlice(contracts.JudgeRolePrimary),
		agentState.rawComplianceSlice(contracts.JudgeRoleSecondary),
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
	agentState.replaceRawScores(role, result.RawScores)
	agentState.replaceRawCompliance(role, result.RawCompliance)
	if role == contracts.JudgeRolePrimary || role == contracts.JudgeRoleSecondary {
		agentState.clearArbiter()
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

type stepPathsResult struct {
	MarkerPath      string
	LockPath        string
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
	lockPath, err := runCtx.ResolveRunRelative("30/.step30.lock")
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
		LockPath:        lockPath,
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

type resumeState struct {
	agents map[contracts.AgentID]*resumeAgentState
}

type resumeAgentState struct {
	rawScores       map[contracts.JudgeRole]map[contracts.Dimension]contracts.RawScoreEntry
	rawCompliance   map[contracts.JudgeRole]map[string]contracts.RawComplianceEntry
	finalScores     map[contracts.Dimension]contracts.ScoreEntry
	finalCompliance map[string]contracts.ComplianceEntry
}

func loadResumeState(paths stepPathsResult, expectedComplianceRules map[string]struct{}) (*resumeState, error) {
	scoreRaw, err := internalio.ReadJSONL[contracts.RawScoreEntry](paths.ScoreRaw)
	if err != nil {
		return nil, err
	}
	complianceRaw, err := internalio.ReadJSONL[contracts.RawComplianceEntry](paths.ComplianceRaw)
	if err != nil {
		return nil, err
	}
	scoreFinal, err := internalio.ReadJSONL[contracts.ScoreEntry](paths.ScoreFinal)
	if err != nil {
		return nil, err
	}
	complianceFinal, err := internalio.ReadJSONL[contracts.ComplianceEntry](paths.ComplianceFinal)
	if err != nil {
		return nil, err
	}

	state := &resumeState{agents: make(map[contracts.AgentID]*resumeAgentState)}
	for _, row := range scorecore.CollapseRawScores(scoreRaw) {
		state.agent(row.Agent).upsertRawScores([]contracts.RawScoreEntry{row})
	}
	for _, row := range scorecore.CollapseRawCompliance(complianceRaw) {
		if !complianceRuleExpected(row.RuleID, expectedComplianceRules) {
			continue
		}
		state.agent(row.Agent).upsertRawCompliance([]contracts.RawComplianceEntry{row})
	}
	for _, row := range scorecore.CollapseFinalScores(scoreFinal) {
		state.agent(row.Agent).upsertFinalScores([]contracts.ScoreEntry{row})
	}
	for _, row := range scorecore.CollapseFinalCompliance(complianceFinal) {
		if !complianceRuleExpected(row.RuleID, expectedComplianceRules) {
			continue
		}
		state.agent(row.Agent).upsertFinalCompliance([]contracts.ComplianceEntry{row})
	}
	return state, nil
}

func (s *resumeState) agent(agent contracts.AgentID) *resumeAgentState {
	if existing, ok := s.agents[agent]; ok {
		return existing
	}
	state := &resumeAgentState{
		rawScores: map[contracts.JudgeRole]map[contracts.Dimension]contracts.RawScoreEntry{
			contracts.JudgeRolePrimary:   {},
			contracts.JudgeRoleSecondary: {},
			contracts.JudgeRoleArbiter:   {},
		},
		rawCompliance: map[contracts.JudgeRole]map[string]contracts.RawComplianceEntry{
			contracts.JudgeRolePrimary:   {},
			contracts.JudgeRoleSecondary: {},
			contracts.JudgeRoleArbiter:   {},
		},
		finalScores:     make(map[contracts.Dimension]contracts.ScoreEntry),
		finalCompliance: make(map[string]contracts.ComplianceEntry),
	}
	s.agents[agent] = state
	return state
}

func (s *resumeAgentState) upsertRawScores(rows []contracts.RawScoreEntry) {
	for _, row := range rows {
		s.rawScores[row.JudgeRole][row.Dimension] = row
	}
}

func (s *resumeAgentState) replaceRawScores(role contracts.JudgeRole, rows []contracts.RawScoreEntry) {
	s.rawScores[role] = make(map[contracts.Dimension]contracts.RawScoreEntry, len(rows))
	s.upsertRawScores(rows)
}

func (s *resumeAgentState) upsertRawCompliance(rows []contracts.RawComplianceEntry) {
	for _, row := range rows {
		s.rawCompliance[row.JudgeRole][row.RuleID] = row
	}
}

func (s *resumeAgentState) replaceRawCompliance(role contracts.JudgeRole, rows []contracts.RawComplianceEntry) {
	s.rawCompliance[role] = make(map[string]contracts.RawComplianceEntry, len(rows))
	s.upsertRawCompliance(rows)
}

func (s *resumeAgentState) upsertFinalScores(rows []contracts.ScoreEntry) {
	for _, row := range rows {
		s.finalScores[row.Dimension] = row
	}
}

func (s *resumeAgentState) upsertFinalCompliance(rows []contracts.ComplianceEntry) {
	for _, row := range rows {
		s.finalCompliance[row.RuleID] = row
	}
}

func (s *resumeAgentState) clearArbiter() {
	s.rawScores[contracts.JudgeRoleArbiter] = map[contracts.Dimension]contracts.RawScoreEntry{}
	s.rawCompliance[contracts.JudgeRoleArbiter] = map[string]contracts.RawComplianceEntry{}
}

func (s *resumeAgentState) clearFinal() {
	s.finalScores = make(map[contracts.Dimension]contracts.ScoreEntry)
	s.finalCompliance = make(map[string]contracts.ComplianceEntry)
}

func (s *resumeAgentState) roleComplete(role contracts.JudgeRole, outputSha, rubricVersion, promptVersion string, expectedRules map[string]struct{}) bool {
	if !hasAllDimensions(s.rawScores[role]) {
		return false
	}
	if !s.roleOutputShaMatches(role, outputSha) || !s.roleVersionMatches(role, rubricVersion, promptVersion) {
		return false
	}
	return s.roleComplianceCoverage(role, expectedRules)
}

func (s *resumeAgentState) rawScoreSlice(role contracts.JudgeRole) []contracts.RawScoreEntry {
	out := make([]contracts.RawScoreEntry, 0, len(s.rawScores[role]))
	for _, dim := range allDimensions() {
		row, ok := s.rawScores[role][dim]
		if ok {
			out = append(out, row)
		}
	}
	return out
}

func (s *resumeAgentState) rawComplianceSlice(role contracts.JudgeRole) []contracts.RawComplianceEntry {
	rules := make([]string, 0, len(s.rawCompliance[role]))
	for ruleID := range s.rawCompliance[role] {
		rules = append(rules, ruleID)
	}
	sort.Strings(rules)
	out := make([]contracts.RawComplianceEntry, 0, len(rules))
	for _, ruleID := range rules {
		out = append(out, s.rawCompliance[role][ruleID])
	}
	return out
}

func (s *resumeAgentState) arbiterCompleteFor(
	expectedRules map[string]struct{},
	outputSha, rubricVersion, promptVersion string,
) bool {
	if !hasAllDimensions(s.rawScores[contracts.JudgeRoleArbiter]) {
		return false
	}
	if !s.roleOutputShaMatches(contracts.JudgeRoleArbiter, outputSha) {
		return false
	}
	if !s.roleVersionMatches(contracts.JudgeRoleArbiter, rubricVersion, promptVersion) {
		return false
	}
	return s.roleComplianceCoverage(contracts.JudgeRoleArbiter, expectedRules)
}

func (s *resumeAgentState) roleVersionMatches(role contracts.JudgeRole, rubricVersion, promptVersion string) bool {
	for _, row := range s.rawScores[role] {
		if row.RubricVersion != rubricVersion || row.PromptVersion != promptVersion {
			return false
		}
	}
	for _, row := range s.rawCompliance[role] {
		if row.RubricVersion != rubricVersion || row.PromptVersion != promptVersion {
			return false
		}
	}
	return true
}

func (s *resumeAgentState) roleOutputShaMatches(role contracts.JudgeRole, outputSha string) bool {
	for _, row := range s.rawScores[role] {
		if row.OutputSha256 != outputSha {
			return false
		}
	}
	for _, row := range s.rawCompliance[role] {
		if row.OutputSha256 != outputSha {
			return false
		}
	}
	return true
}

func (s *resumeAgentState) roleComplianceCoverage(role contracts.JudgeRole, expected map[string]struct{}) bool {
	if len(expected) == 0 {
		return len(s.rawCompliance[role]) == 0
	}
	if len(s.rawCompliance[role]) != len(expected) {
		return false
	}
	for ruleID := range expected {
		if _, ok := s.rawCompliance[role][ruleID]; !ok {
			return false
		}
	}
	return true
}

func expectedComplianceRuleSet(ruleIDs []string) map[string]struct{} {
	if len(ruleIDs) == 0 {
		return nil
	}
	rules := make(map[string]struct{}, len(ruleIDs))
	for _, ruleID := range ruleIDs {
		rules[ruleID] = struct{}{}
	}
	return rules
}

func expectedStep30ComplianceRuleIDs(rubricPath string) ([]string, error) {
	activeRuleIDs, err := judges.ActiveComplianceRuleIDs(rubricPath)
	if err != nil {
		return nil, err
	}
	if len(activeRuleIDs) > 0 {
		return activeRuleIDs, nil
	}
	return judges.ExpectedComplianceRuleIDs(rubricPath)
}

func complianceRuleExpected(ruleID string, expected map[string]struct{}) bool {
	if len(expected) == 0 {
		return false
	}
	_, ok := expected[ruleID]
	return ok
}

func filterRawComplianceRows(rows []contracts.RawComplianceEntry, expected map[string]struct{}) []contracts.RawComplianceEntry {
	out := make([]contracts.RawComplianceEntry, 0, len(rows))
	for _, row := range rows {
		if complianceRuleExpected(row.RuleID, expected) {
			out = append(out, row)
		}
	}
	return out
}

func filterFinalComplianceRows(rows []contracts.ComplianceEntry, expected map[string]struct{}) []contracts.ComplianceEntry {
	out := make([]contracts.ComplianceEntry, 0, len(rows))
	for _, row := range rows {
		if complianceRuleExpected(row.RuleID, expected) {
			out = append(out, row)
		}
	}
	return out
}

func disputedComplianceRuleIDs(primary, secondary []contracts.RawComplianceEntry) []string {
	primaryByRule := make(map[string]contracts.ComplianceVerdict, len(primary))
	for _, row := range primary {
		primaryByRule[row.RuleID] = row.Verdict
	}
	secondaryByRule := make(map[string]contracts.ComplianceVerdict, len(secondary))
	for _, row := range secondary {
		secondaryByRule[row.RuleID] = row.Verdict
	}
	disputed := make(map[string]struct{})
	for ruleID, primaryVerdict := range primaryByRule {
		secondaryVerdict, ok := secondaryByRule[ruleID]
		if !ok || secondaryVerdict != primaryVerdict {
			disputed[ruleID] = struct{}{}
		}
	}
	for ruleID := range secondaryByRule {
		if _, ok := primaryByRule[ruleID]; !ok {
			disputed[ruleID] = struct{}{}
		}
	}
	out := make([]string, 0, len(disputed))
	for ruleID := range disputed {
		out = append(out, ruleID)
	}
	sort.Strings(out)
	return out
}

func appendExpectedFinalScores(paths stepPathsResult, state *resumeAgentState, result scorecore.PanelResult) error {
	for _, row := range result.FinalScores {
		current, ok := state.finalScores[row.Dimension]
		if ok {
			same, err := sameCanonicalJSON(current, row)
			if err != nil {
				return err
			}
			if same {
				continue
			}
		}
		if err := internalio.AppendJSONL(paths.ScoreFinal, row); err != nil {
			return err
		}
		state.upsertFinalScores([]contracts.ScoreEntry{row})
	}
	return nil
}

func appendExpectedFinalCompliance(paths stepPathsResult, state *resumeAgentState, result scorecore.PanelResult) error {
	for _, row := range result.FinalCompliance {
		current, ok := state.finalCompliance[row.RuleID]
		if ok {
			same, err := sameCanonicalJSON(current, row)
			if err != nil {
				return err
			}
			if same {
				continue
			}
		}
		if err := internalio.AppendJSONL(paths.ComplianceFinal, row); err != nil {
			return err
		}
		state.upsertFinalCompliance([]contracts.ComplianceEntry{row})
	}
	return nil
}

func resetStep30FinalFiles(paths stepPathsResult) error {
	if err := rewriteJSONL[contracts.ScoreEntry](paths.ScoreFinal, nil); err != nil {
		return err
	}
	return rewriteJSONL[contracts.ComplianceEntry](paths.ComplianceFinal, nil)
}

func rewriteJSONL[T any](path string, rows []T) error {
	var buf bytes.Buffer
	for _, row := range rows {
		if _, err := contracts.MarshalStrict(row); err != nil {
			return err
		}
		payload, err := contracts.CanonicalMarshal(row)
		if err != nil {
			return err
		}
		if len(payload)+1 > internalio.JSONLMaxLineBytes {
			return internalio.ErrEntryTooLarge
		}
		buf.Write(payload)
		buf.WriteByte('\n')
	}
	return internalio.WriteAtomic(path, buf.Bytes())
}

func fileExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func sameCanonicalJSON(left, right any) (bool, error) {
	leftJSON, err := contracts.CanonicalMarshal(left)
	if err != nil {
		return false, err
	}
	rightJSON, err := contracts.CanonicalMarshal(right)
	if err != nil {
		return false, err
	}
	return bytes.Equal(leftJSON, rightJSON), nil
}

func hasAllDimensions(rows map[contracts.Dimension]contracts.RawScoreEntry) bool {
	for _, dim := range allDimensions() {
		if _, ok := rows[dim]; !ok {
			return false
		}
	}
	return true
}

func allDimensions() []contracts.Dimension {
	return []contracts.Dimension{
		contracts.DimensionFidelity,
		contracts.DimensionCorrectness,
		contracts.DimensionMaintainability,
		contracts.DimensionDiscipline,
		contracts.DimensionCommunication,
	}
}

func step30VersionsMatch(paths stepPathsResult, rubricVersion, promptVersion string, expectedComplianceRules map[string]struct{}) (bool, error) {
	scoreRaw, err := internalio.ReadJSONL[contracts.RawScoreEntry](paths.ScoreRaw)
	if err != nil {
		return false, err
	}
	complianceRaw, err := internalio.ReadJSONL[contracts.RawComplianceEntry](paths.ComplianceRaw)
	if err != nil {
		return false, err
	}
	scoreFinal, err := internalio.ReadJSONL[contracts.ScoreEntry](paths.ScoreFinal)
	if err != nil {
		return false, err
	}
	complianceFinal, err := internalio.ReadJSONL[contracts.ComplianceEntry](paths.ComplianceFinal)
	if err != nil {
		return false, err
	}
	rawComplianceRows := filterRawComplianceRows(scorecore.CollapseRawCompliance(complianceRaw), expectedComplianceRules)
	finalComplianceRows := filterFinalComplianceRows(scorecore.CollapseFinalCompliance(complianceFinal), expectedComplianceRules)
	return scorecore.RowsMatchVersion(scorecore.CollapseRawScores(scoreRaw), func(row contracts.RawScoreEntry) (string, string) {
		return row.RubricVersion, row.PromptVersion
	}, rubricVersion, promptVersion) &&
		scorecore.RowsMatchVersion(rawComplianceRows, func(row contracts.RawComplianceEntry) (string, string) {
			return row.RubricVersion, row.PromptVersion
		}, rubricVersion, promptVersion) &&
		scorecore.RowsMatchVersion(scorecore.CollapseFinalScores(scoreFinal), func(row contracts.ScoreEntry) (string, string) {
			return row.RubricVersion, row.PromptVersion
		}, rubricVersion, promptVersion) &&
		scorecore.RowsMatchVersion(finalComplianceRows, func(row contracts.ComplianceEntry) (string, string) {
			return row.RubricVersion, row.PromptVersion
		}, rubricVersion, promptVersion), nil
}

func (s *Step) resolveRubricPath(runCtx internalio.RunContext) (string, error) {
	if s.rubricPathFn != nil {
		return s.rubricPathFn(runCtx)
	}
	path, err := judges.ResolveRunRubricPath(runCtx)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrRubricPathUnresolved, err)
	}
	return path, nil
}

func effectiveRubricVersion(baseVersion, rubricPath string) (string, error) {
	hash, err := fileSha256(rubricPath)
	if err != nil {
		return "", fmt.Errorf("step30_score: hash rubric: %w", err)
	}
	base := strings.TrimSpace(baseVersion)
	if base == "" {
		base = defaultRubricVersion
	}
	return fmt.Sprintf("%s+sha256:%s", base, hash[:12]), nil
}

func fileSha256(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// snapshotAndHashDiff materializes a read-only snapshot of the manifest diff
// under the run directory and hashes the bytes that were snapshotted. The
// judge receives the snapshot path as OutputPath so a concurrent
// rename/symlink swap between the hash call and the judge read cannot split
// hash-bytes from score-bytes (F16 TOCTOU).
//
// Snapshots are per-agent and content-addressed by sha256 so reruns that
// encounter identical diffs are no-ops. Old snapshots for the same agent
// are removed before the new one is written so the run directory does not
// accumulate stale copies across resume cycles.
func snapshotAndHashDiff(runCtx internalio.RunContext, agent contracts.AgentID, diffAbs string) (string, string, error) {
	if err := contracts.EnsureCleanAbsolutePath(diffAbs); err != nil {
		return "", "", err
	}
	// os.ReadFile follows symlinks — that is acceptable because we pin the
	// exact bytes we read into a snapshot and hash those bytes, not the
	// live path. A post-read swap of the original symlink does not affect
	// subsequent scoring.
	data, err := os.ReadFile(diffAbs)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])

	snapshotDir, err := runCtx.ResolveRunRelative(filepath.Join("30", "snapshots"))
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(snapshotDir, 0o755); err != nil {
		return "", "", err
	}
	// Content-address by sha256 so we never rewrite an already-pinned byte
	// sequence, and so concurrent resume attempts converge.
	fileName := fmt.Sprintf("%s-%s.patch", string(agent), hash)
	snapshotPath := filepath.Join(snapshotDir, fileName)
	if err := contracts.EnsureCleanAbsolutePath(snapshotPath); err != nil {
		return "", "", err
	}

	// Fast path: snapshot already exists with matching content.
	if existing, err := os.ReadFile(snapshotPath); err == nil && bytesEqual(existing, data) {
		return snapshotPath, hash, nil
	}

	// Atomic write into the snapshot path.
	tmp, err := os.CreateTemp(snapshotDir, string(agent)+"-snap-*.tmp")
	if err != nil {
		return "", "", err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return "", "", err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return "", "", err
	}
	if err := os.Rename(tmpName, snapshotPath); err != nil {
		_ = os.Remove(tmpName)
		// Concurrent snapshotter may have landed first.
		if existing, verr := os.ReadFile(snapshotPath); verr == nil && bytesEqual(existing, data) {
			return snapshotPath, hash, nil
		}
		return "", "", err
	}

	// Best-effort cleanup of stale snapshots for the same agent from prior
	// resume cycles (different hash). Failures are non-fatal — a stale file
	// cannot affect correctness because we always name by current hash.
	if entries, rerr := os.ReadDir(snapshotDir); rerr == nil {
		prefix := string(agent) + "-"
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasPrefix(name, prefix) || name == fileName {
				continue
			}
			_ = os.Remove(filepath.Join(snapshotDir, name))
		}
	}

	return snapshotPath, hash, nil
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
