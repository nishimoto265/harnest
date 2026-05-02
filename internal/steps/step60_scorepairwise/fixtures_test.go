package step60_scorepairwise

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/judges"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fixtureOptions struct {
	agents                 []contracts.AgentID
	writePass1Score        bool
	missingPass2Agents     map[contracts.AgentID]bool
	nonScorablePass1Agents map[contracts.AgentID]bool
	nonScorablePass2Agents map[contracts.AgentID]bool
	// pass1RubricVersion / pass1PromptVersion override the scoring-version
	// metadata stamped on the pass1 scores-A.jsonl fixture rows. Leaving them
	// empty keeps the historical stub defaults (default / phase0-stub).
	pass1RubricVersion string
	pass1PromptVersion string
}

type scriptedJudge struct {
	score            int
	reasonPrefix     string
	compliance       map[string]contracts.ComplianceVerdict
	resolvedAt       time.Time
	strictCompliance bool
}

type mutatingReadJudge struct {
	delegate    scriptedJudge
	targetAgent contracts.AgentID
	mutatePath  string
	mutateBytes []byte
	seenPath    string
	seenBytes   []byte
}

type echoExpectedComplianceJudge struct {
	score  int
	inputs []judges.JudgeInput
}

type overflowRefJudge struct{}
type duplicateComplianceJudge struct{}
type versionedJudge struct {
	delegate judges.Judge
	version  string
}
type recordingPairwiseJudge struct {
	orders []string
}
type recordingPairwiseDecisionJudge struct {
	calls           int
	mode            judges.PairwiseMode
	pairCount       int
	comparisonCount int
}

func (j *recordingPairwiseJudge) ComparePairwise(_ context.Context, input judges.PairwiseInput) (judges.PairwiseComparison, error) {
	j.orders = append(j.orders, fmt.Sprintf("%s:%s", input.Agent, input.Order))
	winner := contracts.PairwiseWinnerB
	if input.Order == "BA" {
		// The step normalizes BA back to the canonical pass1/pass2 labels.
		winner = contracts.PairwiseWinnerA
	}
	return judges.PairwiseComparison{
		Agent:         input.Agent,
		Order:         input.Order,
		Winner:        winner,
		Margin:        contracts.PairwiseMarginClear,
		Justification: fmt.Sprintf("recorded %s comparison", input.Order),
		DimensionVotes: []judges.PairwiseDimensionVote{{
			Dimension: contracts.DimensionCorrectness,
			Winner:    winner,
			Reason:    "recorded dimension vote",
		}},
	}, nil
}

func (j *recordingPairwiseDecisionJudge) DecidePairwise(_ context.Context, input judges.PairwiseDecisionInput) (judges.PairwiseDecision, error) {
	j.calls++
	j.mode = input.Mode
	j.pairCount = len(input.Pairs)
	j.comparisonCount = len(input.Comparisons)

	decisions := make([]judges.PairwiseAgentDecision, 0, len(input.Pairs))
	for _, pair := range input.Pairs {
		decisions = append(decisions, judges.PairwiseAgentDecision{
			Agent:         pair.Agent,
			Winner:        contracts.PairwiseWinnerB,
			Margin:        contracts.PairwiseMarginClear,
			Justification: "recorded agent decision",
		})
	}
	return judges.PairwiseDecision{
		Action:         judges.PairwiseDecisionAdopt,
		Justification:  "recorded final decision",
		AgentDecisions: decisions,
	}, nil
}

func (j *echoExpectedComplianceJudge) ScoreOutput(ctx context.Context, input judges.JudgeInput) (judges.JudgeOutput, error) {
	j.inputs = append(j.inputs, input)
	if err := input.Validate(); err != nil {
		return judges.JudgeOutput{}, err
	}
	select {
	case <-ctx.Done():
		return judges.JudgeOutput{}, ctx.Err()
	default:
	}

	scores := make([]contracts.ScoreEntry, 0, len(canonicalDimensions))
	for _, dimension := range canonicalDimensions {
		scores = append(scores, contracts.ScoreEntry{
			SchemaVersion: "1",
			RunID:         input.RunID,
			Pass:          input.Pass,
			Agent:         input.Agent,
			Dimension:     dimension,
			Score:         j.score,
			Reasons:       fmt.Sprintf("echo-%s", dimension),
			VerdictPath:   contracts.VerdictPathSingle,
			RubricVersion: "echo-rubric",
			PromptVersion: "echo-prompt",
			ResolvedAt:    time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
		})
	}
	compliance := make([]contracts.ComplianceEntry, 0, len(input.ExpectedComplianceRuleIDs))
	for _, ruleID := range input.ExpectedComplianceRuleIDs {
		compliance = append(compliance, contracts.ComplianceEntry{
			SchemaVersion: "1",
			RunID:         input.RunID,
			Pass:          input.Pass,
			Agent:         input.Agent,
			RuleID:        ruleID,
			Verdict:       contracts.ComplianceVerdictCompliant,
			Rationale:     "echo compliant",
			VerdictPath:   contracts.VerdictPathSingle,
			RubricVersion: "echo-rubric",
			PromptVersion: "echo-prompt",
			ResolvedAt:    time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
		})
	}
	output := judges.JudgeOutput{Scores: scores, Compliance: compliance}
	return output, output.ValidateFor(input)
}

func (j *mutatingReadJudge) ScoreOutput(ctx context.Context, input judges.JudgeInput) (judges.JudgeOutput, error) {
	if input.Agent != j.targetAgent {
		return j.delegate.ScoreOutput(ctx, input)
	}
	if err := os.WriteFile(j.mutatePath, j.mutateBytes, 0o644); err != nil {
		return judges.JudgeOutput{}, err
	}
	data, err := os.ReadFile(input.OutputPath)
	if err != nil {
		return judges.JudgeOutput{}, err
	}
	j.seenPath = input.OutputPath
	j.seenBytes = append([]byte(nil), data...)
	return j.delegate.ScoreOutput(ctx, input)
}

func (j scriptedJudge) ScoreOutput(ctx context.Context, input judges.JudgeInput) (judges.JudgeOutput, error) {
	if err := input.Validate(); err != nil {
		return judges.JudgeOutput{}, err
	}
	select {
	case <-ctx.Done():
		return judges.JudgeOutput{}, ctx.Err()
	default:
	}

	scores := make([]contracts.ScoreEntry, 0, len(canonicalDimensions))
	resolvedAt := j.resolvedAt
	if resolvedAt.IsZero() {
		resolvedAt = time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC)
	}
	for _, dimension := range canonicalDimensions {
		scores = append(scores, contracts.ScoreEntry{
			SchemaVersion: "1",
			RunID:         input.RunID,
			Pass:          input.Pass,
			Agent:         input.Agent,
			Dimension:     dimension,
			Score:         j.score,
			Reasons:       fmt.Sprintf("%s-%s", j.reasonPrefix, dimension),
			VerdictPath:   contracts.VerdictPathSingle,
			RubricVersion: "scripted-rubric",
			PromptVersion: "scripted-prompt",
			ResolvedAt:    resolvedAt,
		})
	}

	ruleIDs := make([]string, 0, len(j.compliance))
	for ruleID := range j.compliance {
		ruleIDs = append(ruleIDs, ruleID)
	}
	if !j.strictCompliance && len(input.ExpectedComplianceRuleIDs) > 0 {
		ruleIDs = append([]string(nil), input.ExpectedComplianceRuleIDs...)
	} else if !j.strictCompliance && input.EnforceExpectedCompliance {
		ruleIDs = nil
	} else if !j.strictCompliance {
		for _, ruleID := range input.ExpectedComplianceRuleIDs {
			if _, ok := j.compliance[ruleID]; !ok {
				ruleIDs = append(ruleIDs, ruleID)
			}
		}
	}
	sort.Strings(ruleIDs)

	compliance := make([]contracts.ComplianceEntry, 0, len(ruleIDs))
	for _, ruleID := range ruleIDs {
		verdict, ok := j.compliance[ruleID]
		if !ok {
			verdict = contracts.ComplianceVerdictCompliant
		}
		compliance = append(compliance, contracts.ComplianceEntry{
			SchemaVersion: "1",
			RunID:         input.RunID,
			Pass:          input.Pass,
			Agent:         input.Agent,
			RuleID:        ruleID,
			Verdict:       verdict,
			Rationale:     fmt.Sprintf("%s-%s", ruleID, verdict),
			VerdictPath:   contracts.VerdictPathSingle,
			RubricVersion: "scripted-rubric",
			PromptVersion: "scripted-prompt",
			ResolvedAt:    resolvedAt,
		})
	}

	output := judges.JudgeOutput{
		Scores:     scores,
		Compliance: compliance,
	}
	return output, output.ValidateFor(input)
}

func (overflowRefJudge) ScoreOutput(ctx context.Context, input judges.JudgeInput) (judges.JudgeOutput, error) {
	if err := input.Validate(); err != nil {
		return judges.JudgeOutput{}, err
	}
	select {
	case <-ctx.Done():
		return judges.JudgeOutput{}, ctx.Err()
	default:
	}

	inlineText := "judge supplied inline text"
	bogus := &contracts.OverflowRef{Path: "60/reasons/bogus.txt", Sha256: strings.Repeat("f", 64)}
	scores := make([]contracts.ScoreEntry, 0, len(canonicalDimensions))
	for _, dimension := range canonicalDimensions {
		scores = append(scores, contracts.ScoreEntry{
			SchemaVersion:      "1",
			RunID:              input.RunID,
			Pass:               input.Pass,
			Agent:              input.Agent,
			Dimension:          dimension,
			Score:              80,
			Reasons:            inlineText,
			ReasonsOverflowRef: bogus,
			VerdictPath:        contracts.VerdictPathSingle,
			RubricVersion:      "overflow-rubric",
			PromptVersion:      "overflow-prompt",
			ResolvedAt:         time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
		})
	}
	ruleIDs := append([]string(nil), input.ExpectedComplianceRuleIDs...)
	if len(ruleIDs) == 0 {
		ruleIDs = append(ruleIDs, "shared")
	}
	sort.Strings(ruleIDs)
	compliance := make([]contracts.ComplianceEntry, 0, len(ruleIDs))
	for _, ruleID := range ruleIDs {
		compliance = append(compliance, contracts.ComplianceEntry{
			SchemaVersion:        "1",
			RunID:                input.RunID,
			Pass:                 input.Pass,
			Agent:                input.Agent,
			RuleID:               ruleID,
			Verdict:              contracts.ComplianceVerdictCompliant,
			Rationale:            inlineText,
			RationaleOverflowRef: bogus,
			VerdictPath:          contracts.VerdictPathSingle,
			RubricVersion:        "overflow-rubric",
			PromptVersion:        "overflow-prompt",
			ResolvedAt:           time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
		})
	}
	output := judges.JudgeOutput{Scores: scores, Compliance: compliance}
	return output, output.ValidateFor(input)
}

func (duplicateComplianceJudge) ScoreOutput(ctx context.Context, input judges.JudgeInput) (judges.JudgeOutput, error) {
	output, err := scriptedJudge{
		score:        80,
		reasonPrefix: "duplicate",
		compliance:   map[string]contracts.ComplianceVerdict{"rule-x": contracts.ComplianceVerdictViolated},
	}.ScoreOutput(ctx, input)
	if err != nil {
		return judges.JudgeOutput{}, err
	}
	duplicate := output.Compliance[0]
	duplicate.Verdict = contracts.ComplianceVerdictCompliant
	duplicate.ResolvedAt = duplicate.ResolvedAt.Add(time.Second)
	output.Compliance = append(output.Compliance, duplicate)
	return output, output.ValidateFor(input)
}

func (j versionedJudge) ScoreOutput(ctx context.Context, input judges.JudgeInput) (judges.JudgeOutput, error) {
	return j.delegate.ScoreOutput(ctx, input)
}

func (j versionedJudge) JudgePromptVersion() string {
	return j.version
}

type cancelingJudge struct {
	delegate scriptedJudge
	cancel   context.CancelFunc
}

func (j cancelingJudge) ScoreOutput(ctx context.Context, input judges.JudgeInput) (judges.JudgeOutput, error) {
	output, err := j.delegate.ScoreOutput(ctx, input)
	if j.cancel != nil {
		j.cancel()
	}
	return output, err
}

type unexpectedCallJudge struct {
	called *bool
}

func (j unexpectedCallJudge) ScoreOutput(ctx context.Context, input judges.JudgeInput) (judges.JudgeOutput, error) {
	if j.called != nil {
		*j.called = true
	}
	return judges.JudgeOutput{}, errors.New("unexpected judge call")
}

type blockingJudge struct {
	delegate scriptedJudge
	started  chan struct{}
	release  chan struct{}
	once     sync.Once
	calls    int32
}

func (j *blockingJudge) ScoreOutput(ctx context.Context, input judges.JudgeInput) (judges.JudgeOutput, error) {
	atomic.AddInt32(&j.calls, 1)
	j.once.Do(func() {
		close(j.started)
		<-j.release
	})
	return j.delegate.ScoreOutput(ctx, input)
}

func (j *blockingJudge) callCount() int32 {
	return atomic.LoadInt32(&j.calls)
}

type countingJudge struct {
	delegate judges.Judge
	calls    int32
}

func (j *countingJudge) ScoreOutput(ctx context.Context, input judges.JudgeInput) (judges.JudgeOutput, error) {
	atomic.AddInt32(&j.calls, 1)
	return j.delegate.ScoreOutput(ctx, input)
}

func (j *countingJudge) callCount() int32 {
	return atomic.LoadInt32(&j.calls)
}

func seedStep60Fixture(t *testing.T, opts fixtureOptions) (internalio.RunContext, contracts.TaskPackage) {
	t.Helper()

	runsBase := filepath.Join(t.TempDir(), "runs")
	worktreeBase := filepath.Join(t.TempDir(), "worktrees")
	runID := contracts.RunID("2026-04-21-PR42-abcdef0")
	agents := opts.agents
	if len(agents) == 0 {
		agents = []contracts.AgentID{"a1", "a2", "a3"}
	}

	worktrees := make([]contracts.WorktreeAllocation, 0, len(agents)*2)
	for pass := 1; pass <= 2; pass++ {
		for _, agent := range agents {
			path := filepath.Join(worktreeBase, fmt.Sprintf("%s-pass%d-%s", runID, pass, agent))
			require.NoError(t, os.MkdirAll(path, 0o755))
			worktrees = append(worktrees, contracts.WorktreeAllocation{
				Agent:   agent,
				Pass:    pass,
				Path:    path,
				Branch:  filepath.ToSlash(filepath.Join("auto-improve", string(runID), fmt.Sprintf("pass%d", pass), string(agent))),
				BaseSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				HeadSHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			})
		}
	}

	pkg := contracts.TaskPackage{
		SchemaVersion:           "1",
		RunID:                   runID,
		PR:                      42,
		Title:                   "step60 fixture",
		BaseSHA:                 "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		BestBranch:              "best",
		ReconstructedTaskPrompt: "fixture prompt",
		Worktrees:               worktrees,
		CreatedAt:               time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
	}
	require.NoError(t, pkg.Validate())

	runIO, err := internalio.RunContextFromTaskPackage(pkg, runsBase, worktreeBase)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(runIO.RunDir(), 0o755))

	for _, agent := range agents {
		switch {
		case opts.nonScorablePass1Agents[agent]:
			writeManifestError(t, runIO, runID, 1, agent)
		default:
			writeManifestSuccess(t, runIO, runID, 1, agent)
		}
		switch {
		case opts.missingPass2Agents[agent]:
		case opts.nonScorablePass2Agents[agent]:
			writeManifestError(t, runIO, runID, 2, agent)
		default:
			writeManifestSuccess(t, runIO, runID, 2, agent)
		}
	}

	if opts.writePass1Score {
		rubricVersion := opts.pass1RubricVersion
		if rubricVersion == "" {
			rubricVersion = "default"
		}
		promptVersion := opts.pass1PromptVersion
		if promptVersion == "" {
			promptVersion = "phase0-stub"
		}
		writePass1ScoresAt(t, runIO, runID, agents, rubricVersion, promptVersion)
	}

	return runIO, pkg
}

func writeManifestSuccess(t *testing.T, runIO internalio.RunContext, runID contracts.RunID, pass int, agent contracts.AgentID) {
	t.Helper()

	prefix := filepath.Join("20-pass1", string(agent))
	if pass == 2 {
		prefix = filepath.Join("50-pass2", string(agent))
	}

	require.NoError(t, internalio.WriteAtomic(mustResolve(t, runIO, filepath.Join(prefix, "diff.patch")), []byte("diff\n")))
	require.NoError(t, internalio.WriteAtomic(mustResolve(t, runIO, filepath.Join(prefix, "session.jsonl")), []byte("{}\n")))
	require.NoError(t, internalio.WriteAtomic(mustResolve(t, runIO, filepath.Join(prefix, "checklist-result.json")), []byte("{}\n")))

	manifestPath, err := runIO.ManifestPath(pass, agent)
	require.NoError(t, err)
	manifest := contracts.Manifest{
		Kind: contracts.ManifestKindSuccess,
		Value: contracts.ManifestSuccess{
			Kind:          contracts.ManifestKindSuccess,
			SchemaVersion: "1",
			RunID:         runID,
			Pass:          pass,
			Agent:         agent,
			BranchName:    "auto-improve/fixture",
			HeadSHA:       "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			BaseSHA:       "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			DiffPath:      filepath.ToSlash(filepath.Join(prefix, "diff.patch")),
			SessionPath:   filepath.ToSlash(filepath.Join(prefix, "session.jsonl")),
			ChecklistPath: filepath.ToSlash(filepath.Join(prefix, "checklist-result.json")),
			PromptVersion: "stub-prompt-v1",
			StartedAt:     time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
			FinishedAt:    time.Date(2026, 4, 21, 0, 1, 0, 0, time.UTC),
		},
	}
	require.NoError(t, internalio.WriteJSONAtomic(manifestPath, manifest))
}

func writeManifestError(t *testing.T, runIO internalio.RunContext, runID contracts.RunID, pass int, agent contracts.AgentID) {
	t.Helper()

	manifestPath, err := runIO.ManifestPath(pass, agent)
	require.NoError(t, err)
	manifest := contracts.Manifest{
		Kind: contracts.ManifestKindError,
		Value: contracts.ManifestError{
			Kind:          contracts.ManifestKindError,
			SchemaVersion: "1",
			RunID:         runID,
			Pass:          pass,
			Agent:         agent,
			ExitCode:      1,
			Reason:        "unknown",
			Detail:        "fixture non-scorable manifest",
			StartedAt:     time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
			FinishedAt:    time.Date(2026, 4, 21, 0, 1, 0, 0, time.UTC),
		},
	}
	require.NoError(t, internalio.WriteJSONAtomic(manifestPath, manifest))
}

// writePass1ScoresAt lets tests seed step30 scores-A.jsonl with a specific
// rubric/prompt version so F8's fail-closed version check exercises the
// matching path.
func writePass1ScoresAt(t *testing.T, runIO internalio.RunContext, runID contracts.RunID, agents []contracts.AgentID, rubricVersion, promptVersion string) {
	t.Helper()

	path := mustResolve(t, runIO, "30/scores-A.jsonl")
	for _, agent := range agents {
		for _, entry := range primaryStubScores(runID, 1, agent) {
			entry.RubricVersion = rubricVersion
			entry.PromptVersion = promptVersion
			require.NoError(t, internalio.AppendJSONL(path, entry))
		}
	}
}

func appendPass1ScoresWithScore(t *testing.T, runIO internalio.RunContext, runID contracts.RunID, agents []contracts.AgentID, score int) {
	t.Helper()

	path := mustResolve(t, runIO, "30/scores-A.jsonl")
	for _, agent := range agents {
		for _, entry := range primaryStubScores(runID, 1, agent) {
			entry.Score = score
			entry.Reasons = fmt.Sprintf("pass1 score changed to %d", score)
			require.NoError(t, internalio.AppendJSONL(path, entry))
		}
	}
}

// rewritePass1ScoresAt clobbers the existing pass1 scores-A.jsonl with rows
// stamped at the supplied version — used by tests that simulate step30 being
// rerun after step60 picked a new rubric/prompt version.
func rewritePass1ScoresAt(t *testing.T, runIO internalio.RunContext, runID contracts.RunID, agents []contracts.AgentID, rubricVersion, promptVersion string) {
	t.Helper()
	path := mustResolve(t, runIO, "30/scores-A.jsonl")
	require.NoError(t, os.Remove(path))
	writePass1ScoresAt(t, runIO, runID, agents, rubricVersion, promptVersion)
}

func primaryStubScores(runID contracts.RunID, pass int, agent contracts.AgentID) []contracts.ScoreEntry {
	return []contracts.ScoreEntry{
		{SchemaVersion: "1", RunID: runID, Pass: pass, Agent: agent, Dimension: contracts.DimensionFidelity, Score: 84, Reasons: "stub primary fixture evaluated fidelity with deterministic phase-0 scoring.", VerdictPath: contracts.VerdictPathAgreement, RubricVersion: "default", PromptVersion: "phase0-stub", ResolvedAt: time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC)},
		{SchemaVersion: "1", RunID: runID, Pass: pass, Agent: agent, Dimension: contracts.DimensionCorrectness, Score: 82, Reasons: "stub primary fixture evaluated correctness with deterministic phase-0 scoring.", VerdictPath: contracts.VerdictPathAgreement, RubricVersion: "default", PromptVersion: "phase0-stub", ResolvedAt: time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC)},
		{SchemaVersion: "1", RunID: runID, Pass: pass, Agent: agent, Dimension: contracts.DimensionMaintainability, Score: 80, Reasons: "stub primary fixture evaluated maintainability with deterministic phase-0 scoring.", VerdictPath: contracts.VerdictPathAgreement, RubricVersion: "default", PromptVersion: "phase0-stub", ResolvedAt: time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC)},
		{SchemaVersion: "1", RunID: runID, Pass: pass, Agent: agent, Dimension: contracts.DimensionDiscipline, Score: 86, Reasons: "stub primary fixture evaluated discipline with deterministic phase-0 scoring.", VerdictPath: contracts.VerdictPathAgreement, RubricVersion: "default", PromptVersion: "phase0-stub", ResolvedAt: time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC)},
		{SchemaVersion: "1", RunID: runID, Pass: pass, Agent: agent, Dimension: contracts.DimensionCommunication, Score: 78, Reasons: "stub primary fixture evaluated communication with deterministic phase-0 scoring.", VerdictPath: contracts.VerdictPathAgreement, RubricVersion: "default", PromptVersion: "phase0-stub", ResolvedAt: time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC)},
	}
}

func assertArtifactsByteIdentical(t *testing.T, left, right internalio.RunContext) {
	t.Helper()
	assert.Equal(t, readStep60Artifacts(t, left), readStep60Artifacts(t, right))
}

func readStep60Artifacts(t *testing.T, runIO internalio.RunContext) map[string][]byte {
	t.Helper()
	return map[string][]byte{
		"60/scores-B.jsonl":         mustReadFile(t, mustResolve(t, runIO, "60/scores-B.jsonl")),
		"60/compliance-B.jsonl":     mustReadFile(t, mustResolve(t, runIO, "60/compliance-B.jsonl")),
		"60/pairwise.jsonl":         mustReadFile(t, mustResolve(t, runIO, "60/pairwise.jsonl")),
		"60/scores-B-raw.jsonl":     mustReadFile(t, mustResolve(t, runIO, "60/scores-B-raw.jsonl")),
		"60/compliance-B-raw.jsonl": mustReadFile(t, mustResolve(t, runIO, "60/compliance-B-raw.jsonl")),
		"60/done.marker":            mustReadFile(t, mustResolve(t, runIO, "60/done.marker")),
	}
}

func mustResolve(t *testing.T, runIO internalio.RunContext, rel string) string {
	t.Helper()
	path, err := runIO.ResolveRunRelative(rel)
	require.NoError(t, err)
	return path
}

func mustReadJSON[T any](t *testing.T, path string) T {
	t.Helper()
	value, err := internalio.ReadJSON[T](path)
	require.NoError(t, err)
	return value
}

func mustReadJSONL[T any](t *testing.T, runIO internalio.RunContext, rel string) []T {
	t.Helper()
	rows, err := internalio.ReadJSONL[T](mustResolve(t, runIO, rel))
	require.NoError(t, err)
	return rows
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}

func writeStep60ReasonsSidecar(t *testing.T, runIO internalio.RunContext, content string) (contracts.OverflowRef, string) {
	t.Helper()
	reasonsDir := mustResolve(t, runIO, "60/reasons")
	sum := sha256Hex([]byte(content))
	sidecarPath, err := internalio.WriteSidecar(reasonsDir, sum, content)
	require.NoError(t, err)
	refPath, err := internalio.SidecarRefPath(runIO.RunDir(), sidecarPath)
	require.NoError(t, err)
	return contracts.OverflowRef{Path: refPath, Sha256: sum}, sidecarPath
}

func rewriteRawScores(t *testing.T, path string, rows []contracts.RawScoreEntry) {
	t.Helper()
	require.NoError(t, os.Remove(path))
	for _, row := range rows {
		require.NoError(t, internalio.AppendJSONL(path, row))
	}
}

func rewriteRawCompliance(t *testing.T, path string, rows []contracts.RawComplianceEntry) {
	t.Helper()
	require.NoError(t, os.Remove(path))
	for _, row := range rows {
		require.NoError(t, internalio.AppendJSONL(path, row))
	}
}

func writeEmptyRubric(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rubric.md")
	require.NoError(t, os.WriteFile(path, []byte("# rubric\n"), 0o644))
	return path
}

func writePass1Compliance(t *testing.T, runIO internalio.RunContext, runID contracts.RunID, agent contracts.AgentID, verdicts map[string]contracts.ComplianceVerdict) {
	t.Helper()
	path := mustResolve(t, runIO, "30/compliance-A.jsonl")
	ruleIDs := make([]string, 0, len(verdicts))
	for ruleID := range verdicts {
		ruleIDs = append(ruleIDs, ruleID)
	}
	sort.Strings(ruleIDs)
	for _, ruleID := range ruleIDs {
		require.NoError(t, internalio.AppendJSONL(path, contracts.ComplianceEntry{
			SchemaVersion: "1",
			RunID:         runID,
			Pass:          1,
			Agent:         agent,
			RuleID:        ruleID,
			Verdict:       verdicts[ruleID],
			Rationale:     fmt.Sprintf("pass1-%s-%s", agent, ruleID),
			VerdictPath:   contracts.VerdictPathAgreement,
			RubricVersion: "default",
			PromptVersion: "phase0-stub",
			ResolvedAt:    time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
		}))
	}
}

func mustHashFinalScores(t *testing.T, entries []contracts.ScoreEntry) string {
	t.Helper()
	hash, err := hashFinalScores(entries)
	require.NoError(t, err)
	return hash
}

func mustHashFinalCompliance(t *testing.T, entries []contracts.ComplianceEntry) string {
	t.Helper()
	hash, err := hashFinalCompliance(entries)
	require.NoError(t, err)
	return hash
}

func mustHashFinalPairwise(t *testing.T, entries []contracts.PairwiseEntry) string {
	t.Helper()
	hash, err := hashFinalPairwise(entries)
	require.NoError(t, err)
	return hash
}

func mustHashReducedRawScores(t *testing.T, runIO internalio.RunContext) string {
	t.Helper()
	hash, err := hashReducedRawScoresFile(runIO, mustResolve(t, runIO, "60/scores-B-raw.jsonl"))
	require.NoError(t, err)
	return hash
}

func mustHashReducedRawCompliance(t *testing.T, runIO internalio.RunContext) string {
	t.Helper()
	hash, err := hashReducedRawComplianceFile(runIO, mustResolve(t, runIO, "60/compliance-B-raw.jsonl"))
	require.NoError(t, err)
	return hash
}

func flipHexChar(value string) string {
	if value == "" {
		return value
	}
	if value[0] == '0' {
		return "1" + value[1:]
	}
	return "0" + value[1:]
}
