package step60_scorepairwise

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/contracts/stepio"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/judges"
)

var (
	ErrNoScorablePass2Agents = errors.New("step60: no scorable pass2 agents found")
	ErrPass1ScoresIncomplete = errors.New("step60: pass1 scores are incomplete")
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

type pairwiseKey struct {
	AgentA contracts.AgentID
	AgentB contracts.AgentID
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
	done            string
	scoresRaw       string
	scoresFinal     string
	complianceRaw   string
	complianceFinal string
	pairwise        string
}

func (p step60Paths) truncateTargets() []string {
	return []string{p.scoresRaw, p.scoresFinal, p.complianceRaw, p.complianceFinal, p.pairwise}
}

type scorablePass2Agent struct {
	agent    contracts.AgentID
	manifest *contracts.ManifestSuccess
}

// Run scores pass2 outputs, emits step60 final artifacts, and returns
// ErrNoScorablePass2Agents before writing any JSONL when no pass2 manifest is
// scorable for this run.
func Run(ctx context.Context, in Input) error {
	in, err := applyDefaults(in)
	if err != nil {
		return err
	}

	resolvedAt := in.Now().UTC()
	paths, err := resolveStep60Paths(in.IO)
	if err != nil {
		return err
	}
	if _, err := os.Stat(paths.done); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("step60: stat done marker: %w", err)
	}

	scorableAgents, err := loadScorablePass2Agents(in.IO, deriveScorableAgents(in))
	if err != nil {
		return err
	}
	if len(scorableAgents) == 0 {
		return ErrNoScorablePass2Agents
	}

	request := stepio.Step60Request{
		TaskPackage:    *in.TaskPackage,
		ScorableAgents: scorableAgentIDs(scorableAgents),
		RubricVersion:  in.RubricVersion,
		PromptVersion:  in.PromptVersion,
	}
	if err := request.Validate(); err != nil {
		return fmt.Errorf("step60: validate request: %w", err)
	}
	if err := truncateStep60Artifacts(paths.truncateTargets()); err != nil {
		return err
	}

	pass1ScoresByAgent, err := loadPass1Scores(in.IO)
	if err != nil {
		return err
	}

	finalScores := make([]contracts.ScoreEntry, 0, len(scorableAgents)*len(canonicalDimensions))
	finalCompliance := make([]contracts.ComplianceEntry, 0, len(scorableAgents))
	pass2ScoresByAgent := make(map[contracts.AgentID][]contracts.ScoreEntry, len(scorableAgents))
	completedAgents := make([]contracts.AgentID, 0, len(scorableAgents))

	for _, agentInfo := range scorableAgents {
		outputPath, err := in.IO.ResolveRunRelative(agentInfo.manifest.DiffPath)
		if err != nil {
			return fmt.Errorf("step60: resolve pass2 diff path for agent=%s: %w", agentInfo.agent, err)
		}
		judgeInput := judges.JudgeInput{
			RunID:      in.TaskPackage.RunID,
			Pass:       2,
			Agent:      agentInfo.agent,
			OutputPath: outputPath,
			RubricPath: in.RubricPath,
		}

		primaryOutput, err := scoreJudgeOutput(ctx, in.Primary, judgeInput, contracts.JudgeRolePrimary)
		if err != nil {
			return err
		}
		secondaryOutput, err := scoreJudgeOutput(ctx, in.Secondary, judgeInput, contracts.JudgeRoleSecondary)
		if err != nil {
			return err
		}

		primaryScores := normalizeScores(primaryOutput.Scores, in.RubricVersion, in.PromptVersion)
		secondaryScores := normalizeScores(secondaryOutput.Scores, in.RubricVersion, in.PromptVersion)
		primaryCompliance := normalizeCompliance(primaryOutput.Compliance, in.RubricVersion, in.PromptVersion)
		secondaryCompliance := normalizeCompliance(secondaryOutput.Compliance, in.RubricVersion, in.PromptVersion)

		scoreDisagreements := collectScoreDisagreements(primaryScores, secondaryScores)
		complianceDisagreements := collectComplianceDisagreements(primaryCompliance, secondaryCompliance)
		needArbiter := len(scoreDisagreements) > 0 || len(complianceDisagreements) > 0

		var arbiterScores map[contracts.Dimension]contracts.ScoreEntry
		var arbiterCompliance map[string]contracts.ComplianceEntry
		if needArbiter {
			arbiterOutput, err := scoreJudgeOutput(ctx, in.Arbiter, judgeInput, contracts.JudgeRoleArbiter)
			if err != nil {
				return err
			}
			arbiterScores = normalizeScores(arbiterOutput.Scores, in.RubricVersion, in.PromptVersion)
			arbiterCompliance = normalizeCompliance(arbiterOutput.Compliance, in.RubricVersion, in.PromptVersion)
		}

		agentScores, err := emitScores(paths.scoresRaw, paths.scoresFinal, agentInfo.agent, primaryScores, secondaryScores, arbiterScores, scoreDisagreements, resolvedAt)
		if err != nil {
			return err
		}
		agentCompliance, err := emitCompliance(paths.complianceRaw, paths.complianceFinal, agentInfo.agent, primaryCompliance, secondaryCompliance, arbiterCompliance, resolvedAt, in.RubricVersion, in.PromptVersion)
		if err != nil {
			return err
		}

		if len(agentScores) != len(canonicalDimensions) {
			return fmt.Errorf("step60: incomplete score set for agent=%s: got=%d want=%d", agentInfo.agent, len(agentScores), len(canonicalDimensions))
		}

		completedAgents = append(completedAgents, agentInfo.agent)
		finalScores = append(finalScores, agentScores...)
		finalCompliance = append(finalCompliance, agentCompliance...)
		pass2ScoresByAgent[agentInfo.agent] = agentScores
	}

	pairwiseEntries := make([]contracts.PairwiseEntry, 0, len(completedAgents))
	for _, agent := range completedAgents {
		pass1Total, err := resolvePass1Total(in.IO, agent, pass1ScoresByAgent[agent])
		if err != nil {
			return err
		}
		pass2Total, err := sumScores(pass2ScoresByAgent[agent])
		if err != nil {
			return err
		}
		entry := makePairwiseEntry(in, agent, pass1Total, pass2Total, resolvedAt)
		if err := internalio.AppendJSONL(paths.pairwise, entry); err != nil {
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

	scoresRawHash, err := hashRawScores(paths.scoresRaw)
	if err != nil {
		return err
	}
	complianceRawHash, err := hashRawCompliance(paths.complianceRaw)
	if err != nil {
		return err
	}
	scoresFinalHash, err := hashFinalScores(finalScores)
	if err != nil {
		return err
	}
	complianceFinalHash, err := hashFinalCompliance(finalCompliance)
	if err != nil {
		return err
	}
	pairwiseFinalHash, err := hashFinalPairwise(pairwiseEntries)
	if err != nil {
		return err
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
		return fmt.Errorf("step60: validate done marker: %w", err)
	}
	if err := internalio.WriteJSONAtomic(paths.done, marker); err != nil {
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
		done:            donePath,
		scoresRaw:       scoresRawPath,
		scoresFinal:     scoresFinalPath,
		complianceRaw:   complianceRawPath,
		complianceFinal: complianceFinalPath,
		pairwise:        pairwisePath,
	}, nil
}

func truncateStep60Artifacts(paths []string) error {
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("step60: truncate artifact %s: %w", filepath.Base(path), err)
		}
	}
	return nil
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

func scorableAgentIDs(agents []scorablePass2Agent) []contracts.AgentID {
	ids := make([]contracts.AgentID, 0, len(agents))
	for _, agent := range agents {
		ids = append(ids, agent.agent)
	}
	return ids
}

func loadScorablePass2Agents(runIO internalio.RunContext, agents []contracts.AgentID) ([]scorablePass2Agent, error) {
	out := make([]scorablePass2Agent, 0, len(agents))
	for _, agent := range agents {
		manifestPath, err := runIO.ManifestPath(2, agent)
		if err != nil {
			return nil, fmt.Errorf("step60: resolve pass2 manifest path for agent=%s: %w", agent, err)
		}
		if _, err := os.Stat(manifestPath); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("step60: stat pass2 manifest for agent=%s: %w", agent, err)
		}
		manifest, err := internalio.LoadScorableManifest(runIO, 2, agent)
		if errors.Is(err, internalio.ErrNotScorable) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("step60: load scorable pass2 manifest for agent=%s: %w", agent, err)
		}
		out = append(out, scorablePass2Agent{agent: agent, manifest: manifest})
	}
	return out, nil
}

func scoreJudgeOutput(ctx context.Context, judge judges.Judge, input judges.JudgeInput, role contracts.JudgeRole) (judges.JudgeOutput, error) {
	output, err := judge.ScoreOutput(ctx, input)
	if err != nil {
		return judges.JudgeOutput{}, fmt.Errorf("step60: %s judge call for agent=%s: %w", role, input.Agent, err)
	}
	if err := output.ValidateFor(input); err != nil {
		return judges.JudgeOutput{}, fmt.Errorf("step60: %s judge output validation for agent=%s: %w", role, input.Agent, err)
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

func collectScoreDisagreements(primary, secondary map[contracts.Dimension]contracts.ScoreEntry) map[contracts.Dimension]bool {
	disagreements := make(map[contracts.Dimension]bool)
	for _, dimension := range canonicalDimensions {
		if !scoreValuesEqual(primary[dimension], secondary[dimension]) {
			disagreements[dimension] = true
		}
	}
	return disagreements
}

func collectComplianceDisagreements(primary, secondary map[string]contracts.ComplianceEntry) map[string]bool {
	disagreements := make(map[string]bool)
	ruleIDs := unionRuleIDs(primary, secondary, nil)
	for _, ruleID := range ruleIDs {
		if complianceValue(primary, ruleID).Verdict != complianceValue(secondary, ruleID).Verdict {
			disagreements[ruleID] = true
		}
	}
	return disagreements
}

func emitScores(
	scoresRawPath string,
	scoresFinalPath string,
	agent contracts.AgentID,
	primary map[contracts.Dimension]contracts.ScoreEntry,
	secondary map[contracts.Dimension]contracts.ScoreEntry,
	arbiter map[contracts.Dimension]contracts.ScoreEntry,
	disagreements map[contracts.Dimension]bool,
	resolvedAt time.Time,
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
			return nil, fmt.Errorf("step60: hash primary score agent=%s dimension=%s: %w", agent, dimension, err)
		}
		secondaryHash, err := scoreOutputHash(secondaryScore)
		if err != nil {
			return nil, fmt.Errorf("step60: hash secondary score agent=%s dimension=%s: %w", agent, dimension, err)
		}
		if err := internalio.AppendJSONL(scoresRawPath, makeRawScoreEntry(primaryScore, contracts.JudgeRolePrimary, primaryHash, nil, nil)); err != nil {
			return nil, fmt.Errorf("step60: append primary raw score agent=%s dimension=%s: %w", agent, dimension, err)
		}
		if err := internalio.AppendJSONL(scoresRawPath, makeRawScoreEntry(secondaryScore, contracts.JudgeRoleSecondary, secondaryHash, nil, nil)); err != nil {
			return nil, fmt.Errorf("step60: append secondary raw score agent=%s dimension=%s: %w", agent, dimension, err)
		}

		finalScore := primaryScore
		finalScore.ResolvedAt = resolvedAt
		if disagreements[dimension] {
			arbiterScore, ok := arbiter[dimension]
			if !ok {
				return nil, fmt.Errorf("step60: arbiter score missing dimension=%s agent=%s", dimension, agent)
			}
			arbiterHash, err := scoreOutputHash(arbiterScore)
			if err != nil {
				return nil, fmt.Errorf("step60: hash arbiter score agent=%s dimension=%s: %w", agent, dimension, err)
			}
			if err := internalio.AppendJSONL(scoresRawPath, makeRawScoreEntry(
				arbiterScore,
				contracts.JudgeRoleArbiter,
				arbiterHash,
				&contracts.RawJudgeRef{Role: contracts.JudgeRolePrimary, Sha256: primaryHash},
				&contracts.RawJudgeRef{Role: contracts.JudgeRoleSecondary, Sha256: secondaryHash},
			)); err != nil {
				return nil, fmt.Errorf("step60: append arbiter raw score agent=%s dimension=%s: %w", agent, dimension, err)
			}
			finalScore = arbiterScore
			finalScore.VerdictPath = resolveScoreVerdictPath(primaryScore, secondaryScore, arbiterScore)
			finalScore.ResolvedAt = resolvedAt
		} else {
			finalScore.VerdictPath = contracts.VerdictPathAgreement
		}

		if err := internalio.AppendJSONL(scoresFinalPath, finalScore); err != nil {
			return nil, fmt.Errorf("step60: append final score agent=%s dimension=%s: %w", agent, dimension, err)
		}
		finalScores = append(finalScores, finalScore)
	}
	return finalScores, nil
}

func emitCompliance(
	complianceRawPath string,
	complianceFinalPath string,
	agent contracts.AgentID,
	primary map[string]contracts.ComplianceEntry,
	secondary map[string]contracts.ComplianceEntry,
	arbiter map[string]contracts.ComplianceEntry,
	resolvedAt time.Time,
	rubricVersion string,
	promptVersion string,
) ([]contracts.ComplianceEntry, error) {
	ruleIDs := unionRuleIDs(primary, secondary, arbiter)
	finalEntries := make([]contracts.ComplianceEntry, 0, len(ruleIDs))

	for _, ruleID := range ruleIDs {
		template := complianceTemplate(agent, ruleID, primary, secondary, arbiter, rubricVersion, promptVersion, resolvedAt)
		primaryActual, primaryOK := primary[ruleID]
		secondaryActual, secondaryOK := secondary[ruleID]
		arbiterActual, arbiterOK := arbiter[ruleID]

		primaryValue := template
		secondaryValue := template
		arbiterValue := template
		primaryValue.Verdict = contracts.ComplianceVerdictMissed
		secondaryValue.Verdict = contracts.ComplianceVerdictMissed
		arbiterValue.Verdict = contracts.ComplianceVerdictMissed

		primaryHash, err := complianceOutputHash(primaryValue)
		if err != nil {
			return nil, fmt.Errorf("step60: hash synthetic primary compliance agent=%s rule=%s: %w", agent, ruleID, err)
		}
		secondaryHash, err := complianceOutputHash(secondaryValue)
		if err != nil {
			return nil, fmt.Errorf("step60: hash synthetic secondary compliance agent=%s rule=%s: %w", agent, ruleID, err)
		}

		if primaryOK {
			primaryValue = primaryActual
			primaryHash, err = complianceOutputHash(primaryActual)
			if err != nil {
				return nil, fmt.Errorf("step60: hash primary compliance agent=%s rule=%s: %w", agent, ruleID, err)
			}
			if err := internalio.AppendJSONL(complianceRawPath, makeRawComplianceEntry(primaryActual, contracts.JudgeRolePrimary, primaryHash, nil, nil)); err != nil {
				return nil, fmt.Errorf("step60: append primary raw compliance agent=%s rule=%s: %w", agent, ruleID, err)
			}
		}
		if secondaryOK {
			secondaryValue = secondaryActual
			secondaryHash, err = complianceOutputHash(secondaryActual)
			if err != nil {
				return nil, fmt.Errorf("step60: hash secondary compliance agent=%s rule=%s: %w", agent, ruleID, err)
			}
			if err := internalio.AppendJSONL(complianceRawPath, makeRawComplianceEntry(secondaryActual, contracts.JudgeRoleSecondary, secondaryHash, nil, nil)); err != nil {
				return nil, fmt.Errorf("step60: append secondary raw compliance agent=%s rule=%s: %w", agent, ruleID, err)
			}
		}
		if arbiterOK {
			arbiterValue = arbiterActual
			arbiterHash, err := complianceOutputHash(arbiterActual)
			if err != nil {
				return nil, fmt.Errorf("step60: hash arbiter compliance agent=%s rule=%s: %w", agent, ruleID, err)
			}
			if err := internalio.AppendJSONL(complianceRawPath, makeRawComplianceEntry(
				arbiterActual,
				contracts.JudgeRoleArbiter,
				arbiterHash,
				&contracts.RawJudgeRef{Role: contracts.JudgeRolePrimary, Sha256: primaryHash},
				&contracts.RawJudgeRef{Role: contracts.JudgeRoleSecondary, Sha256: secondaryHash},
			)); err != nil {
				return nil, fmt.Errorf("step60: append arbiter raw compliance agent=%s rule=%s: %w", agent, ruleID, err)
			}
		}

		finalEntry := primaryValue
		if primaryValue.Verdict == secondaryValue.Verdict {
			finalEntry.VerdictPath = contracts.VerdictPathAgreement
		} else {
			finalEntry = arbiterValue
			finalEntry.VerdictPath = resolveComplianceVerdictPath(primaryValue, secondaryValue, arbiterValue)
		}
		finalEntry.ResolvedAt = resolvedAt
		if err := internalio.AppendJSONL(complianceFinalPath, finalEntry); err != nil {
			return nil, fmt.Errorf("step60: append final compliance agent=%s rule=%s: %w", agent, ruleID, err)
		}
		finalEntries = append(finalEntries, finalEntry)
	}

	return finalEntries, nil
}

func unionRuleIDs(primary, secondary, arbiter map[string]contracts.ComplianceEntry) []string {
	seen := make(map[string]struct{}, len(primary)+len(secondary)+len(arbiter))
	ruleIDs := make([]string, 0, len(seen))
	for ruleID := range primary {
		if _, ok := seen[ruleID]; ok {
			continue
		}
		seen[ruleID] = struct{}{}
		ruleIDs = append(ruleIDs, ruleID)
	}
	for ruleID := range secondary {
		if _, ok := seen[ruleID]; ok {
			continue
		}
		seen[ruleID] = struct{}{}
		ruleIDs = append(ruleIDs, ruleID)
	}
	for ruleID := range arbiter {
		if _, ok := seen[ruleID]; ok {
			continue
		}
		seen[ruleID] = struct{}{}
		ruleIDs = append(ruleIDs, ruleID)
	}
	sort.Strings(ruleIDs)
	return ruleIDs
}

func complianceTemplate(
	agent contracts.AgentID,
	ruleID string,
	primary map[string]contracts.ComplianceEntry,
	secondary map[string]contracts.ComplianceEntry,
	arbiter map[string]contracts.ComplianceEntry,
	rubricVersion string,
	promptVersion string,
	resolvedAt time.Time,
) contracts.ComplianceEntry {
	for _, source := range []map[string]contracts.ComplianceEntry{primary, secondary, arbiter} {
		if entry, ok := source[ruleID]; ok {
			entry.ResolvedAt = resolvedAt
			entry.VerdictPath = contracts.VerdictPathSingle
			return entry
		}
	}
	return contracts.ComplianceEntry{
		SchemaVersion: "1",
		Agent:         agent,
		RuleID:        ruleID,
		VerdictPath:   contracts.VerdictPathSingle,
		RubricVersion: rubricVersion,
		PromptVersion: promptVersion,
		ResolvedAt:    resolvedAt,
	}
}

func complianceValue(entries map[string]contracts.ComplianceEntry, ruleID string) contracts.ComplianceEntry {
	if entry, ok := entries[ruleID]; ok {
		return entry
	}
	return contracts.ComplianceEntry{Verdict: contracts.ComplianceVerdictMissed}
}

func resolveScoreVerdictPath(primary, secondary, arbiter contracts.ScoreEntry) contracts.VerdictPath {
	if scoreValuesEqual(primary, secondary) {
		return contracts.VerdictPathAgreement
	}
	if scoreValuesEqual(arbiter, primary) || scoreValuesEqual(arbiter, secondary) {
		return contracts.VerdictPathArbitrated
	}
	return contracts.VerdictPathArbiterOverruled
}

func resolveComplianceVerdictPath(primary, secondary, arbiter contracts.ComplianceEntry) contracts.VerdictPath {
	if primary.Verdict == secondary.Verdict {
		return contracts.VerdictPathAgreement
	}
	if arbiter.Verdict == primary.Verdict || arbiter.Verdict == secondary.Verdict {
		return contracts.VerdictPathArbitrated
	}
	return contracts.VerdictPathArbiterOverruled
}

func scoreValuesEqual(a, b contracts.ScoreEntry) bool {
	return a.Score == b.Score &&
		a.Reasons == b.Reasons &&
		overflowRefEqual(a.ReasonsOverflowRef, b.ReasonsOverflowRef)
}

func overflowRefEqual(a, b *contracts.OverflowRef) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		return a.Path == b.Path && a.Sha256 == b.Sha256
	}
}

func makeRawScoreEntry(
	score contracts.ScoreEntry,
	role contracts.JudgeRole,
	outputHash string,
	primaryRef *contracts.RawJudgeRef,
	secondaryRef *contracts.RawJudgeRef,
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
		ResolvedAt:         score.ResolvedAt,
	}
}

func makeRawComplianceEntry(
	entry contracts.ComplianceEntry,
	role contracts.JudgeRole,
	outputHash string,
	primaryRef *contracts.RawJudgeRef,
	secondaryRef *contracts.RawJudgeRef,
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
		ResolvedAt:           entry.ResolvedAt,
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

func resolvePass1Total(runIO internalio.RunContext, agent contracts.AgentID, scores []contracts.ScoreEntry) (int, error) {
	if len(scores) == len(canonicalDimensions) {
		return sumScores(scores)
	}
	path, err := runIO.ResolveRunRelative("30/scores-A.jsonl")
	if err != nil {
		return 0, fmt.Errorf("step60: resolve pass1 scores path for agent=%s: %w", agent, err)
	}
	return 0, fmt.Errorf("%w: agent=%s file=%s got=%d want=%d", ErrPass1ScoresIncomplete, agent, path, len(scores), len(canonicalDimensions))
}

func sumScores(scores []contracts.ScoreEntry) (int, error) {
	if len(scores) != len(canonicalDimensions) {
		return 0, fmt.Errorf("step60: score sum requires %d scores, got %d", len(canonicalDimensions), len(scores))
	}
	var total int
	for _, score := range scores {
		total += score.Score
	}
	return total, nil
}

func makePairwiseEntry(in Input, agent contracts.AgentID, pass1Total, pass2Total int, resolvedAt time.Time) contracts.PairwiseEntry {
	winner := contracts.PairwiseWinnerTie
	switch {
	case pass2Total > pass1Total:
		winner = contracts.PairwiseWinnerB
	case pass1Total > pass2Total:
		winner = contracts.PairwiseWinnerA
	}

	margin := contracts.PairwiseMarginSlight
	delta := math.Abs(float64(pass2Total - pass1Total))
	switch {
	case delta > 50:
		margin = contracts.PairwiseMarginDecisive
	case delta > 15:
		margin = contracts.PairwiseMarginClear
	}

	return contracts.PairwiseEntry{
		SchemaVersion: "1",
		RunID:         in.TaskPackage.RunID,
		AgentA:        agent,
		AgentB:        agent,
		Winner:        winner,
		Margin:        margin,
		Justification: fmt.Sprintf("pass1_total=%d pass2_total=%d", pass1Total, pass2Total),
		// pairwise is derived from step30/step60 aggregates, not a multi-judge verdict.
		VerdictPath:   contracts.VerdictPathSingle,
		RubricVersion: in.RubricVersion,
		PromptVersion: in.PromptVersion,
		ResolvedAt:    resolvedAt,
	}
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
	collapsed := internalio.CollapseByKey(entries, func(entry contracts.PairwiseEntry) pairwiseKey {
		return pairwiseKey{AgentA: entry.AgentA, AgentB: entry.AgentB}
	})
	sort.Slice(collapsed, func(i, j int) bool {
		if collapsed[i].AgentA != collapsed[j].AgentA {
			return collapsed[i].AgentA < collapsed[j].AgentA
		}
		return collapsed[i].AgentB < collapsed[j].AgentB
	})
	return hashCanonicalRows(collapsed)
}

func hashRawScores(path string) (string, error) {
	rows, err := internalio.ReadJSONL[contracts.RawScoreEntry](path)
	if err != nil {
		return "", fmt.Errorf("step60: read raw scores for hashing: %w", err)
	}
	reduced := reduceRawScores(rows)
	sort.Slice(reduced, func(i, j int) bool {
		if reduced[i].Agent != reduced[j].Agent {
			return reduced[i].Agent < reduced[j].Agent
		}
		if reduced[i].JudgeRole != reduced[j].JudgeRole {
			return reduced[i].JudgeRole < reduced[j].JudgeRole
		}
		return reduced[i].Dimension < reduced[j].Dimension
	})
	return hashCanonicalRows(reduced)
}

func hashRawCompliance(path string) (string, error) {
	rows, err := internalio.ReadJSONL[contracts.RawComplianceEntry](path)
	if err != nil {
		return "", fmt.Errorf("step60: read raw compliance for hashing: %w", err)
	}
	reduced := reduceRawCompliance(rows)
	sort.Slice(reduced, func(i, j int) bool {
		if reduced[i].Agent != reduced[j].Agent {
			return reduced[i].Agent < reduced[j].Agent
		}
		if reduced[i].JudgeRole != reduced[j].JudgeRole {
			return reduced[i].JudgeRole < reduced[j].JudgeRole
		}
		return reduced[i].RuleID < reduced[j].RuleID
	})
	return hashCanonicalRows(reduced)
}

func reduceRawScores(rows []contracts.RawScoreEntry) []contracts.RawScoreEntry {
	primarySecondary := make([]contracts.RawScoreEntry, 0, len(rows))
	arbiterRows := make([]contracts.RawScoreEntry, 0, len(rows))
	for _, row := range rows {
		if row.JudgeRole == contracts.JudgeRoleArbiter {
			arbiterRows = append(arbiterRows, row)
			continue
		}
		primarySecondary = append(primarySecondary, row)
	}

	collapsed := internalio.CollapseByKey(primarySecondary, func(row contracts.RawScoreEntry) rawScoreKey {
		return rawScoreKey{Agent: row.Agent, JudgeRole: row.JudgeRole, Dimension: row.Dimension}
	})
	latestPrimary := make(map[scoreKey]contracts.RawScoreEntry, len(collapsed))
	latestSecondary := make(map[scoreKey]contracts.RawScoreEntry, len(collapsed))
	for _, row := range collapsed {
		key := scoreKey{Agent: row.Agent, Dimension: row.Dimension}
		switch row.JudgeRole {
		case contracts.JudgeRolePrimary:
			latestPrimary[key] = row
		case contracts.JudgeRoleSecondary:
			latestSecondary[key] = row
		}
	}

	validArbiters := make([]contracts.RawScoreEntry, 0, len(arbiterRows))
	for _, row := range arbiterRows {
		key := scoreKey{Agent: row.Agent, Dimension: row.Dimension}
		primaryRow, ok := latestPrimary[key]
		if !ok || row.PrimaryRef == nil || row.PrimaryRef.Sha256 != primaryRow.OutputSha256 {
			continue
		}
		secondaryRow, ok := latestSecondary[key]
		if !ok || row.SecondaryRef == nil || row.SecondaryRef.Sha256 != secondaryRow.OutputSha256 {
			continue
		}
		validArbiters = append(validArbiters, row)
	}
	collapsedArbiters := internalio.CollapseByKey(validArbiters, func(row contracts.RawScoreEntry) rawScoreKey {
		return rawScoreKey{Agent: row.Agent, JudgeRole: row.JudgeRole, Dimension: row.Dimension}
	})
	return append(collapsed, collapsedArbiters...)
}

func reduceRawCompliance(rows []contracts.RawComplianceEntry) []contracts.RawComplianceEntry {
	primarySecondary := make([]contracts.RawComplianceEntry, 0, len(rows))
	arbiterRows := make([]contracts.RawComplianceEntry, 0, len(rows))
	for _, row := range rows {
		if row.JudgeRole == contracts.JudgeRoleArbiter {
			arbiterRows = append(arbiterRows, row)
			continue
		}
		primarySecondary = append(primarySecondary, row)
	}

	collapsed := internalio.CollapseByKey(primarySecondary, func(row contracts.RawComplianceEntry) rawComplianceKey {
		return rawComplianceKey{Agent: row.Agent, JudgeRole: row.JudgeRole, RuleID: row.RuleID}
	})
	latestPrimary := make(map[complianceKey]contracts.RawComplianceEntry, len(collapsed))
	latestSecondary := make(map[complianceKey]contracts.RawComplianceEntry, len(collapsed))
	for _, row := range collapsed {
		key := complianceKey{Agent: row.Agent, RuleID: row.RuleID}
		switch row.JudgeRole {
		case contracts.JudgeRolePrimary:
			latestPrimary[key] = row
		case contracts.JudgeRoleSecondary:
			latestSecondary[key] = row
		}
	}

	validArbiters := make([]contracts.RawComplianceEntry, 0, len(arbiterRows))
	for _, row := range arbiterRows {
		key := complianceKey{Agent: row.Agent, RuleID: row.RuleID}
		primaryRow, ok := latestPrimary[key]
		if !ok || row.PrimaryRef == nil || row.PrimaryRef.Sha256 != primaryRow.OutputSha256 {
			continue
		}
		secondaryRow, ok := latestSecondary[key]
		if !ok || row.SecondaryRef == nil || row.SecondaryRef.Sha256 != secondaryRow.OutputSha256 {
			continue
		}
		validArbiters = append(validArbiters, row)
	}
	collapsedArbiters := internalio.CollapseByKey(validArbiters, func(row contracts.RawComplianceEntry) rawComplianceKey {
		return rawComplianceKey{Agent: row.Agent, JudgeRole: row.JudgeRole, RuleID: row.RuleID}
	})
	return append(collapsed, collapsedArbiters...)
}

func hashCanonicalRows[T any](records []T) (string, error) {
	hasher := sha256.New()
	for i, record := range records {
		if i > 0 {
			if _, err := hasher.Write([]byte{0}); err != nil {
				return "", err
			}
		}
		payload, err := contracts.CanonicalMarshal(record)
		if err != nil {
			return "", err
		}
		if _, err := hasher.Write(payload); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
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
