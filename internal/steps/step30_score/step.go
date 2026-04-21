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
func (s *Step) Run(ctx context.Context, req Request) (runErr error) {
	if req.TaskPackage == nil {
		return ErrNoTaskPackage
	}

	lockPath, err := req.RunContext.ResolveRunRelative("30/.step30.lock")
	if err != nil {
		return err
	}
	lock, err := internalio.AcquireFileLock(lockPath)
	if err != nil {
		return err
	}
	defer func() {
		if unlockErr := lock.Unlock(); runErr == nil && unlockErr != nil {
			runErr = unlockErr
		}
	}()

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

		panelInput := scorecore.PanelInput{
			Primary:               primary,
			Secondary:             secondary,
			Arbiter:               arbiter,
			JudgeInput:            judgeInput,
			OutputSha256:          outputSha,
			DisagreementThreshold: s.threshold,
			RunContext:            req.RunContext,
			StepDir:               "30",
		}
		agentState := state.agent(agent.agent)

		if err := s.ensureRole(ctx, paths, panelInput, agentState, contracts.JudgeRolePrimary, primary); err != nil {
			return fmt.Errorf("step30_score: resolve primary agent=%s: %w", agent.agent, err)
		}
		if secondary != nil {
			if err := s.ensureRole(ctx, paths, panelInput, agentState, contracts.JudgeRoleSecondary, secondary); err != nil {
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
			if !agentState.arbiterCompleteFor(primaryCompliance, panelInput.OutputSha256) {
				if err := s.runRole(ctx, paths, panelInput, agentState, contracts.JudgeRoleArbiter, arbiter); err != nil {
					return fmt.Errorf("step30_score: resolve arbiter agent=%s: %w", agent.agent, err)
				}
			}
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
		if err := appendExpectedFinal(paths, agentState, result); err != nil {
			return fmt.Errorf("step30_score: append final agent=%s: %w", agent.agent, err)
		}
	}

	agentIDs := make([]contracts.AgentID, 0, len(scorableAgents))
	for _, a := range scorableAgents {
		agentIDs = append(agentIDs, a.agent)
	}
	if err := rewriteExpectedFinalCompliance(paths, state, agentIDs); err != nil {
		return fmt.Errorf("step30_score: rewrite final compliance: %w", err)
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
) error {
	if agentState.roleComplete(role, panelInput.OutputSha256) {
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
	if role == contracts.JudgeRolePrimary || role == contracts.JudgeRoleSecondary {
		agentState.clearArbiter()
	}
	agentState.replaceRawScores(role, result.RawScores)
	agentState.replaceRawCompliance(role, result.RawCompliance)
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

type resumeState struct {
	agents map[contracts.AgentID]*resumeAgentState
}

type resumeAgentState struct {
	rawScores       map[contracts.JudgeRole]map[contracts.Dimension]contracts.RawScoreEntry
	rawCompliance   map[contracts.JudgeRole]map[string]contracts.RawComplianceEntry
	finalScores     map[contracts.Dimension]contracts.ScoreEntry
	finalCompliance map[string]contracts.ComplianceEntry
}

func loadResumeState(paths stepPathsResult) (*resumeState, error) {
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
	for _, row := range currentRawScores(scoreRaw) {
		state.agent(row.Agent).upsertRawScores([]contracts.RawScoreEntry{row})
	}
	for _, row := range currentRawCompliance(complianceRaw) {
		state.agent(row.Agent).upsertRawCompliance([]contracts.RawComplianceEntry{row})
	}
	for _, row := range scorecore.CollapseFinalScores(scoreFinal) {
		state.agent(row.Agent).upsertFinalScores([]contracts.ScoreEntry{row})
	}
	for _, row := range scorecore.CollapseFinalCompliance(complianceFinal) {
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
	replaced := make(map[contracts.Dimension]contracts.RawScoreEntry, len(rows))
	for _, row := range rows {
		replaced[row.Dimension] = row
	}
	s.rawScores[role] = replaced
}

func (s *resumeAgentState) upsertRawCompliance(rows []contracts.RawComplianceEntry) {
	for _, row := range rows {
		s.rawCompliance[row.JudgeRole][row.RuleID] = row
	}
}

func (s *resumeAgentState) replaceRawCompliance(role contracts.JudgeRole, rows []contracts.RawComplianceEntry) {
	replaced := make(map[string]contracts.RawComplianceEntry, len(rows))
	for _, row := range rows {
		replaced[row.RuleID] = row
	}
	s.rawCompliance[role] = replaced
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

func (s *resumeAgentState) replaceFinalCompliance(rows []contracts.ComplianceEntry) {
	replaced := make(map[string]contracts.ComplianceEntry, len(rows))
	for _, row := range rows {
		replaced[row.RuleID] = row
	}
	s.finalCompliance = replaced
}

func (s *resumeAgentState) clearArbiter() {
	s.rawScores[contracts.JudgeRoleArbiter] = map[contracts.Dimension]contracts.RawScoreEntry{}
	s.rawCompliance[contracts.JudgeRoleArbiter] = map[string]contracts.RawComplianceEntry{}
}

func (s *resumeAgentState) roleComplete(role contracts.JudgeRole, outputSha string) bool {
	return hasAllDimensions(s.rawScores[role]) && s.roleOutputShaMatches(role, outputSha)
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

func (s *resumeAgentState) arbiterCompleteFor(primaryCompliance []contracts.RawComplianceEntry, outputSha string) bool {
	if !hasAllDimensions(s.rawScores[contracts.JudgeRoleArbiter]) {
		return false
	}
	if !s.roleOutputShaMatches(contracts.JudgeRoleArbiter, outputSha) {
		return false
	}
	if len(s.rawCompliance[contracts.JudgeRoleArbiter]) != len(primaryCompliance) {
		return false
	}
	for _, row := range primaryCompliance {
		if _, ok := s.rawCompliance[contracts.JudgeRoleArbiter][row.RuleID]; !ok {
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

func appendExpectedFinal(paths stepPathsResult, state *resumeAgentState, result scorecore.PanelResult) error {
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
	state.replaceFinalCompliance(result.FinalCompliance)
	return nil
}

func rewriteExpectedFinalCompliance(paths stepPathsResult, state *resumeState, agents []contracts.AgentID) error {
	sortedAgents := append([]contracts.AgentID(nil), agents...)
	sort.Slice(sortedAgents, func(i, j int) bool {
		return sortedAgents[i] < sortedAgents[j]
	})

	var buffer bytes.Buffer
	for _, agent := range sortedAgents {
		agentState, ok := state.agents[agent]
		if !ok {
			continue
		}
		rules := make([]string, 0, len(agentState.finalCompliance))
		for ruleID := range agentState.finalCompliance {
			rules = append(rules, ruleID)
		}
		sort.Strings(rules)
		for _, ruleID := range rules {
			payload, err := contracts.CanonicalMarshal(agentState.finalCompliance[ruleID])
			if err != nil {
				return err
			}
			buffer.Write(payload)
			buffer.WriteByte('\n')
		}
	}
	return internalio.WriteAtomic(paths.ComplianceFinal, buffer.Bytes())
}

func currentRawScores(rows []contracts.RawScoreEntry) []contracts.RawScoreEntry {
	if len(rows) == 0 {
		return nil
	}
	latestOutputByRole := make(map[[2]string]string, len(rows))
	filtered := make([]contracts.RawScoreEntry, 0, len(rows))
	for _, row := range rows {
		latestOutputByRole[[2]string{string(row.Agent), string(row.JudgeRole)}] = row.OutputSha256
	}
	for _, row := range rows {
		if latestOutputByRole[[2]string{string(row.Agent), string(row.JudgeRole)}] == row.OutputSha256 {
			filtered = append(filtered, row)
		}
	}
	return scorecore.CollapseRawScores(filtered)
}

func currentRawCompliance(rows []contracts.RawComplianceEntry) []contracts.RawComplianceEntry {
	if len(rows) == 0 {
		return nil
	}
	latestOutputByRole := make(map[[2]string]string, len(rows))
	filtered := make([]contracts.RawComplianceEntry, 0, len(rows))
	for _, row := range rows {
		latestOutputByRole[[2]string{string(row.Agent), string(row.JudgeRole)}] = row.OutputSha256
	}
	for _, row := range rows {
		if latestOutputByRole[[2]string{string(row.Agent), string(row.JudgeRole)}] == row.OutputSha256 {
			filtered = append(filtered, row)
		}
	}
	return scorecore.CollapseRawCompliance(filtered)
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
