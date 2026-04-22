package step60_scorepairwise

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/contracts/stepio"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/judges"
	"github.com/nishimoto265/auto-improve/internal/steps/scorecore"
)

type Input struct {
	IO             internalio.RunContext
	TaskPackage    *contracts.TaskPackage
	ScorableAgents []contracts.AgentID
	RubricVersion  string
	PromptVersion  string
	RubricPath     string
	Primary        judges.Judge
	Secondary      judges.Judge
	Arbiter        judges.Judge
	Now            func() time.Time
}

var (
	ErrNoScorablePass2Agents     = errors.New("step60: no scorable pass2 agents found")
	ErrPass1ScoresIncomplete     = errors.New("step60: pass1 scores incomplete")
	ErrDuplicateComplianceRuleID = errors.New("step60: duplicate compliance rule_id")
)

var canonicalDimensions = []contracts.Dimension{
	contracts.DimensionFidelity,
	contracts.DimensionCorrectness,
	contracts.DimensionMaintainability,
	contracts.DimensionDiscipline,
	contracts.DimensionCommunication,
}

const defaultDisagreementThreshold = 5

type scoreKey struct {
	Agent     contracts.AgentID
	Dimension contracts.Dimension
}

type complianceKey struct {
	Agent  contracts.AgentID
	RuleID string
}

type rawScoreKey struct {
	Agent     contracts.AgentID
	JudgeRole contracts.JudgeRole
	Dimension contracts.Dimension
}

type rawComplianceKey struct {
	Agent     contracts.AgentID
	JudgeRole contracts.JudgeRole
	RuleID    string
}

type step60Paths struct {
	Lock            string
	Done            string
	ScoresRaw       string
	ScoresFinal     string
	ComplianceRaw   string
	ComplianceFinal string
	Pairwise        string
}

type scorableAgentRun struct {
	Agent      contracts.AgentID
	JudgeInput judges.JudgeInput
}

type finalMetadata struct {
	RunID         contracts.RunID
	Pass          int
	RubricVersion string
	PromptVersion string
	ResolvedAt    time.Time
}

// Run scores pass2 outputs, derives pass1-vs-pass2 pairwise rows, and writes
// the step60 done marker. It returns ErrNoScorablePass2Agents when no pass2
// manifests are scorable.
func Run(ctx context.Context, in Input) error {
	in, err := applyDefaults(in)
	if err != nil {
		return err
	}

	paths, err := resolveStep60Paths(in.IO)
	if err != nil {
		return err
	}
	lock, err := internalio.AcquireFileLock(paths.Lock)
	if err != nil {
		return fmt.Errorf("step60: acquire lock: %w", err)
	}
	defer func() {
		_ = lock.Unlock()
	}()
	scorableRuns, err := collectScorableAgentRuns(in, declaredScorableAgents(in), len(in.ScorableAgents) > 0)
	if err != nil {
		return err
	}
	expectedAgents := scorableAgentsFromRuns(scorableRuns)
	resetOutputs := false
	if _, err := os.Stat(paths.Done); err == nil {
		matches, err := doneMarkerMatchesCurrentState(in.IO, paths, expectedAgents)
		if err != nil {
			return err
		}
		versionsMatch, err := step60VersionsMatch(paths, in.RubricVersion, in.PromptVersion)
		if err != nil {
			return err
		}
		if matches && versionsMatch {
			return nil
		}
		if err := os.Remove(paths.Done); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("step60: remove stale done marker: %w", err)
		}
		resetOutputs = true
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("step60: stat done marker: %w", err)
	} else {
		resetOutputs = true
	}
	if resetOutputs {
		if err := resetStep60Outputs(paths); err != nil {
			return err
		}
	}
	rawState, err := loadStep60RawState(paths)
	if err != nil {
		return err
	}
	request := stepio.Step60Request{
		TaskPackage:    *in.TaskPackage,
		ScorableAgents: expectedAgents,
		RubricVersion:  in.RubricVersion,
		PromptVersion:  in.PromptVersion,
	}
	if err := request.Validate(); err != nil {
		return err
	}
	pass1ScoresByAgent, err := loadPass1Scores(in.IO)
	if err != nil {
		return err
	}
	pass1ComplianceRuleIDs, err := loadPass1ComplianceRuleIDs(in.IO)
	if err != nil {
		return err
	}
	fallbackComplianceRuleIDs, err := judges.ExpectedComplianceRuleIDs(in.RubricPath)
	if err != nil {
		return err
	}

	resolvedAt := in.Now().UTC()
	meta := finalMetadata{
		RunID:         in.TaskPackage.RunID,
		Pass:          2,
		RubricVersion: in.RubricVersion,
		PromptVersion: in.PromptVersion,
		ResolvedAt:    resolvedAt,
	}

	finalScores := make([]contracts.ScoreEntry, 0, len(scorableRuns)*len(canonicalDimensions))
	finalCompliance := make([]contracts.ComplianceEntry, 0, len(scorableRuns))
	pass2ScoresByAgent := make(map[contracts.AgentID][]contracts.ScoreEntry, len(scorableRuns))
	completedAgents := make([]contracts.AgentID, 0, len(scorableRuns))

	for _, run := range scorableRuns {
		outputHash, err := fileSHA256(run.JudgeInput.OutputPath)
		if err != nil {
			return fmt.Errorf("step60: hash pass2 output for agent=%s: %w", run.Agent, err)
		}
		expectedCompliance := expectedComplianceRuleIDsForAgent(run.Agent, pass1ComplianceRuleIDs, rawState.final[run.Agent], fallbackComplianceRuleIDs)
		if result, ok, err := tryReuseRawPanelResult(in.IO, rawState, run.Agent, outputHash, in.RubricVersion, in.PromptVersion, expectedCompliance); err != nil {
			return err
		} else if ok {
			agentScores, agentCompliance, err := appendPanelFinals(paths, meta, result)
			if err != nil {
				return err
			}
			completedAgents = append(completedAgents, run.Agent)
			finalScores = append(finalScores, agentScores...)
			finalCompliance = append(finalCompliance, agentCompliance...)
			pass2ScoresByAgent[run.Agent] = agentScores
			continue
		}

		primaryOutput, err := scoreJudgeOutput(ctx, "primary", in.Primary, run.JudgeInput)
		if err != nil {
			return err
		}
		secondaryOutput, err := scoreJudgeOutput(ctx, "secondary", in.Secondary, run.JudgeInput)
		if err != nil {
			return err
		}

		primaryScores, err := normalizeScores(in.IO, primaryOutput.Scores, in.RubricVersion, in.PromptVersion)
		if err != nil {
			return err
		}
		secondaryScores, err := normalizeScores(in.IO, secondaryOutput.Scores, in.RubricVersion, in.PromptVersion)
		if err != nil {
			return err
		}
		primaryCompliance, err := normalizeCompliance(in.IO, primaryOutput.Compliance, in.RubricVersion, in.PromptVersion)
		if err != nil {
			return err
		}
		secondaryCompliance, err := normalizeCompliance(in.IO, secondaryOutput.Compliance, in.RubricVersion, in.PromptVersion)
		if err != nil {
			return err
		}

		complianceDisagreements := complianceDisagreementRuleIDs(primaryCompliance, secondaryCompliance)
		primaryRawScores, primaryScoreRefs, err := buildRawScores(primaryScores, outputHash, contracts.JudgeRolePrimary, nil, nil, meta.ResolvedAt)
		if err != nil {
			return err
		}
		secondaryRawScores, secondaryScoreRefs, err := buildRawScores(secondaryScores, outputHash, contracts.JudgeRoleSecondary, nil, nil, meta.ResolvedAt)
		if err != nil {
			return err
		}
		scoreNeedsArbiter, err := scorecore.PanelDisagrees(primaryRawScores, secondaryRawScores, nil, nil, defaultDisagreementThreshold)
		if err != nil {
			return err
		}

		var arbiterScores map[contracts.Dimension]contracts.ScoreEntry
		var arbiterCompliance map[string]contracts.ComplianceEntry
		if scoreNeedsArbiter || len(complianceDisagreements) > 0 {
			arbiterOutput, err := scoreJudgeOutput(ctx, "arbiter", in.Arbiter, run.JudgeInput)
			if err != nil {
				return err
			}
			arbiterScores, err = normalizeScores(in.IO, arbiterOutput.Scores, in.RubricVersion, in.PromptVersion)
			if err != nil {
				return err
			}
			arbiterCompliance, err = normalizeCompliance(in.IO, arbiterOutput.Compliance, in.RubricVersion, in.PromptVersion)
			if err != nil {
				return err
			}
		}

		agentScores, err := emitScores(paths, meta, run.Agent, outputHash, primaryScores, secondaryScores, arbiterScores, primaryRawScores, secondaryRawScores, primaryScoreRefs, secondaryScoreRefs)
		if err != nil {
			return err
		}
		agentCompliance, err := emitCompliance(paths, meta, run.Agent, outputHash, primaryCompliance, secondaryCompliance, arbiterCompliance)
		if err != nil {
			return err
		}

		if len(agentScores) != len(canonicalDimensions) {
			return fmt.Errorf("step60: incomplete score set for agent=%s: got=%d want=%d", run.Agent, len(agentScores), len(canonicalDimensions))
		}

		completedAgents = append(completedAgents, run.Agent)
		finalScores = append(finalScores, agentScores...)
		finalCompliance = append(finalCompliance, agentCompliance...)
		pass2ScoresByAgent[run.Agent] = agentScores
	}

	pairwiseEntries := make([]contracts.PairwiseEntry, 0, len(completedAgents))
	for _, agent := range completedAgents {
		pass1AverageTenths, err := resolvePass1AverageTenths(in.IO, agent, pass1ScoresByAgent[agent])
		if err != nil {
			return err
		}
		pass2AverageTenths, err := averageScoresTenths(pass2ScoresByAgent[agent])
		if err != nil {
			return err
		}
		entry := makePairwiseEntry(in, agent, pass1AverageTenths, pass2AverageTenths, resolvedAt)
		if err := appendJSONLWithParentDirSync(paths.Pairwise, entry); err != nil {
			return fmt.Errorf("step60: append pairwise row for agent=%s: %w", agent, err)
		}
		pairwiseEntries = append(pairwiseEntries, entry)
	}

	if len(finalScores) != len(completedAgents)*len(canonicalDimensions) {
		return fmt.Errorf("step60: final score cardinality mismatch: scores=%d completed_agents=%d", len(finalScores), len(completedAgents))
	}
	if len(pairwiseEntries) != len(completedAgents) {
		return fmt.Errorf("step60: final pairwise cardinality mismatch: pairwise=%d completed_agents=%d", len(pairwiseEntries), len(completedAgents))
	}

	scoresFinalHash, err := hashFinalScores(finalScores)
	if err != nil {
		return fmt.Errorf("step60: hash scores final: %w", err)
	}
	complianceFinalHash, err := hashFinalCompliance(finalCompliance)
	if err != nil {
		return fmt.Errorf("step60: hash compliance final: %w", err)
	}
	pairwiseFinalHash, err := hashFinalPairwise(pairwiseEntries)
	if err != nil {
		return fmt.Errorf("step60: hash pairwise final: %w", err)
	}
	scoresRawHash, err := hashReducedRawScoresFile(in.IO, paths.ScoresRaw)
	if err != nil {
		return fmt.Errorf("step60: hash scores raw: %w", err)
	}
	complianceRawHash, err := hashReducedRawComplianceFile(in.IO, paths.ComplianceRaw)
	if err != nil {
		return fmt.Errorf("step60: hash compliance raw: %w", err)
	}

	marker := contracts.Step60DoneMarker{
		CompletedAgents: completedAgents,
		Dimensions:      append([]contracts.Dimension(nil), canonicalDimensions...),
		ExpectedCounts: contracts.Step60ExpectedCounts{
			Scores:     int64(len(completedAgents) * len(canonicalDimensions)),
			Compliance: int64(len(finalCompliance)),
			Pairwise:   int64(len(pairwiseEntries)),
		},
		ContentHashes: contracts.Step60DoneContentHashes{
			ScoresFinal:     scoresFinalHash,
			ComplianceFinal: complianceFinalHash,
			PairwiseFinal:   pairwiseFinalHash,
		},
		RawHashes: contracts.StepDoneRawHashes{
			ScoresRaw:     scoresRawHash,
			ComplianceRaw: complianceRawHash,
		},
		ResolvedAt: resolvedAt,
	}
	if err := marker.Validate(); err != nil {
		return err
	}
	if err := internalio.WriteJSONAtomic(paths.Done, marker); err != nil {
		return fmt.Errorf("step60: write done marker: %w", err)
	}
	return nil
}

func applyDefaults(in Input) (Input, error) {
	if in.TaskPackage == nil {
		return Input{}, errors.New("step60: task package is required")
	}
	if err := in.TaskPackage.Validate(); err != nil {
		return Input{}, err
	}
	if in.TaskPackage.RunID != in.IO.RunID {
		return Input{}, fmt.Errorf("step60: task package run_id mismatch: task_package=%s io=%s", in.TaskPackage.RunID, in.IO.RunID)
	}
	if in.Now == nil {
		in.Now = time.Now
	}
	if in.RubricVersion == "" {
		in.RubricVersion = "default"
	}
	if in.PromptVersion == "" {
		in.PromptVersion = "phase0-stub"
	}
	if in.RubricPath == "" {
		rubricPath, err := judges.DefaultRubricPath()
		if err != nil {
			return Input{}, err
		}
		in.RubricPath = rubricPath
	}
	if err := contracts.EnsureCleanAbsolutePath(in.RubricPath); err != nil {
		return Input{}, err
	}
	if in.Primary == nil {
		in.Primary = judges.NewPrimaryStub()
	}
	if in.Secondary == nil {
		in.Secondary = judges.NewSecondaryStub()
	}
	if in.Arbiter == nil {
		in.Arbiter = judges.NewArbiterStub()
	}
	return in, nil
}

func resolveStep60Paths(runIO internalio.RunContext) (step60Paths, error) {
	lockPath, err := runIO.ResolveRunRelative("60/.step60.lock")
	if err != nil {
		return step60Paths{}, fmt.Errorf("step60: resolve lock path: %w", err)
	}
	donePath, err := runIO.ResolveRunRelative("60/done.marker")
	if err != nil {
		return step60Paths{}, fmt.Errorf("step60: resolve done marker path: %w", err)
	}
	scoresRawPath, err := runIO.ResolveRunRelative("60/scores-B-raw.jsonl")
	if err != nil {
		return step60Paths{}, fmt.Errorf("step60: resolve scores raw path: %w", err)
	}
	scoresFinalPath, err := runIO.ResolveRunRelative("60/scores-B.jsonl")
	if err != nil {
		return step60Paths{}, fmt.Errorf("step60: resolve scores final path: %w", err)
	}
	complianceRawPath, err := runIO.ResolveRunRelative("60/compliance-B-raw.jsonl")
	if err != nil {
		return step60Paths{}, fmt.Errorf("step60: resolve compliance raw path: %w", err)
	}
	complianceFinalPath, err := runIO.ResolveRunRelative("60/compliance-B.jsonl")
	if err != nil {
		return step60Paths{}, fmt.Errorf("step60: resolve compliance final path: %w", err)
	}
	pairwisePath, err := runIO.ResolveRunRelative("60/pairwise.jsonl")
	if err != nil {
		return step60Paths{}, fmt.Errorf("step60: resolve pairwise path: %w", err)
	}
	return step60Paths{
		Lock:            lockPath,
		Done:            donePath,
		ScoresRaw:       scoresRawPath,
		ScoresFinal:     scoresFinalPath,
		ComplianceRaw:   complianceRawPath,
		ComplianceFinal: complianceFinalPath,
		Pairwise:        pairwisePath,
	}, nil
}

func declaredScorableAgents(in Input) []contracts.AgentID {
	if len(in.ScorableAgents) > 0 {
		agents := append([]contracts.AgentID(nil), in.ScorableAgents...)
		sort.Slice(agents, func(i, j int) bool { return agents[i] < agents[j] })
		return agents
	}

	agents := make([]contracts.AgentID, 0, len(in.TaskPackage.Worktrees)/2)
	for _, worktree := range in.TaskPackage.Worktrees {
		if worktree.Pass == 2 {
			agents = append(agents, worktree.Agent)
		}
	}
	sort.Slice(agents, func(i, j int) bool { return agents[i] < agents[j] })
	return agents
}

func scorableAgentsFromRuns(runs []scorableAgentRun) []contracts.AgentID {
	agents := make([]contracts.AgentID, 0, len(runs))
	for _, run := range runs {
		agents = append(agents, run.Agent)
	}
	return agents
}

func shouldSkipAgent(err error) bool {
	return errors.Is(err, internalio.ErrNotScorable)
}

func collectScorableAgentRuns(in Input, agents []contracts.AgentID, explicit bool) ([]scorableAgentRun, error) {
	runs := make([]scorableAgentRun, 0, len(agents))
	for _, agent := range agents {
		if _, err := internalio.LoadScorableManifest(in.IO, 1, agent); err != nil {
			if explicit && (shouldSkipAgent(err) || os.IsNotExist(err)) {
				return nil, fmt.Errorf("step60: declared scorable agent missing pass1 scorable manifest: agent=%s: %w", agent, err)
			}
			if shouldSkipAgent(err) || os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("step60: load pass1 scorable manifest for agent=%s: %w", agent, err)
		}
		manifest, err := internalio.LoadScorableManifest(in.IO, 2, agent)
		if explicit && shouldSkipAgent(err) {
			return nil, fmt.Errorf("step60: declared scorable agent missing pass2 scorable manifest: agent=%s: %w", agent, err)
		}
		if shouldSkipAgent(err) {
			continue
		}
		if err != nil {
			if explicit && os.IsNotExist(err) {
				return nil, fmt.Errorf("step60: declared scorable agent missing pass2 manifest: agent=%s: %w", agent, err)
			}
			return nil, fmt.Errorf("step60: load pass2 manifest for agent=%s: %w", agent, err)
		}
		outputPath, err := requireExistingManifestArtifact(in.IO, agent, manifest.DiffPath, "diff")
		if err != nil {
			return nil, err
		}
		if _, err := requireExistingManifestArtifact(in.IO, agent, manifest.SessionPath, "session"); err != nil {
			return nil, err
		}
		if _, err := requireExistingManifestArtifact(in.IO, agent, manifest.ChecklistPath, "checklist"); err != nil {
			return nil, err
		}
		runs = append(runs, scorableAgentRun{
			Agent: agent,
			JudgeInput: judges.JudgeInput{
				RunID:      in.TaskPackage.RunID,
				Pass:       2,
				Agent:      agent,
				OutputPath: outputPath,
				RubricPath: in.RubricPath,
			},
		})
	}
	if len(runs) == 0 {
		return nil, ErrNoScorablePass2Agents
	}
	return runs, nil
}

func requireExistingManifestArtifact(runIO internalio.RunContext, agent contracts.AgentID, relativePath, label string) (string, error) {
	resolvedPath, ok, err := resolveExistingManifestArtifact(runIO, relativePath)
	if err != nil {
		return "", fmt.Errorf("step60: resolve pass2 %s path for agent=%s: %w", label, agent, err)
	}
	if !ok {
		return "", fmt.Errorf("step60: missing declared pass2 %s artifact for agent=%s: %s", label, agent, relativePath)
	}
	return resolvedPath, nil
}

func resolveExistingManifestArtifact(runIO internalio.RunContext, relativePath string) (string, bool, error) {
	resolvedPath, err := runIO.ResolveRunRelative(relativePath)
	if err != nil {
		return "", false, err
	}
	if _, err := os.Stat(resolvedPath); err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	return resolvedPath, true, nil
}

func scoreJudgeOutput(ctx context.Context, label string, judge judges.Judge, input judges.JudgeInput) (judges.JudgeOutput, error) {
	if err := ctx.Err(); err != nil {
		return judges.JudgeOutput{}, fmt.Errorf("step60: %s judge score output for agent=%s: %w", label, input.Agent, err)
	}
	output, err := judge.ScoreOutput(ctx, input)
	if err != nil {
		return judges.JudgeOutput{}, fmt.Errorf("step60: %s judge score output for agent=%s: %w", label, input.Agent, err)
	}
	if err := output.ValidateFor(input); err != nil {
		return judges.JudgeOutput{}, fmt.Errorf("step60: validate %s judge output for agent=%s: %w", label, input.Agent, err)
	}
	return output, nil
}

func normalizeScores(runIO internalio.RunContext, scores []contracts.ScoreEntry, rubricVersion, promptVersion string) (map[contracts.Dimension]contracts.ScoreEntry, error) {
	out := make(map[contracts.Dimension]contracts.ScoreEntry, len(scores))
	for _, score := range scores {
		score.RubricVersion = rubricVersion
		score.PromptVersion = promptVersion
		var err error
		score, err = canonicalizeScoreOverflow(runIO, score)
		if err != nil {
			return nil, err
		}
		out[score.Dimension] = score
	}
	return out, nil
}

func normalizeCompliance(runIO internalio.RunContext, entries []contracts.ComplianceEntry, rubricVersion, promptVersion string) (map[string]contracts.ComplianceEntry, error) {
	out := make(map[string]contracts.ComplianceEntry, len(entries))
	for _, entry := range entries {
		entry.RubricVersion = rubricVersion
		entry.PromptVersion = promptVersion
		var err error
		entry, err = canonicalizeComplianceOverflow(runIO, entry)
		if err != nil {
			return nil, err
		}
		if _, exists := out[entry.RuleID]; exists {
			return nil, fmt.Errorf("%w: rule_id=%s", ErrDuplicateComplianceRuleID, entry.RuleID)
		}
		out[entry.RuleID] = entry
	}
	return out, nil
}

func canonicalizeScoreOverflow(runIO internalio.RunContext, score contracts.ScoreEntry) (contracts.ScoreEntry, error) {
	score.ReasonsOverflowRef = nil
	if len([]rune(score.Reasons)) <= scorecore.ReasonsMaxChars {
		return score, nil
	}
	ref, err := scorecore.WriteOverflowSidecar(runIO, "60", score.Reasons)
	if err != nil {
		return contracts.ScoreEntry{}, err
	}
	score.Reasons = ""
	score.ReasonsOverflowRef = &ref
	return score, nil
}

func canonicalizeComplianceOverflow(runIO internalio.RunContext, entry contracts.ComplianceEntry) (contracts.ComplianceEntry, error) {
	entry.RationaleOverflowRef = nil
	if len([]rune(entry.Rationale)) <= scorecore.RationaleMaxChars {
		return entry, nil
	}
	ref, err := scorecore.WriteOverflowSidecar(runIO, "60", entry.Rationale)
	if err != nil {
		return contracts.ComplianceEntry{}, err
	}
	entry.Rationale = ""
	entry.RationaleOverflowRef = &ref
	return entry, nil
}

func complianceDisagreementRuleIDs(primary, secondary map[string]contracts.ComplianceEntry) []string {
	disagreements := make([]string, 0, minInt(len(primary), len(secondary)))
	for ruleID, primaryEntry := range primary {
		secondaryEntry, ok := secondary[ruleID]
		if ok && primaryEntry.Verdict != secondaryEntry.Verdict {
			disagreements = append(disagreements, ruleID)
		}
	}
	sort.Strings(disagreements)
	return disagreements
}

func complianceRuleSetsMatch(primary, secondary map[string]contracts.ComplianceEntry) bool {
	if len(primary) != len(secondary) {
		return false
	}
	for ruleID := range primary {
		if _, ok := secondary[ruleID]; !ok {
			return false
		}
	}
	return true
}

func emitScores(
	paths step60Paths,
	meta finalMetadata,
	agent contracts.AgentID,
	outputHash string,
	primary map[contracts.Dimension]contracts.ScoreEntry,
	secondary map[contracts.Dimension]contracts.ScoreEntry,
	arbiter map[contracts.Dimension]contracts.ScoreEntry,
	primaryRaw []contracts.RawScoreEntry,
	secondaryRaw []contracts.RawScoreEntry,
	primaryRefs map[contracts.Dimension]*contracts.RawJudgeRef,
	secondaryRefs map[contracts.Dimension]*contracts.RawJudgeRef,
) ([]contracts.ScoreEntry, error) {
	arbiterRaw := make([]contracts.RawScoreEntry, 0, len(canonicalDimensions))
	if len(arbiter) > 0 {
		var err error
		arbiterRaw, _, err = buildRawScores(arbiter, outputHash, contracts.JudgeRoleArbiter, primaryRefs, secondaryRefs, meta.ResolvedAt)
		if err != nil {
			return nil, err
		}
	}
	result, err := scorecore.BuildFinalResultFromRaw(
		primaryRaw,
		secondaryRaw,
		arbiterRaw,
		nil,
		nil,
		nil,
		defaultDisagreementThreshold,
		true,
		len(arbiterRaw) > 0,
	)
	if err != nil {
		return nil, err
	}
	rawRows := make([]contracts.RawScoreEntry, 0, len(primaryRaw)+len(secondaryRaw)+len(arbiterRaw))
	rawRows = append(rawRows, primaryRaw...)
	rawRows = append(rawRows, secondaryRaw...)
	rawRows = append(rawRows, arbiterRaw...)
	for _, row := range rawRows {
		if err := appendJSONLWithParentDirSync(paths.ScoresRaw, row); err != nil {
			return nil, fmt.Errorf("step60: append raw score agent=%s: %w", agent, err)
		}
	}
	finalScores := make([]contracts.ScoreEntry, 0, len(result.FinalScores))
	for _, row := range result.FinalScores {
		finalScore := finalizeScore(meta, row, row.VerdictPath)
		if err := appendJSONLWithParentDirSync(paths.ScoresFinal, finalScore); err != nil {
			return nil, fmt.Errorf("step60: append final score agent=%s: %w", agent, err)
		}
		finalScores = append(finalScores, finalScore)
	}
	return finalScores, nil
}

func emitCompliance(
	paths step60Paths,
	meta finalMetadata,
	agent contracts.AgentID,
	outputHash string,
	primary map[string]contracts.ComplianceEntry,
	secondary map[string]contracts.ComplianceEntry,
	arbiter map[string]contracts.ComplianceEntry,
) ([]contracts.ComplianceEntry, error) {
	if !complianceRuleSetsMatch(primary, secondary) {
		return nil, fmt.Errorf("step60: compliance rule-set mismatch agent=%s", agent)
	}
	if err := scorecore.ValidateArbiterComplianceRuleCoverage(
		disputedComplianceRuleIDs(primary, secondary),
		disputedComplianceRuleIDs(primary, secondary),
		sortedComplianceRuleIDs(arbiter),
	); err != nil && len(arbiter) > 0 {
		return nil, fmt.Errorf("step60: agent=%s: %w", agent, err)
	}
	ruleIDs := complianceRuleIDs(primary, secondary, arbiter)
	finalEntries := make([]contracts.ComplianceEntry, 0, len(ruleIDs))
	for _, ruleID := range ruleIDs {
		primaryEntry, primaryOK := primary[ruleID]
		secondaryEntry, secondaryOK := secondary[ruleID]
		arbiterEntry, arbiterOK := arbiter[ruleID]

		var primaryHash string
		if primaryOK {
			rawPrimary := makeRawComplianceEntry(primaryEntry, contracts.JudgeRolePrimary, outputHash, nil, nil, meta.ResolvedAt)
			var err error
			primaryHash, err = rawComplianceEntryHash(rawPrimary)
			if err != nil {
				return nil, fmt.Errorf("step60: hash primary compliance rule=%s agent=%s: %w", ruleID, agent, err)
			}
			if err := appendJSONLWithParentDirSync(paths.ComplianceRaw, rawPrimary); err != nil {
				return nil, fmt.Errorf("step60: append primary raw compliance rule=%s agent=%s: %w", ruleID, agent, err)
			}
		}
		var secondaryHash string
		if secondaryOK {
			rawSecondary := makeRawComplianceEntry(secondaryEntry, contracts.JudgeRoleSecondary, outputHash, nil, nil, meta.ResolvedAt)
			var err error
			secondaryHash, err = rawComplianceEntryHash(rawSecondary)
			if err != nil {
				return nil, fmt.Errorf("step60: hash secondary compliance rule=%s agent=%s: %w", ruleID, agent, err)
			}
			if err := appendJSONLWithParentDirSync(paths.ComplianceRaw, rawSecondary); err != nil {
				return nil, fmt.Errorf("step60: append secondary raw compliance rule=%s agent=%s: %w", ruleID, agent, err)
			}
		}

		primaryDecision := complianceEntryOrMissed(meta, agent, ruleID, primaryEntry, primaryOK)
		secondaryDecision := complianceEntryOrMissed(meta, agent, ruleID, secondaryEntry, secondaryOK)

		var finalEntry contracts.ComplianceEntry
		switch {
		case primaryDecision.Verdict == secondaryDecision.Verdict:
			finalEntry = finalizeCompliance(meta, preferredComplianceAgreementSource(primaryDecision, secondaryDecision, primaryOK, secondaryOK), contracts.VerdictPathAgreement)
		case !primaryOK || !secondaryOK:
			// Single-side rules finalize directly from the observed side so the final verdict
			// remains fully traceable from compliance-B-raw.jsonl without synthetic arbiter input.
			finalEntry = finalizeCompliance(meta, preferredComplianceSingleSource(primaryDecision, secondaryDecision, primaryOK, secondaryOK), contracts.VerdictPathSingle)
		default:
			if !arbiterOK {
				return nil, fmt.Errorf("step60: arbiter compliance missing rule=%s agent=%s", ruleID, agent)
			}
			if err := appendJSONLWithParentDirSync(paths.ComplianceRaw, makeRawComplianceEntry(
				arbiterEntry,
				contracts.JudgeRoleArbiter,
				outputHash,
				&contracts.RawJudgeRef{Role: contracts.JudgeRolePrimary, Sha256: primaryHash},
				&contracts.RawJudgeRef{Role: contracts.JudgeRoleSecondary, Sha256: secondaryHash},
				meta.ResolvedAt,
			)); err != nil {
				return nil, fmt.Errorf("step60: append arbiter raw compliance rule=%s agent=%s: %w", ruleID, agent, err)
			}
			finalEntry = finalizeCompliance(meta, arbiterEntry, complianceVerdictPath(primaryDecision, secondaryDecision, arbiterEntry))
		}

		if err := appendJSONLWithParentDirSync(paths.ComplianceFinal, finalEntry); err != nil {
			return nil, fmt.Errorf("step60: append final compliance rule=%s agent=%s: %w", ruleID, agent, err)
		}
		finalEntries = append(finalEntries, finalEntry)
	}
	return finalEntries, nil
}

func doneMarkerMatchesCurrentState(runIO internalio.RunContext, paths step60Paths, expectedAgents []contracts.AgentID) (bool, error) {
	marker, err := internalio.ReadJSON[contracts.Step60DoneMarker](paths.Done)
	if err != nil {
		return false, nil
	}
	if err := marker.Validate(); err != nil {
		return false, nil
	}
	if !slices.Equal(marker.Dimensions, canonicalDimensions) {
		return false, nil
	}
	normalizedExpectedAgents := append([]contracts.AgentID(nil), expectedAgents...)
	sort.Slice(normalizedExpectedAgents, func(i, j int) bool { return normalizedExpectedAgents[i] < normalizedExpectedAgents[j] })
	if !slices.Equal(marker.CompletedAgents, normalizedExpectedAgents) {
		return false, nil
	}

	scoresFinalCount, scoresFinalHash, err := currentFinalScoresState(runIO, paths.ScoresFinal)
	if err != nil {
		if overflowValidationFailure(err) {
			return false, nil
		}
		return false, fmt.Errorf("step60: inspect scores final: %w", err)
	}
	complianceFinalCount, complianceFinalHash, err := currentFinalComplianceState(runIO, paths.ComplianceFinal)
	if err != nil {
		if overflowValidationFailure(err) {
			return false, nil
		}
		return false, fmt.Errorf("step60: inspect compliance final: %w", err)
	}
	pairwiseCount, pairwiseHash, err := currentPairwiseState(paths.Pairwise)
	if err != nil {
		return false, fmt.Errorf("step60: inspect pairwise final: %w", err)
	}
	scoresRawHash, err := hashReducedRawScoresFile(runIO, paths.ScoresRaw)
	if err != nil {
		if overflowValidationFailure(err) {
			return false, nil
		}
		return false, fmt.Errorf("step60: hash scores raw: %w", err)
	}
	complianceRawHash, err := hashReducedRawComplianceFile(runIO, paths.ComplianceRaw)
	if err != nil {
		if overflowValidationFailure(err) {
			return false, nil
		}
		return false, fmt.Errorf("step60: hash compliance raw: %w", err)
	}

	return marker.ExpectedCounts.Scores == int64(scoresFinalCount) &&
		marker.ExpectedCounts.Compliance == int64(complianceFinalCount) &&
		marker.ExpectedCounts.Pairwise == int64(pairwiseCount) &&
		marker.ContentHashes.ScoresFinal == scoresFinalHash &&
		marker.ContentHashes.ComplianceFinal == complianceFinalHash &&
		marker.ContentHashes.PairwiseFinal == pairwiseHash &&
		marker.RawHashes.ScoresRaw == scoresRawHash &&
		marker.RawHashes.ComplianceRaw == complianceRawHash, nil
}

func overflowValidationFailure(err error) bool {
	return errors.Is(err, internalio.ErrSidecarDigestMismatch) || os.IsNotExist(err)
}

func currentFinalScoresState(runIO internalio.RunContext, path string) (int, string, error) {
	rows, err := internalio.ReadJSONL[contracts.ScoreEntry](path)
	if err != nil {
		return 0, "", err
	}
	collapsed := internalio.CollapseByKey(rows, func(entry contracts.ScoreEntry) scoreKey {
		return scoreKey{Agent: entry.Agent, Dimension: entry.Dimension}
	})
	if err := validateScoreOverflowRefs(runIO, collapsed); err != nil {
		return 0, "", err
	}
	hash, err := hashFinalScores(collapsed)
	if err != nil {
		return 0, "", err
	}
	return len(collapsed), hash, nil
}

func currentFinalComplianceState(runIO internalio.RunContext, path string) (int, string, error) {
	rows, err := internalio.ReadJSONL[contracts.ComplianceEntry](path)
	if err != nil {
		return 0, "", err
	}
	collapsed := internalio.CollapseByKey(rows, func(entry contracts.ComplianceEntry) complianceKey {
		return complianceKey{Agent: entry.Agent, RuleID: entry.RuleID}
	})
	if err := validateComplianceOverflowRefs(runIO, collapsed); err != nil {
		return 0, "", err
	}
	hash, err := hashFinalCompliance(collapsed)
	if err != nil {
		return 0, "", err
	}
	return len(collapsed), hash, nil
}

func currentPairwiseState(path string) (int, string, error) {
	rows, err := internalio.ReadJSONL[contracts.PairwiseEntry](path)
	if err != nil {
		return 0, "", err
	}
	hash, err := hashFinalPairwise(rows)
	if err != nil {
		return 0, "", err
	}
	collapsed := internalio.CollapseByKey(rows, func(entry contracts.PairwiseEntry) complianceKey {
		return complianceKey{Agent: entry.AgentA, RuleID: string(entry.AgentB)}
	})
	return len(collapsed), hash, nil
}

func resetStep60Outputs(paths step60Paths) error {
	for _, path := range []string{
		paths.ScoresFinal,
		paths.ComplianceFinal,
		paths.Pairwise,
	} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("step60: reset output %s: %w", path, err)
		}
	}
	return nil
}

type step60RawState struct {
	scores     map[contracts.AgentID]map[contracts.JudgeRole]map[contracts.Dimension]contracts.RawScoreEntry
	compliance map[contracts.AgentID]map[contracts.JudgeRole]map[string]contracts.RawComplianceEntry
	final      map[contracts.AgentID]map[string]contracts.ComplianceEntry
}

func loadStep60RawState(paths step60Paths) (step60RawState, error) {
	scoreRows, err := readJSONLOrEmpty[contracts.RawScoreEntry](paths.ScoresRaw)
	if err != nil {
		return step60RawState{}, err
	}
	complianceRows, err := readJSONLOrEmpty[contracts.RawComplianceEntry](paths.ComplianceRaw)
	if err != nil {
		return step60RawState{}, err
	}
	finalComplianceRows, err := readJSONLOrEmpty[contracts.ComplianceEntry](paths.ComplianceFinal)
	if err != nil {
		return step60RawState{}, err
	}
	state := step60RawState{
		scores:     map[contracts.AgentID]map[contracts.JudgeRole]map[contracts.Dimension]contracts.RawScoreEntry{},
		compliance: map[contracts.AgentID]map[contracts.JudgeRole]map[string]contracts.RawComplianceEntry{},
		final:      map[contracts.AgentID]map[string]contracts.ComplianceEntry{},
	}
	for _, row := range reduceRawScores(scoreRows) {
		state.ensureAgent(row.Agent)
		state.scores[row.Agent][row.JudgeRole][row.Dimension] = row
	}
	for _, row := range reduceRawCompliance(complianceRows) {
		state.ensureAgent(row.Agent)
		state.compliance[row.Agent][row.JudgeRole][row.RuleID] = row
	}
	for _, row := range internalio.CollapseByKey(finalComplianceRows, func(entry contracts.ComplianceEntry) complianceKey {
		return complianceKey{Agent: entry.Agent, RuleID: entry.RuleID}
	}) {
		state.ensureAgent(row.Agent)
		state.final[row.Agent][row.RuleID] = row
	}
	return state, nil
}

func (s *step60RawState) ensureAgent(agent contracts.AgentID) {
	if _, ok := s.scores[agent]; !ok {
		s.scores[agent] = map[contracts.JudgeRole]map[contracts.Dimension]contracts.RawScoreEntry{
			contracts.JudgeRolePrimary:   {},
			contracts.JudgeRoleSecondary: {},
			contracts.JudgeRoleArbiter:   {},
		}
	}
	if _, ok := s.compliance[agent]; !ok {
		s.compliance[agent] = map[contracts.JudgeRole]map[string]contracts.RawComplianceEntry{
			contracts.JudgeRolePrimary:   {},
			contracts.JudgeRoleSecondary: {},
			contracts.JudgeRoleArbiter:   {},
		}
	}
	if _, ok := s.final[agent]; !ok {
		s.final[agent] = map[string]contracts.ComplianceEntry{}
	}
}

func (s step60RawState) scoreRows(agent contracts.AgentID, role contracts.JudgeRole) []contracts.RawScoreEntry {
	roleRows := s.scores[agent][role]
	out := make([]contracts.RawScoreEntry, 0, len(roleRows))
	for _, dimension := range canonicalDimensions {
		if row, ok := roleRows[dimension]; ok {
			out = append(out, row)
		}
	}
	return out
}

func (s step60RawState) complianceRows(agent contracts.AgentID, role contracts.JudgeRole) []contracts.RawComplianceEntry {
	roleRows := s.compliance[agent][role]
	ruleIDs := make([]string, 0, len(roleRows))
	for ruleID := range roleRows {
		ruleIDs = append(ruleIDs, ruleID)
	}
	sort.Strings(ruleIDs)
	out := make([]contracts.RawComplianceEntry, 0, len(ruleIDs))
	for _, ruleID := range ruleIDs {
		out = append(out, roleRows[ruleID])
	}
	return out
}

func tryReuseRawPanelResult(
	runIO internalio.RunContext,
	state step60RawState,
	agent contracts.AgentID,
	outputHash, rubricVersion, promptVersion string,
	expectedCompliance map[string]struct{},
) (scorecore.PanelResult, bool, error) {
	primaryScores := state.scoreRows(agent, contracts.JudgeRolePrimary)
	secondaryScores := state.scoreRows(agent, contracts.JudgeRoleSecondary)
	primaryCompliance := state.complianceRows(agent, contracts.JudgeRolePrimary)
	secondaryCompliance := state.complianceRows(agent, contracts.JudgeRoleSecondary)
	if !rawRoleUsable(primaryScores, outputHash, rubricVersion, promptVersion) || !rawRoleUsable(secondaryScores, outputHash, rubricVersion, promptVersion) {
		return scorecore.PanelResult{}, false, nil
	}
	arbiterScores := state.scoreRows(agent, contracts.JudgeRoleArbiter)
	arbiterCompliance := state.complianceRows(agent, contracts.JudgeRoleArbiter)
	if len(expectedCompliance) == 0 && (len(primaryCompliance) > 0 || len(secondaryCompliance) > 0 || len(arbiterCompliance) > 0) {
		return scorecore.PanelResult{}, false, nil
	}
	if !rawComplianceUsable(primaryCompliance, expectedCompliance, outputHash, rubricVersion, promptVersion) || !rawComplianceUsable(secondaryCompliance, expectedCompliance, outputHash, rubricVersion, promptVersion) {
		return scorecore.PanelResult{}, false, nil
	}
	if err := validateRawScoreOverflowRefs(runIO, primaryScores); err != nil {
		return scorecore.PanelResult{}, false, nil
	}
	if err := validateRawScoreOverflowRefs(runIO, secondaryScores); err != nil {
		return scorecore.PanelResult{}, false, nil
	}
	if err := validateRawComplianceOverflowRefs(runIO, primaryCompliance); err != nil {
		return scorecore.PanelResult{}, false, nil
	}
	if err := validateRawComplianceOverflowRefs(runIO, secondaryCompliance); err != nil {
		return scorecore.PanelResult{}, false, nil
	}
	if !rawRoleUsable(arbiterScores, outputHash, rubricVersion, promptVersion) {
		arbiterScores = nil
	}
	if !rawArbiterComplianceUsable(arbiterCompliance, outputHash, rubricVersion, promptVersion) {
		arbiterCompliance = nil
	}
	if err := validateRawScoreOverflowRefs(runIO, arbiterScores); err != nil {
		arbiterScores = nil
	}
	if err := validateRawComplianceOverflowRefs(runIO, arbiterCompliance); err != nil {
		arbiterCompliance = nil
	}
	result, err := scorecore.BuildFinalResultFromRaw(
		primaryScores,
		secondaryScores,
		arbiterScores,
		primaryCompliance,
		secondaryCompliance,
		arbiterCompliance,
		defaultDisagreementThreshold,
		true,
		len(arbiterScores) > 0 || len(arbiterCompliance) > 0,
	)
	if err != nil {
		return scorecore.PanelResult{}, false, nil
	}
	return result, true, nil
}

func appendPanelFinals(paths step60Paths, meta finalMetadata, result scorecore.PanelResult) ([]contracts.ScoreEntry, []contracts.ComplianceEntry, error) {
	finalScores := make([]contracts.ScoreEntry, 0, len(result.FinalScores))
	finalCompliance := make([]contracts.ComplianceEntry, 0, len(result.FinalCompliance))
	for _, row := range result.FinalScores {
		finalScore := finalizeScore(meta, row, row.VerdictPath)
		if err := appendJSONLWithParentDirSync(paths.ScoresFinal, finalScore); err != nil {
			return nil, nil, err
		}
		finalScores = append(finalScores, finalScore)
	}
	for _, row := range result.FinalCompliance {
		finalEntry := finalizeCompliance(meta, row, row.VerdictPath)
		if err := appendJSONLWithParentDirSync(paths.ComplianceFinal, finalEntry); err != nil {
			return nil, nil, err
		}
		finalCompliance = append(finalCompliance, finalEntry)
	}
	return finalScores, finalCompliance, nil
}

func rawRoleUsable(rows []contracts.RawScoreEntry, outputHash, rubricVersion, promptVersion string) bool {
	if len(rows) != len(canonicalDimensions) {
		return false
	}
	for _, row := range rows {
		if row.OutputSha256 != outputHash || row.RubricVersion != rubricVersion || row.PromptVersion != promptVersion {
			return false
		}
	}
	return true
}

func rawComplianceUsable(rows []contracts.RawComplianceEntry, expected map[string]struct{}, outputHash, rubricVersion, promptVersion string) bool {
	if len(expected) > 0 && len(rows) != len(expected) {
		return false
	}
	seen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		if row.OutputSha256 != outputHash || row.RubricVersion != rubricVersion || row.PromptVersion != promptVersion {
			return false
		}
		seen[row.RuleID] = struct{}{}
	}
	for ruleID := range expected {
		if _, ok := seen[ruleID]; !ok {
			return false
		}
	}
	return true
}

func rawArbiterComplianceUsable(rows []contracts.RawComplianceEntry, outputHash, rubricVersion, promptVersion string) bool {
	for _, row := range rows {
		if row.OutputSha256 != outputHash || row.RubricVersion != rubricVersion || row.PromptVersion != promptVersion {
			return false
		}
	}
	return true
}

func readJSONLOrEmpty[T any](path string) ([]T, error) {
	rows, err := internalio.ReadJSONL[T](path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return rows, nil
}

func step60VersionsMatch(paths step60Paths, rubricVersion, promptVersion string) (bool, error) {
	scoreRaw, err := readJSONLOrEmpty[contracts.RawScoreEntry](paths.ScoresRaw)
	if err != nil {
		return false, err
	}
	complianceRaw, err := readJSONLOrEmpty[contracts.RawComplianceEntry](paths.ComplianceRaw)
	if err != nil {
		return false, err
	}
	scoreFinal, err := readJSONLOrEmpty[contracts.ScoreEntry](paths.ScoresFinal)
	if err != nil {
		return false, err
	}
	complianceFinal, err := readJSONLOrEmpty[contracts.ComplianceEntry](paths.ComplianceFinal)
	if err != nil {
		return false, err
	}
	pairwiseRows, err := readJSONLOrEmpty[contracts.PairwiseEntry](paths.Pairwise)
	if err != nil {
		return false, err
	}
	for _, row := range reduceRawScores(scoreRaw) {
		if row.RubricVersion != rubricVersion || row.PromptVersion != promptVersion {
			return false, nil
		}
	}
	for _, row := range reduceRawCompliance(complianceRaw) {
		if row.RubricVersion != rubricVersion || row.PromptVersion != promptVersion {
			return false, nil
		}
	}
	for _, row := range scoreFinal {
		if row.RubricVersion != rubricVersion || row.PromptVersion != promptVersion {
			return false, nil
		}
	}
	for _, row := range complianceFinal {
		if row.RubricVersion != rubricVersion || row.PromptVersion != promptVersion {
			return false, nil
		}
	}
	for _, row := range pairwiseRows {
		if row.RubricVersion != rubricVersion || row.PromptVersion != promptVersion {
			return false, nil
		}
	}
	return true, nil
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func finalizeScore(meta finalMetadata, score contracts.ScoreEntry, path contracts.VerdictPath) contracts.ScoreEntry {
	score.VerdictPath = path
	score.RubricVersion = meta.RubricVersion
	score.PromptVersion = meta.PromptVersion
	score.ResolvedAt = meta.ResolvedAt
	return score
}

func finalizeCompliance(meta finalMetadata, entry contracts.ComplianceEntry, path contracts.VerdictPath) contracts.ComplianceEntry {
	entry.VerdictPath = path
	entry.RubricVersion = meta.RubricVersion
	entry.PromptVersion = meta.PromptVersion
	entry.ResolvedAt = meta.ResolvedAt
	return entry
}

func complianceEntryOrMissed(
	meta finalMetadata,
	agent contracts.AgentID,
	ruleID string,
	entry contracts.ComplianceEntry,
	ok bool,
) contracts.ComplianceEntry {
	if ok {
		return entry
	}
	return contracts.ComplianceEntry{
		SchemaVersion: "1",
		RunID:         meta.RunID,
		Pass:          meta.Pass,
		Agent:         agent,
		RuleID:        ruleID,
		Verdict:       contracts.ComplianceVerdictMissed,
		RubricVersion: meta.RubricVersion,
		PromptVersion: meta.PromptVersion,
		ResolvedAt:    meta.ResolvedAt,
	}
}

func preferredComplianceAgreementSource(
	primary contracts.ComplianceEntry,
	secondary contracts.ComplianceEntry,
	primaryOK bool,
	secondaryOK bool,
) contracts.ComplianceEntry {
	if primaryOK {
		return primary
	}
	if secondaryOK {
		return secondary
	}
	return primary
}

func preferredComplianceSingleSource(
	primary contracts.ComplianceEntry,
	secondary contracts.ComplianceEntry,
	primaryOK bool,
	secondaryOK bool,
) contracts.ComplianceEntry {
	if primaryOK && primary.Verdict != contracts.ComplianceVerdictMissed {
		return primary
	}
	if secondaryOK && secondary.Verdict != contracts.ComplianceVerdictMissed {
		return secondary
	}
	if primaryOK {
		return primary
	}
	return secondary
}

func makeRawScoreEntry(
	score contracts.ScoreEntry,
	role contracts.JudgeRole,
	outputHash string,
	primaryRef *contracts.RawJudgeRef,
	secondaryRef *contracts.RawJudgeRef,
	resolvedAt time.Time,
) contracts.RawScoreEntry {
	return contracts.RawScoreEntry{
		SchemaVersion:      "1",
		RunID:              score.RunID,
		Pass:               score.Pass,
		Agent:              score.Agent,
		JudgeRole:          role,
		Dimension:          score.Dimension,
		Score:              score.Score,
		Reasons:            score.Reasons,
		ReasonsOverflowRef: score.ReasonsOverflowRef,
		OutputSha256:       outputHash,
		PrimaryRef:         primaryRef,
		SecondaryRef:       secondaryRef,
		RubricVersion:      score.RubricVersion,
		PromptVersion:      score.PromptVersion,
		ResolvedAt:         resolvedAt,
	}
}

func makeRawComplianceEntry(
	entry contracts.ComplianceEntry,
	role contracts.JudgeRole,
	outputHash string,
	primaryRef *contracts.RawJudgeRef,
	secondaryRef *contracts.RawJudgeRef,
	resolvedAt time.Time,
) contracts.RawComplianceEntry {
	return contracts.RawComplianceEntry{
		SchemaVersion:        "1",
		RunID:                entry.RunID,
		Pass:                 entry.Pass,
		Agent:                entry.Agent,
		JudgeRole:            role,
		RuleID:               entry.RuleID,
		Verdict:              entry.Verdict,
		Rationale:            entry.Rationale,
		RationaleOverflowRef: entry.RationaleOverflowRef,
		OutputSha256:         outputHash,
		PrimaryRef:           primaryRef,
		SecondaryRef:         secondaryRef,
		RubricVersion:        entry.RubricVersion,
		PromptVersion:        entry.PromptVersion,
		ResolvedAt:           resolvedAt,
	}
}

func loadPass1Scores(runIO internalio.RunContext) (map[contracts.AgentID][]contracts.ScoreEntry, error) {
	path, err := runIO.ResolveRunRelative("30/scores-A.jsonl")
	if err != nil {
		return nil, fmt.Errorf("step60: resolve pass1 scores path: %w", err)
	}
	rows, err := internalio.ReadJSONL[contracts.ScoreEntry](path)
	if err != nil {
		return nil, fmt.Errorf("step60: read pass1 scores: %w", err)
	}
	collapsed := internalio.CollapseByKey(rows, func(entry contracts.ScoreEntry) scoreKey {
		return scoreKey{Agent: entry.Agent, Dimension: entry.Dimension}
	})
	byAgent := make(map[contracts.AgentID][]contracts.ScoreEntry, len(collapsed))
	for _, entry := range collapsed {
		byAgent[entry.Agent] = append(byAgent[entry.Agent], entry)
	}
	return byAgent, nil
}

func loadPass1ComplianceRuleIDs(runIO internalio.RunContext) (map[contracts.AgentID]map[string]struct{}, error) {
	path, err := runIO.ResolveRunRelative("30/compliance-A.jsonl")
	if err != nil {
		return nil, fmt.Errorf("step60: resolve pass1 compliance path: %w", err)
	}
	rows, err := internalio.ReadJSONL[contracts.ComplianceEntry](path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[contracts.AgentID]map[string]struct{}{}, nil
		}
		return nil, fmt.Errorf("step60: read pass1 compliance: %w", err)
	}
	collapsed := scorecore.CollapseFinalCompliance(rows)
	byAgent := make(map[contracts.AgentID]map[string]struct{}, len(collapsed))
	for _, entry := range collapsed {
		if _, ok := byAgent[entry.Agent]; !ok {
			byAgent[entry.Agent] = map[string]struct{}{}
		}
		byAgent[entry.Agent][entry.RuleID] = struct{}{}
	}
	return byAgent, nil
}

func expectedComplianceRuleIDsForAgent(
	agent contracts.AgentID,
	pass1Rules map[contracts.AgentID]map[string]struct{},
	finalRules map[string]contracts.ComplianceEntry,
	fallbackRules []string,
) map[string]struct{} {
	rules := make(map[string]struct{})
	for ruleID := range pass1Rules[agent] {
		rules[ruleID] = struct{}{}
	}
	for ruleID := range finalRules {
		rules[ruleID] = struct{}{}
	}
	if len(rules) == 0 {
		for _, ruleID := range fallbackRules {
			rules[ruleID] = struct{}{}
		}
	}
	if len(rules) == 0 {
		return nil
	}
	return rules
}

func resolvePass1AverageTenths(runIO internalio.RunContext, agent contracts.AgentID, scores []contracts.ScoreEntry) (int, error) {
	if len(scores) == len(canonicalDimensions) {
		return averageScoresTenths(scores)
	}
	path, err := runIO.ResolveRunRelative("30/scores-A.jsonl")
	if err != nil {
		return 0, fmt.Errorf("step60: resolve pass1 scores path for agent=%s: %w", agent, err)
	}
	return 0, fmt.Errorf("%w: agent=%s path=%s got=%d want=%d", ErrPass1ScoresIncomplete, agent, path, len(scores), len(canonicalDimensions))
}

func averageScoresTenths(scores []contracts.ScoreEntry) (int, error) {
	if len(scores) != len(canonicalDimensions) {
		return 0, fmt.Errorf("step60: average requires %d scores, got %d", len(canonicalDimensions), len(scores))
	}
	total := 0
	for _, score := range scores {
		total += score.Score
	}
	return total * 10 / len(scores), nil
}

func makePairwiseEntry(in Input, agent contracts.AgentID, pass1AverageTenths, pass2AverageTenths int, resolvedAt time.Time) contracts.PairwiseEntry {
	winner := contracts.PairwiseWinnerTie
	switch {
	case pass2AverageTenths > pass1AverageTenths:
		winner = contracts.PairwiseWinnerB
	case pass1AverageTenths > pass2AverageTenths:
		winner = contracts.PairwiseWinnerA
	}

	margin := contracts.PairwiseMarginSlight
	deltaTenths := pass2AverageTenths - pass1AverageTenths
	if deltaTenths < 0 {
		deltaTenths = -deltaTenths
	}
	switch {
	case deltaTenths > 100:
		margin = contracts.PairwiseMarginDecisive
	case deltaTenths > 30:
		margin = contracts.PairwiseMarginClear
	}

	return contracts.PairwiseEntry{
		SchemaVersion: "1",
		RunID:         in.TaskPackage.RunID,
		AgentA:        agent,
		AgentB:        agent,
		Winner:        winner,
		Margin:        margin,
		Justification: fmt.Sprintf("pass1_avg_tenths=%d pass2_avg_tenths=%d", pass1AverageTenths, pass2AverageTenths),
		// Pairwise is derived from one pass1 aggregate and one pass2 aggregate, not a panel verdict.
		VerdictPath:   contracts.VerdictPathSingle,
		RubricVersion: in.RubricVersion,
		PromptVersion: in.PromptVersion,
		ResolvedAt:    resolvedAt,
	}
}

func sameScoreDecision(left, right contracts.ScoreEntry) bool {
	return left.Score == right.Score &&
		left.Reasons == right.Reasons &&
		overflowRefEqual(left.ReasonsOverflowRef, right.ReasonsOverflowRef)
}

func overflowRefEqual(left, right *contracts.OverflowRef) bool {
	switch {
	case left == nil && right == nil:
		return true
	case left == nil || right == nil:
		return false
	default:
		return left.Path == right.Path && left.Sha256 == right.Sha256
	}
}

func scoreVerdictPath(primary, secondary, arbiter contracts.ScoreEntry) contracts.VerdictPath {
	if sameScoreDecision(primary, secondary) {
		return contracts.VerdictPathAgreement
	}
	if sameScoreDecision(arbiter, primary) || sameScoreDecision(arbiter, secondary) {
		return contracts.VerdictPathArbitrated
	}
	return contracts.VerdictPathArbiterOverruled
}

func complianceVerdictPath(primary, secondary, arbiter contracts.ComplianceEntry) contracts.VerdictPath {
	if primary.Verdict == secondary.Verdict {
		return contracts.VerdictPathAgreement
	}
	if arbiter.Verdict == primary.Verdict || arbiter.Verdict == secondary.Verdict {
		return contracts.VerdictPathArbitrated
	}
	return contracts.VerdictPathArbiterOverruled
}

func complianceRuleIDs(
	primary map[string]contracts.ComplianceEntry,
	secondary map[string]contracts.ComplianceEntry,
	_ map[string]contracts.ComplianceEntry,
) []string {
	set := make(map[string]struct{}, len(primary)+len(secondary))
	for ruleID := range primary {
		set[ruleID] = struct{}{}
	}
	for ruleID := range secondary {
		set[ruleID] = struct{}{}
	}
	ruleIDs := make([]string, 0, len(set))
	for ruleID := range set {
		ruleIDs = append(ruleIDs, ruleID)
	}
	sort.Strings(ruleIDs)
	return ruleIDs
}

func disputedComplianceRuleIDs(primary, secondary map[string]contracts.ComplianceEntry) []string {
	ruleIDs := make([]string, 0, minInt(len(primary), len(secondary)))
	for ruleID, primaryEntry := range primary {
		secondaryEntry, ok := secondary[ruleID]
		if !ok || primaryEntry.Verdict == secondaryEntry.Verdict {
			continue
		}
		ruleIDs = append(ruleIDs, ruleID)
	}
	sort.Strings(ruleIDs)
	return ruleIDs
}

func sortedComplianceRuleIDs(entries map[string]contracts.ComplianceEntry) []string {
	ruleIDs := make([]string, 0, len(entries))
	for ruleID := range entries {
		ruleIDs = append(ruleIDs, ruleID)
	}
	sort.Strings(ruleIDs)
	return ruleIDs
}

func scoreOutputHash(score contracts.ScoreEntry) (string, error) {
	return canonicalSHA256(score)
}

func complianceOutputHash(entry contracts.ComplianceEntry) (string, error) {
	return canonicalSHA256(entry)
}

func rawScoreEntryHash(entry contracts.RawScoreEntry) (string, error) {
	return canonicalSHA256(entry)
}

func rawComplianceEntryHash(entry contracts.RawComplianceEntry) (string, error) {
	return canonicalSHA256(entry)
}

func hashFinalScores(entries []contracts.ScoreEntry) (string, error) {
	collapsed := internalio.CollapseByKey(entries, func(entry contracts.ScoreEntry) scoreKey {
		return scoreKey{Agent: entry.Agent, Dimension: entry.Dimension}
	})
	sort.Slice(collapsed, func(i, j int) bool {
		if collapsed[i].Agent != collapsed[j].Agent {
			return collapsed[i].Agent < collapsed[j].Agent
		}
		return collapsed[i].Dimension < collapsed[j].Dimension
	})
	return hashCanonicalRows(collapsed)
}

func hashFinalCompliance(entries []contracts.ComplianceEntry) (string, error) {
	collapsed := internalio.CollapseByKey(entries, func(entry contracts.ComplianceEntry) complianceKey {
		return complianceKey{Agent: entry.Agent, RuleID: entry.RuleID}
	})
	sort.Slice(collapsed, func(i, j int) bool {
		if collapsed[i].Agent != collapsed[j].Agent {
			return collapsed[i].Agent < collapsed[j].Agent
		}
		return collapsed[i].RuleID < collapsed[j].RuleID
	})
	return hashCanonicalRows(collapsed)
}

func hashFinalPairwise(entries []contracts.PairwiseEntry) (string, error) {
	collapsed := internalio.CollapseByKey(entries, func(entry contracts.PairwiseEntry) complianceKey {
		return complianceKey{Agent: entry.AgentA, RuleID: string(entry.AgentB)}
	})
	sort.Slice(collapsed, func(i, j int) bool {
		if collapsed[i].AgentA != collapsed[j].AgentA {
			return collapsed[i].AgentA < collapsed[j].AgentA
		}
		return collapsed[i].AgentB < collapsed[j].AgentB
	})
	return hashCanonicalRows(collapsed)
}

func hashReducedRawScoresFile(runIO internalio.RunContext, path string) (string, error) {
	rows, err := internalio.ReadJSONL[contracts.RawScoreEntry](path)
	if err != nil {
		return "", fmt.Errorf("read raw scores: %w", err)
	}
	reduced := reduceRawScores(rows)
	if err := validateRawScoreOverflowRefs(runIO, reduced); err != nil {
		return "", err
	}
	return hashCanonicalRows(reduced)
}

func hashReducedRawComplianceFile(runIO internalio.RunContext, path string) (string, error) {
	rows, err := internalio.ReadJSONL[contracts.RawComplianceEntry](path)
	if err != nil {
		return "", fmt.Errorf("read raw compliance: %w", err)
	}
	reduced := reduceRawCompliance(rows)
	if err := validateRawComplianceOverflowRefs(runIO, reduced); err != nil {
		return "", err
	}
	return hashCanonicalRows(reduced)
}

func validateScoreOverflowRefs(runIO internalio.RunContext, rows []contracts.ScoreEntry) error {
	for _, row := range rows {
		if row.ReasonsOverflowRef == nil {
			continue
		}
		if _, err := internalio.ReadSidecar(runIO, *row.ReasonsOverflowRef); err != nil {
			return err
		}
	}
	return nil
}

func validateComplianceOverflowRefs(runIO internalio.RunContext, rows []contracts.ComplianceEntry) error {
	for _, row := range rows {
		if row.RationaleOverflowRef == nil {
			continue
		}
		if _, err := internalio.ReadSidecar(runIO, *row.RationaleOverflowRef); err != nil {
			return err
		}
	}
	return nil
}

func validateRawScoreOverflowRefs(runIO internalio.RunContext, rows []contracts.RawScoreEntry) error {
	for _, row := range rows {
		if row.ReasonsOverflowRef == nil {
			continue
		}
		if _, err := internalio.ReadSidecar(runIO, *row.ReasonsOverflowRef); err != nil {
			return err
		}
	}
	return nil
}

func validateRawComplianceOverflowRefs(runIO internalio.RunContext, rows []contracts.RawComplianceEntry) error {
	for _, row := range rows {
		if row.RationaleOverflowRef == nil {
			continue
		}
		if _, err := internalio.ReadSidecar(runIO, *row.RationaleOverflowRef); err != nil {
			return err
		}
	}
	return nil
}

func buildRawScores(
	scores map[contracts.Dimension]contracts.ScoreEntry,
	outputHash string,
	role contracts.JudgeRole,
	primaryRefs map[contracts.Dimension]*contracts.RawJudgeRef,
	secondaryRefs map[contracts.Dimension]*contracts.RawJudgeRef,
	resolvedAt time.Time,
) ([]contracts.RawScoreEntry, map[contracts.Dimension]*contracts.RawJudgeRef, error) {
	rows := make([]contracts.RawScoreEntry, 0, len(scores))
	refs := make(map[contracts.Dimension]*contracts.RawJudgeRef, len(scores))
	for _, dimension := range canonicalDimensions {
		score, ok := scores[dimension]
		if !ok {
			continue
		}
		row := makeRawScoreEntry(score, role, outputHash, primaryRefs[dimension], secondaryRefs[dimension], resolvedAt)
		hash, err := rawScoreEntryHash(row)
		if err != nil {
			return nil, nil, err
		}
		refs[dimension] = &contracts.RawJudgeRef{Role: role, Sha256: hash}
		rows = append(rows, row)
	}
	return rows, refs, nil
}

func fileSHA256(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return sha256Hex(data), nil
}

func reduceRawScores(rows []contracts.RawScoreEntry) []contracts.RawScoreEntry {
	return scorecore.CollapseRawScores(rows)
}

func reduceRawCompliance(rows []contracts.RawComplianceEntry) []contracts.RawComplianceEntry {
	return scorecore.CollapseRawCompliance(rows)
}

func collapseRawScoresByRole(rows []contracts.RawScoreEntry, role contracts.JudgeRole) []contracts.RawScoreEntry {
	filtered := make([]contracts.RawScoreEntry, 0, len(rows))
	for _, entry := range rows {
		if entry.JudgeRole == role {
			filtered = append(filtered, entry)
		}
	}
	return internalio.CollapseByKey(filtered, func(entry contracts.RawScoreEntry) rawScoreKey {
		return rawScoreKey{Agent: entry.Agent, JudgeRole: entry.JudgeRole, Dimension: entry.Dimension}
	})
}

func collapseRawComplianceByRole(rows []contracts.RawComplianceEntry, role contracts.JudgeRole) []contracts.RawComplianceEntry {
	filtered := make([]contracts.RawComplianceEntry, 0, len(rows))
	for _, entry := range rows {
		if entry.JudgeRole == role {
			filtered = append(filtered, entry)
		}
	}
	return internalio.CollapseByKey(filtered, func(entry contracts.RawComplianceEntry) rawComplianceKey {
		return rawComplianceKey{Agent: entry.Agent, JudgeRole: entry.JudgeRole, RuleID: entry.RuleID}
	})
}

func hashCanonicalRows[T any](rows []T) (string, error) {
	joined := make([]byte, 0)
	for i, row := range rows {
		payload, err := contracts.CanonicalMarshal(row)
		if err != nil {
			return "", err
		}
		if i > 0 {
			joined = append(joined, 0x00)
		}
		joined = append(joined, payload...)
	}
	return sha256Hex(joined), nil
}

func canonicalSHA256(v any) (string, error) {
	data, err := contracts.CanonicalMarshal(v)
	if err != nil {
		return "", err
	}
	return sha256Hex(data), nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
