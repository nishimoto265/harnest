package step60_scorepairwise

import (
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
	"github.com/nishimoto265/auto-improve/internal/contracts/stepio"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/judges"
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
	ErrNoScorablePass2Agents = errors.New("step60: no scorable pass2 agents found")
	ErrPass1ScoresIncomplete = errors.New("step60: pass1 scores incomplete")
)

var canonicalDimensions = []contracts.Dimension{
	contracts.DimensionFidelity,
	contracts.DimensionCorrectness,
	contracts.DimensionMaintainability,
	contracts.DimensionDiscipline,
	contracts.DimensionCommunication,
}

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
	skip, err := verifyDoneMarker(paths)
	if err != nil {
		return err
	}
	if skip {
		return nil
	}

	request := stepio.Step60Request{
		TaskPackage:    *in.TaskPackage,
		ScorableAgents: deriveScorableAgents(in),
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
	scorableRuns, err := collectScorableAgentRuns(in, request.ScorableAgents)
	if err != nil {
		return err
	}

	if err := truncateStep60Artifacts(paths); err != nil {
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
		primaryOutput, err := scoreJudgeOutput(ctx, "primary", in.Primary, run.JudgeInput)
		if err != nil {
			return err
		}
		secondaryOutput, err := scoreJudgeOutput(ctx, "secondary", in.Secondary, run.JudgeInput)
		if err != nil {
			return err
		}

		primaryScores := normalizeScores(primaryOutput.Scores, in.RubricVersion, in.PromptVersion)
		secondaryScores := normalizeScores(secondaryOutput.Scores, in.RubricVersion, in.PromptVersion)
		primaryCompliance := normalizeCompliance(primaryOutput.Compliance, in.RubricVersion, in.PromptVersion)
		secondaryCompliance := normalizeCompliance(secondaryOutput.Compliance, in.RubricVersion, in.PromptVersion)

		scoreDisagreements := scoreDisagreementDimensions(primaryScores, secondaryScores)
		complianceDisagreements := complianceDisagreementRuleIDs(primaryCompliance, secondaryCompliance)

		var arbiterScores map[contracts.Dimension]contracts.ScoreEntry
		var arbiterCompliance map[string]contracts.ComplianceEntry
		if len(scoreDisagreements) > 0 || len(complianceDisagreements) > 0 {
			arbiterOutput, err := scoreJudgeOutput(ctx, "arbiter", in.Arbiter, run.JudgeInput)
			if err != nil {
				return err
			}
			arbiterScores = normalizeScores(arbiterOutput.Scores, in.RubricVersion, in.PromptVersion)
			arbiterCompliance = normalizeCompliance(arbiterOutput.Compliance, in.RubricVersion, in.PromptVersion)
		}

		agentScores, err := emitScores(paths, meta, run.Agent, primaryScores, secondaryScores, arbiterScores)
		if err != nil {
			return err
		}
		agentCompliance, err := emitCompliance(paths, meta, run.Agent, primaryCompliance, secondaryCompliance, arbiterCompliance)
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
		if err := internalio.AppendJSONL(paths.Pairwise, entry); err != nil {
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
	scoresRawHash, err := hashReducedRawScoresFile(paths.ScoresRaw)
	if err != nil {
		return fmt.Errorf("step60: hash scores raw: %w", err)
	}
	complianceRawHash, err := hashReducedRawComplianceFile(paths.ComplianceRaw)
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
		in.RubricPath = filepath.Join(in.IO.RunDir(), "rubrics", "default.md")
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
		Done:            donePath,
		ScoresRaw:       scoresRawPath,
		ScoresFinal:     scoresFinalPath,
		ComplianceRaw:   complianceRawPath,
		ComplianceFinal: complianceFinalPath,
		Pairwise:        pairwisePath,
	}, nil
}

func deriveScorableAgents(in Input) []contracts.AgentID {
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

func shouldSkipAgent(err error) bool {
	return errors.Is(err, internalio.ErrNotScorable)
}

func collectScorableAgentRuns(in Input, agents []contracts.AgentID) ([]scorableAgentRun, error) {
	runs := make([]scorableAgentRun, 0, len(agents))
	for _, agent := range agents {
		manifest, err := internalio.LoadScorableManifest(in.IO, 2, agent)
		if shouldSkipAgent(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("step60: load pass2 manifest for agent=%s: %w", agent, err)
		}
		outputPath, err := in.IO.ResolveRunRelative(manifest.DiffPath)
		if err != nil {
			return nil, fmt.Errorf("step60: resolve pass2 diff path for agent=%s: %w", agent, err)
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

func truncateStep60Artifacts(paths step60Paths) error {
	for _, path := range []string{
		paths.ScoresRaw,
		paths.ScoresFinal,
		paths.ComplianceRaw,
		paths.ComplianceFinal,
		paths.Pairwise,
	} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("step60: truncate artifact %s: %w", filepath.Base(path), err)
		}
	}
	return nil
}

func scoreJudgeOutput(ctx context.Context, label string, judge judges.Judge, input judges.JudgeInput) (judges.JudgeOutput, error) {
	output, err := judge.ScoreOutput(ctx, input)
	if err != nil {
		return judges.JudgeOutput{}, fmt.Errorf("step60: %s judge score output for agent=%s: %w", label, input.Agent, err)
	}
	if err := output.ValidateFor(input); err != nil {
		return judges.JudgeOutput{}, fmt.Errorf("step60: validate %s judge output for agent=%s: %w", label, input.Agent, err)
	}
	return output, nil
}

func normalizeScores(scores []contracts.ScoreEntry, rubricVersion, promptVersion string) map[contracts.Dimension]contracts.ScoreEntry {
	out := make(map[contracts.Dimension]contracts.ScoreEntry, len(scores))
	for _, score := range scores {
		score.RubricVersion = rubricVersion
		score.PromptVersion = promptVersion
		out[score.Dimension] = score
	}
	return out
}

func normalizeCompliance(entries []contracts.ComplianceEntry, rubricVersion, promptVersion string) map[string]contracts.ComplianceEntry {
	out := make(map[string]contracts.ComplianceEntry, len(entries))
	for _, entry := range entries {
		entry.RubricVersion = rubricVersion
		entry.PromptVersion = promptVersion
		out[entry.RuleID] = entry
	}
	return out
}

func scoreDisagreementDimensions(primary, secondary map[contracts.Dimension]contracts.ScoreEntry) []contracts.Dimension {
	disagreements := make([]contracts.Dimension, 0, len(canonicalDimensions))
	for _, dimension := range canonicalDimensions {
		p, ok := primary[dimension]
		if !ok {
			disagreements = append(disagreements, dimension)
			continue
		}
		s, ok := secondary[dimension]
		if !ok || !sameScoreDecision(p, s) {
			disagreements = append(disagreements, dimension)
		}
	}
	return disagreements
}

func complianceDisagreementRuleIDs(primary, secondary map[string]contracts.ComplianceEntry) []string {
	ruleIDs := complianceRuleIDs(primary, secondary, nil)
	disagreements := make([]string, 0, len(ruleIDs))
	for _, ruleID := range ruleIDs {
		if complianceVerdict(primary, ruleID) != complianceVerdict(secondary, ruleID) {
			disagreements = append(disagreements, ruleID)
		}
	}
	return disagreements
}

func emitScores(
	paths step60Paths,
	meta finalMetadata,
	agent contracts.AgentID,
	primary map[contracts.Dimension]contracts.ScoreEntry,
	secondary map[contracts.Dimension]contracts.ScoreEntry,
	arbiter map[contracts.Dimension]contracts.ScoreEntry,
) ([]contracts.ScoreEntry, error) {
	finalScores := make([]contracts.ScoreEntry, 0, len(canonicalDimensions))
	for _, dimension := range canonicalDimensions {
		primaryScore, ok := primary[dimension]
		if !ok {
			return nil, fmt.Errorf("step60: primary score missing dimension=%s agent=%s", dimension, agent)
		}
		secondaryScore, ok := secondary[dimension]
		if !ok {
			return nil, fmt.Errorf("step60: secondary score missing dimension=%s agent=%s", dimension, agent)
		}

		primaryHash, err := scoreOutputHash(primaryScore)
		if err != nil {
			return nil, fmt.Errorf("step60: hash primary score dimension=%s agent=%s: %w", dimension, agent, err)
		}
		secondaryHash, err := scoreOutputHash(secondaryScore)
		if err != nil {
			return nil, fmt.Errorf("step60: hash secondary score dimension=%s agent=%s: %w", dimension, agent, err)
		}
		if err := internalio.AppendJSONL(paths.ScoresRaw, makeRawScoreEntry(primaryScore, contracts.JudgeRolePrimary, primaryHash, nil, nil, meta.ResolvedAt)); err != nil {
			return nil, fmt.Errorf("step60: append primary raw score dimension=%s agent=%s: %w", dimension, agent, err)
		}
		if err := internalio.AppendJSONL(paths.ScoresRaw, makeRawScoreEntry(secondaryScore, contracts.JudgeRoleSecondary, secondaryHash, nil, nil, meta.ResolvedAt)); err != nil {
			return nil, fmt.Errorf("step60: append secondary raw score dimension=%s agent=%s: %w", dimension, agent, err)
		}

		finalScore := finalizeScore(meta, primaryScore, contracts.VerdictPathAgreement)
		if !sameScoreDecision(primaryScore, secondaryScore) {
			arbiterScore, ok := arbiter[dimension]
			if !ok {
				return nil, fmt.Errorf("step60: arbiter score missing dimension=%s agent=%s", dimension, agent)
			}
			arbiterHash, err := scoreOutputHash(arbiterScore)
			if err != nil {
				return nil, fmt.Errorf("step60: hash arbiter score dimension=%s agent=%s: %w", dimension, agent, err)
			}
			if err := internalio.AppendJSONL(paths.ScoresRaw, makeRawScoreEntry(
				arbiterScore,
				contracts.JudgeRoleArbiter,
				arbiterHash,
				&contracts.RawJudgeRef{Role: contracts.JudgeRolePrimary, Sha256: primaryHash},
				&contracts.RawJudgeRef{Role: contracts.JudgeRoleSecondary, Sha256: secondaryHash},
				meta.ResolvedAt,
			)); err != nil {
				return nil, fmt.Errorf("step60: append arbiter raw score dimension=%s agent=%s: %w", dimension, agent, err)
			}
			finalScore = finalizeScore(meta, arbiterScore, scoreVerdictPath(primaryScore, secondaryScore, arbiterScore))
		}

		if err := internalio.AppendJSONL(paths.ScoresFinal, finalScore); err != nil {
			return nil, fmt.Errorf("step60: append final score dimension=%s agent=%s: %w", dimension, agent, err)
		}
		finalScores = append(finalScores, finalScore)
	}
	return finalScores, nil
}

func emitCompliance(
	paths step60Paths,
	meta finalMetadata,
	agent contracts.AgentID,
	primary map[string]contracts.ComplianceEntry,
	secondary map[string]contracts.ComplianceEntry,
	arbiter map[string]contracts.ComplianceEntry,
) ([]contracts.ComplianceEntry, error) {
	ruleIDs := complianceRuleIDs(primary, secondary, nil)
	finalEntries := make([]contracts.ComplianceEntry, 0, len(ruleIDs))
	for _, ruleID := range ruleIDs {
		primaryEntry, primaryOK := primary[ruleID]
		secondaryEntry, secondaryOK := secondary[ruleID]
		arbiterEntry, arbiterOK := arbiter[ruleID]

		var primaryHash string
		if primaryOK {
			var err error
			primaryHash, err = complianceOutputHash(primaryEntry)
			if err != nil {
				return nil, fmt.Errorf("step60: hash primary compliance rule=%s agent=%s: %w", ruleID, agent, err)
			}
			if err := internalio.AppendJSONL(paths.ComplianceRaw, makeRawComplianceEntry(primaryEntry, contracts.JudgeRolePrimary, primaryHash, nil, nil, meta.ResolvedAt)); err != nil {
				return nil, fmt.Errorf("step60: append primary raw compliance rule=%s agent=%s: %w", ruleID, agent, err)
			}
		}
		var secondaryHash string
		if secondaryOK {
			var err error
			secondaryHash, err = complianceOutputHash(secondaryEntry)
			if err != nil {
				return nil, fmt.Errorf("step60: hash secondary compliance rule=%s agent=%s: %w", ruleID, agent, err)
			}
			if err := internalio.AppendJSONL(paths.ComplianceRaw, makeRawComplianceEntry(secondaryEntry, contracts.JudgeRoleSecondary, secondaryHash, nil, nil, meta.ResolvedAt)); err != nil {
				return nil, fmt.Errorf("step60: append secondary raw compliance rule=%s agent=%s: %w", ruleID, agent, err)
			}
		}

		primaryDecision := complianceEntryOrMissed(meta, agent, ruleID, primaryEntry, primaryOK)
		secondaryDecision := complianceEntryOrMissed(meta, agent, ruleID, secondaryEntry, secondaryOK)

		var finalEntry contracts.ComplianceEntry
		switch {
		case primaryDecision.Verdict == secondaryDecision.Verdict:
			finalEntry = finalizeCompliance(meta, preferredComplianceAgreementSource(primaryDecision, secondaryDecision, primaryOK, secondaryOK), contracts.VerdictPathAgreement)
		case primaryOK && secondaryOK:
			if !arbiterOK {
				return nil, fmt.Errorf("step60: arbiter compliance missing rule=%s agent=%s", ruleID, agent)
			}
			arbiterHash, err := complianceOutputHash(arbiterEntry)
			if err != nil {
				return nil, fmt.Errorf("step60: hash arbiter compliance rule=%s agent=%s: %w", ruleID, agent, err)
			}
			if err := internalio.AppendJSONL(paths.ComplianceRaw, makeRawComplianceEntry(
				arbiterEntry,
				contracts.JudgeRoleArbiter,
				arbiterHash,
				&contracts.RawJudgeRef{Role: contracts.JudgeRolePrimary, Sha256: primaryHash},
				&contracts.RawJudgeRef{Role: contracts.JudgeRoleSecondary, Sha256: secondaryHash},
				meta.ResolvedAt,
			)); err != nil {
				return nil, fmt.Errorf("step60: append arbiter raw compliance rule=%s agent=%s: %w", ruleID, agent, err)
			}
			finalEntry = finalizeCompliance(meta, arbiterEntry, complianceVerdictPath(primaryDecision, secondaryDecision, arbiterEntry))
		default:
			// Single-sided rules finalize directly from the side that emitted them.
			// Raw arbiter refs require both sides, so using arbiter output here would
			// break provenance in compliance-B-raw.jsonl.
			finalEntry = finalizeCompliance(meta, preferredComplianceSingleSource(primaryDecision, secondaryDecision, primaryOK, secondaryOK), contracts.VerdictPathSingle)
		}

		if err := internalio.AppendJSONL(paths.ComplianceFinal, finalEntry); err != nil {
			return nil, fmt.Errorf("step60: append final compliance rule=%s agent=%s: %w", ruleID, agent, err)
		}
		finalEntries = append(finalEntries, finalEntry)
	}
	return finalEntries, nil
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

func verifyDoneMarker(paths step60Paths) (bool, error) {
	if _, err := os.Stat(paths.Done); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("step60: stat done marker: %w", err)
	}

	marker, err := internalio.ReadJSON[contracts.Step60DoneMarker](paths.Done)
	if err != nil {
		if err := os.Remove(paths.Done); err != nil && !os.IsNotExist(err) {
			return false, fmt.Errorf("step60: remove unreadable done marker: %w", err)
		}
		return false, nil
	}
	if err := marker.Validate(); err != nil {
		if err := os.Remove(paths.Done); err != nil && !os.IsNotExist(err) {
			return false, fmt.Errorf("step60: remove invalid done marker: %w", err)
		}
		return false, nil
	}

	current, err := currentStep60State(paths)
	if err != nil {
		return false, err
	}
	if marker.ExpectedCounts == current.ExpectedCounts &&
		marker.ContentHashes == current.ContentHashes &&
		marker.RawHashes == current.RawHashes {
		return true, nil
	}

	if err := os.Remove(paths.Done); err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("step60: remove stale done marker: %w", err)
	}
	return false, nil
}

func currentStep60State(paths step60Paths) (contracts.Step60DoneMarker, error) {
	scores, err := internalio.ReadJSONL[contracts.ScoreEntry](paths.ScoresFinal)
	if err != nil {
		return contracts.Step60DoneMarker{}, fmt.Errorf("step60: read scores final for marker verify: %w", err)
	}
	compliance, err := internalio.ReadJSONL[contracts.ComplianceEntry](paths.ComplianceFinal)
	if err != nil {
		return contracts.Step60DoneMarker{}, fmt.Errorf("step60: read compliance final for marker verify: %w", err)
	}
	pairwise, err := internalio.ReadJSONL[contracts.PairwiseEntry](paths.Pairwise)
	if err != nil {
		return contracts.Step60DoneMarker{}, fmt.Errorf("step60: read pairwise final for marker verify: %w", err)
	}

	scoresFinalHash, err := hashFinalScores(scores)
	if err != nil {
		return contracts.Step60DoneMarker{}, fmt.Errorf("step60: hash scores final for marker verify: %w", err)
	}
	complianceFinalHash, err := hashFinalCompliance(compliance)
	if err != nil {
		return contracts.Step60DoneMarker{}, fmt.Errorf("step60: hash compliance final for marker verify: %w", err)
	}
	pairwiseFinalHash, err := hashFinalPairwise(pairwise)
	if err != nil {
		return contracts.Step60DoneMarker{}, fmt.Errorf("step60: hash pairwise final for marker verify: %w", err)
	}
	scoresRawHash, err := hashReducedRawScoresFile(paths.ScoresRaw)
	if err != nil {
		return contracts.Step60DoneMarker{}, fmt.Errorf("step60: hash scores raw for marker verify: %w", err)
	}
	complianceRawHash, err := hashReducedRawComplianceFile(paths.ComplianceRaw)
	if err != nil {
		return contracts.Step60DoneMarker{}, fmt.Errorf("step60: hash compliance raw for marker verify: %w", err)
	}

	return contracts.Step60DoneMarker{
		ExpectedCounts: contracts.Step60ExpectedCounts{
			Scores: int64(len(internalio.CollapseByKey(scores, func(entry contracts.ScoreEntry) scoreKey {
				return scoreKey{Agent: entry.Agent, Dimension: entry.Dimension}
			}))),
			Compliance: int64(len(internalio.CollapseByKey(compliance, func(entry contracts.ComplianceEntry) complianceKey {
				return complianceKey{Agent: entry.Agent, RuleID: entry.RuleID}
			}))),
			Pairwise: int64(len(internalio.CollapseByKey(pairwise, func(entry contracts.PairwiseEntry) complianceKey {
				return complianceKey{Agent: entry.AgentA, RuleID: string(entry.AgentB)}
			}))),
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
	}, nil
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
	arbiter map[string]contracts.ComplianceEntry,
) []string {
	set := make(map[string]struct{}, len(primary)+len(secondary)+len(arbiter))
	for ruleID := range primary {
		set[ruleID] = struct{}{}
	}
	for ruleID := range secondary {
		set[ruleID] = struct{}{}
	}
	for ruleID := range arbiter {
		set[ruleID] = struct{}{}
	}
	ruleIDs := make([]string, 0, len(set))
	for ruleID := range set {
		ruleIDs = append(ruleIDs, ruleID)
	}
	sort.Strings(ruleIDs)
	return ruleIDs
}

func complianceVerdict(entries map[string]contracts.ComplianceEntry, ruleID string) contracts.ComplianceVerdict {
	entry, ok := entries[ruleID]
	if !ok {
		return contracts.ComplianceVerdictMissed
	}
	return entry.Verdict
}

func scoreOutputHash(score contracts.ScoreEntry) (string, error) {
	return canonicalSHA256(score)
}

func complianceOutputHash(entry contracts.ComplianceEntry) (string, error) {
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

func hashReducedRawScoresFile(path string) (string, error) {
	rows, err := internalio.ReadJSONL[contracts.RawScoreEntry](path)
	if err != nil {
		return "", fmt.Errorf("read raw scores: %w", err)
	}
	return hashCanonicalRows(reduceRawScores(rows))
}

func hashReducedRawComplianceFile(path string) (string, error) {
	rows, err := internalio.ReadJSONL[contracts.RawComplianceEntry](path)
	if err != nil {
		return "", fmt.Errorf("read raw compliance: %w", err)
	}
	return hashCanonicalRows(reduceRawCompliance(rows))
}

func reduceRawScores(rows []contracts.RawScoreEntry) []contracts.RawScoreEntry {
	primary := collapseRawScoresByRole(rows, contracts.JudgeRolePrimary)
	secondary := collapseRawScoresByRole(rows, contracts.JudgeRoleSecondary)
	primaryByKey := make(map[scoreKey]contracts.RawScoreEntry, len(primary))
	for _, entry := range primary {
		primaryByKey[scoreKey{Agent: entry.Agent, Dimension: entry.Dimension}] = entry
	}
	secondaryByKey := make(map[scoreKey]contracts.RawScoreEntry, len(secondary))
	for _, entry := range secondary {
		secondaryByKey[scoreKey{Agent: entry.Agent, Dimension: entry.Dimension}] = entry
	}

	validArbiters := make([]contracts.RawScoreEntry, 0)
	for _, entry := range rows {
		if entry.JudgeRole != contracts.JudgeRoleArbiter {
			continue
		}
		key := scoreKey{Agent: entry.Agent, Dimension: entry.Dimension}
		primaryEntry, ok := primaryByKey[key]
		if !ok || entry.PrimaryRef == nil || entry.PrimaryRef.Sha256 != primaryEntry.OutputSha256 {
			continue
		}
		secondaryEntry, ok := secondaryByKey[key]
		if !ok || entry.SecondaryRef == nil || entry.SecondaryRef.Sha256 != secondaryEntry.OutputSha256 {
			continue
		}
		validArbiters = append(validArbiters, entry)
	}
	arbiter := internalio.CollapseByKey(validArbiters, func(entry contracts.RawScoreEntry) rawScoreKey {
		return rawScoreKey{Agent: entry.Agent, JudgeRole: entry.JudgeRole, Dimension: entry.Dimension}
	})

	reduced := make([]contracts.RawScoreEntry, 0, len(primary)+len(secondary)+len(arbiter))
	reduced = append(reduced, primary...)
	reduced = append(reduced, secondary...)
	reduced = append(reduced, arbiter...)
	sort.Slice(reduced, func(i, j int) bool {
		if reduced[i].Agent != reduced[j].Agent {
			return reduced[i].Agent < reduced[j].Agent
		}
		if reduced[i].JudgeRole != reduced[j].JudgeRole {
			return reduced[i].JudgeRole < reduced[j].JudgeRole
		}
		return reduced[i].Dimension < reduced[j].Dimension
	})
	return reduced
}

func reduceRawCompliance(rows []contracts.RawComplianceEntry) []contracts.RawComplianceEntry {
	primary := collapseRawComplianceByRole(rows, contracts.JudgeRolePrimary)
	secondary := collapseRawComplianceByRole(rows, contracts.JudgeRoleSecondary)
	primaryByKey := make(map[complianceKey]contracts.RawComplianceEntry, len(primary))
	for _, entry := range primary {
		primaryByKey[complianceKey{Agent: entry.Agent, RuleID: entry.RuleID}] = entry
	}
	secondaryByKey := make(map[complianceKey]contracts.RawComplianceEntry, len(secondary))
	for _, entry := range secondary {
		secondaryByKey[complianceKey{Agent: entry.Agent, RuleID: entry.RuleID}] = entry
	}

	validArbiters := make([]contracts.RawComplianceEntry, 0)
	for _, entry := range rows {
		if entry.JudgeRole != contracts.JudgeRoleArbiter {
			continue
		}
		key := complianceKey{Agent: entry.Agent, RuleID: entry.RuleID}
		primaryEntry, ok := primaryByKey[key]
		if !ok || entry.PrimaryRef == nil || entry.PrimaryRef.Sha256 != primaryEntry.OutputSha256 {
			continue
		}
		secondaryEntry, ok := secondaryByKey[key]
		if !ok || entry.SecondaryRef == nil || entry.SecondaryRef.Sha256 != secondaryEntry.OutputSha256 {
			continue
		}
		validArbiters = append(validArbiters, entry)
	}
	arbiter := internalio.CollapseByKey(validArbiters, func(entry contracts.RawComplianceEntry) rawComplianceKey {
		return rawComplianceKey{Agent: entry.Agent, JudgeRole: entry.JudgeRole, RuleID: entry.RuleID}
	})

	reduced := make([]contracts.RawComplianceEntry, 0, len(primary)+len(secondary)+len(arbiter))
	reduced = append(reduced, primary...)
	reduced = append(reduced, secondary...)
	reduced = append(reduced, arbiter...)
	sort.Slice(reduced, func(i, j int) bool {
		if reduced[i].Agent != reduced[j].Agent {
			return reduced[i].Agent < reduced[j].Agent
		}
		if reduced[i].JudgeRole != reduced[j].JudgeRole {
			return reduced[i].JudgeRole < reduced[j].JudgeRole
		}
		return reduced[i].RuleID < reduced[j].RuleID
	})
	return reduced
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
