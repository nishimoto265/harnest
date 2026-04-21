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

func Run(ctx context.Context, in Input) error {
	in, err := applyDefaults(in)
	if err != nil {
		return err
	}

	donePath, err := in.IO.ResolveRunRelative("60/done.marker")
	if err != nil {
		return err
	}
	if _, err := os.Stat(donePath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
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

	scoresRawPath, err := in.IO.ResolveRunRelative("60/scores-B-raw.jsonl")
	if err != nil {
		return err
	}
	scoresFinalPath, err := in.IO.ResolveRunRelative("60/scores-B.jsonl")
	if err != nil {
		return err
	}
	complianceRawPath, err := in.IO.ResolveRunRelative("60/compliance-B-raw.jsonl")
	if err != nil {
		return err
	}
	complianceFinalPath, err := in.IO.ResolveRunRelative("60/compliance-B.jsonl")
	if err != nil {
		return err
	}
	pairwisePath, err := in.IO.ResolveRunRelative("60/pairwise.jsonl")
	if err != nil {
		return err
	}

	pass1ScoresByAgent, err := loadPass1Scores(in.IO)
	if err != nil {
		return err
	}

	finalScores := make([]contracts.ScoreEntry, 0, len(request.ScorableAgents)*len(canonicalDimensions))
	finalCompliance := make([]contracts.ComplianceEntry, 0, len(request.ScorableAgents))
	pass2ScoresByAgent := make(map[contracts.AgentID][]contracts.ScoreEntry, len(request.ScorableAgents))
	completedAgents := make([]contracts.AgentID, 0, len(request.ScorableAgents))

	for _, agent := range request.ScorableAgents {
		manifest, err := internalio.LoadScorableManifest(in.IO, 2, agent)
		if shouldSkipAgent(err) {
			continue
		}
		if err != nil {
			return err
		}

		outputPath, err := in.IO.ResolveRunRelative(manifest.DiffPath)
		if err != nil {
			return err
		}
		judgeInput := judges.JudgeInput{
			RunID:      in.TaskPackage.RunID,
			Pass:       2,
			Agent:      agent,
			OutputPath: outputPath,
			RubricPath: in.RubricPath,
		}

		primaryOutput, err := in.Primary.ScoreOutput(ctx, judgeInput)
		if err != nil {
			return err
		}
		secondaryOutput, err := in.Secondary.ScoreOutput(ctx, judgeInput)
		if err != nil {
			return err
		}

		primaryScores := normalizeScores(primaryOutput.Scores, in.RubricVersion, in.PromptVersion)
		secondaryScores := normalizeScores(secondaryOutput.Scores, in.RubricVersion, in.PromptVersion)
		primaryCompliance := normalizeCompliance(primaryOutput.Compliance, in.RubricVersion, in.PromptVersion)
		secondaryCompliance := normalizeCompliance(secondaryOutput.Compliance, in.RubricVersion, in.PromptVersion)

		needArbiter := scoreDisagreement(primaryScores, secondaryScores) || complianceDisagreement(primaryCompliance, secondaryCompliance)
		var arbiterScores map[contracts.Dimension]contracts.ScoreEntry
		var arbiterCompliance map[string]contracts.ComplianceEntry
		if needArbiter {
			arbiterOutput, err := in.Arbiter.ScoreOutput(ctx, judgeInput)
			if err != nil {
				return err
			}
			arbiterScores = normalizeScores(arbiterOutput.Scores, in.RubricVersion, in.PromptVersion)
			arbiterCompliance = normalizeCompliance(arbiterOutput.Compliance, in.RubricVersion, in.PromptVersion)
		}

		agentScores, err := emitScores(scoresRawPath, scoresFinalPath, agent, primaryScores, secondaryScores, arbiterScores)
		if err != nil {
			return err
		}
		agentCompliance, err := emitCompliance(complianceRawPath, complianceFinalPath, agent, primaryCompliance, secondaryCompliance, arbiterCompliance)
		if err != nil {
			return err
		}

		if len(agentScores) != len(canonicalDimensions) {
			return fmt.Errorf("step60: incomplete score set for agent=%s: got=%d want=%d", agent, len(agentScores), len(canonicalDimensions))
		}

		completedAgents = append(completedAgents, agent)
		finalScores = append(finalScores, agentScores...)
		finalCompliance = append(finalCompliance, agentCompliance...)
		pass2ScoresByAgent[agent] = agentScores
	}

	pairwiseEntries := make([]contracts.PairwiseEntry, 0, len(completedAgents))
	for _, agent := range completedAgents {
		pass1Average, err := resolvePass1Average(ctx, in, agent, pass1ScoresByAgent[agent])
		if err != nil {
			return err
		}
		pass2Average, err := averageScores(pass2ScoresByAgent[agent])
		if err != nil {
			return err
		}
		entry := makePairwiseEntry(in, agent, pass1Average, pass2Average)
		if err := internalio.AppendJSONL(pairwisePath, entry); err != nil {
			return err
		}
		pairwiseEntries = append(pairwiseEntries, entry)
	}

	if len(finalScores) != len(completedAgents)*len(canonicalDimensions) {
		return fmt.Errorf("step60: final score cardinality mismatch: scores=%d completed_agents=%d", len(finalScores), len(completedAgents))
	}
	if len(pairwiseEntries) != len(completedAgents) {
		return fmt.Errorf("step60: final pairwise cardinality mismatch: pairwise=%d completed_agents=%d", len(pairwiseEntries), len(completedAgents))
	}

	scoresRawHash, err := hashFileOrEmpty(scoresRawPath)
	if err != nil {
		return err
	}
	complianceRawHash, err := hashFileOrEmpty(complianceRawPath)
	if err != nil {
		return err
	}

	resolvedAt := in.Now().UTC()
	marker := contracts.Step60DoneMarker{
		CompletedAgents: completedAgents,
		Dimensions:      append([]contracts.Dimension(nil), canonicalDimensions...),
		ExpectedCounts: contracts.Step60ExpectedCounts{
			Scores:     int64(len(completedAgents) * len(canonicalDimensions)),
			Compliance: int64(len(finalCompliance)),
			Pairwise:   int64(len(pairwiseEntries)),
		},
		ContentHashes: contracts.Step60DoneContentHashes{
			ScoresFinal:     hashFinalScores(finalScores),
			ComplianceFinal: hashFinalCompliance(finalCompliance),
			PairwiseFinal:   hashFinalPairwise(pairwiseEntries),
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
	return internalio.WriteJSONAtomic(donePath, marker)
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
	return err != nil && (errors.Is(err, internalio.ErrNotScorable) || os.IsNotExist(err))
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

func scoreDisagreement(primary, secondary map[contracts.Dimension]contracts.ScoreEntry) bool {
	for _, dimension := range canonicalDimensions {
		p, ok := primary[dimension]
		if !ok {
			return true
		}
		s, ok := secondary[dimension]
		if !ok {
			return true
		}
		if p.Score != s.Score || p.Reasons != s.Reasons {
			return true
		}
	}
	return false
}

func complianceDisagreement(primary, secondary map[string]contracts.ComplianceEntry) bool {
	if len(primary) != len(secondary) {
		return true
	}
	for ruleID, p := range primary {
		s, ok := secondary[ruleID]
		if !ok {
			return true
		}
		if p.Verdict != s.Verdict {
			return true
		}
	}
	return false
}

func emitScores(
	scoresRawPath string,
	scoresFinalPath string,
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

		primaryHash := scoreOutputHash(primaryScore)
		secondaryHash := scoreOutputHash(secondaryScore)
		if err := internalio.AppendJSONL(scoresRawPath, makeRawScoreEntry(primaryScore, contracts.JudgeRolePrimary, primaryHash, nil, nil)); err != nil {
			return nil, err
		}
		if err := internalio.AppendJSONL(scoresRawPath, makeRawScoreEntry(secondaryScore, contracts.JudgeRoleSecondary, secondaryHash, nil, nil)); err != nil {
			return nil, err
		}

		finalScore := primaryScore
		if primaryScore.Score == secondaryScore.Score && primaryScore.Reasons == secondaryScore.Reasons {
			finalScore.VerdictPath = contracts.VerdictPathAgreement
		} else {
			arbiterScore, ok := arbiter[dimension]
			if !ok {
				return nil, fmt.Errorf("step60: arbiter score missing dimension=%s agent=%s", dimension, agent)
			}
			if err := internalio.AppendJSONL(scoresRawPath, makeRawScoreEntry(
				arbiterScore,
				contracts.JudgeRoleArbiter,
				scoreOutputHash(arbiterScore),
				&contracts.RawJudgeRef{Role: contracts.JudgeRolePrimary, Sha256: primaryHash},
				&contracts.RawJudgeRef{Role: contracts.JudgeRoleSecondary, Sha256: secondaryHash},
			)); err != nil {
				return nil, err
			}
			finalScore = arbiterScore
			finalScore.VerdictPath = contracts.VerdictPathArbitrated
		}

		if err := internalio.AppendJSONL(scoresFinalPath, finalScore); err != nil {
			return nil, err
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
) ([]contracts.ComplianceEntry, error) {
	ruleIDs := make([]string, 0, len(primary))
	for ruleID := range primary {
		ruleIDs = append(ruleIDs, ruleID)
	}
	sort.Strings(ruleIDs)

	finalEntries := make([]contracts.ComplianceEntry, 0, len(ruleIDs))
	for _, ruleID := range ruleIDs {
		primaryEntry, ok := primary[ruleID]
		if !ok {
			return nil, fmt.Errorf("step60: primary compliance missing rule=%s agent=%s", ruleID, agent)
		}
		secondaryEntry, ok := secondary[ruleID]
		if !ok {
			return nil, fmt.Errorf("step60: secondary compliance missing rule=%s agent=%s", ruleID, agent)
		}

		primaryHash := complianceOutputHash(primaryEntry)
		secondaryHash := complianceOutputHash(secondaryEntry)
		if err := internalio.AppendJSONL(complianceRawPath, makeRawComplianceEntry(primaryEntry, contracts.JudgeRolePrimary, primaryHash, nil, nil)); err != nil {
			return nil, err
		}
		if err := internalio.AppendJSONL(complianceRawPath, makeRawComplianceEntry(secondaryEntry, contracts.JudgeRoleSecondary, secondaryHash, nil, nil)); err != nil {
			return nil, err
		}

		finalEntry := primaryEntry
		if primaryEntry.Verdict == secondaryEntry.Verdict {
			finalEntry.VerdictPath = contracts.VerdictPathAgreement
		} else {
			arbiterEntry, ok := arbiter[ruleID]
			if !ok {
				return nil, fmt.Errorf("step60: arbiter compliance missing rule=%s agent=%s", ruleID, agent)
			}
			if err := internalio.AppendJSONL(complianceRawPath, makeRawComplianceEntry(
				arbiterEntry,
				contracts.JudgeRoleArbiter,
				complianceOutputHash(arbiterEntry),
				&contracts.RawJudgeRef{Role: contracts.JudgeRolePrimary, Sha256: primaryHash},
				&contracts.RawJudgeRef{Role: contracts.JudgeRoleSecondary, Sha256: secondaryHash},
			)); err != nil {
				return nil, err
			}
			finalEntry = arbiterEntry
			finalEntry.VerdictPath = contracts.VerdictPathArbitrated
		}

		if err := internalio.AppendJSONL(complianceFinalPath, finalEntry); err != nil {
			return nil, err
		}
		finalEntries = append(finalEntries, finalEntry)
	}
	return finalEntries, nil
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
		return nil, err
	}
	rows, err := internalio.ReadJSONL[contracts.ScoreEntry](path)
	if err != nil {
		return nil, err
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

func resolvePass1Average(ctx context.Context, in Input, agent contracts.AgentID, scores []contracts.ScoreEntry) (float64, error) {
	if len(scores) == len(canonicalDimensions) {
		return averageScores(scores)
	}

	outputPath, err := in.IO.ResolveRunRelative(filepath.Join("20-pass1", string(agent), "diff.patch"))
	if err != nil {
		return 0, err
	}
	output, err := in.Primary.ScoreOutput(ctx, judges.JudgeInput{
		RunID:      in.TaskPackage.RunID,
		Pass:       1,
		Agent:      agent,
		OutputPath: outputPath,
		RubricPath: in.RubricPath,
	})
	if err != nil {
		return 0, err
	}
	fallbackScores := make([]contracts.ScoreEntry, 0, len(output.Scores))
	for _, score := range output.Scores {
		score.RubricVersion = in.RubricVersion
		score.PromptVersion = in.PromptVersion
		fallbackScores = append(fallbackScores, score)
	}
	return averageScores(fallbackScores)
}

func averageScores(scores []contracts.ScoreEntry) (float64, error) {
	if len(scores) != len(canonicalDimensions) {
		return 0, fmt.Errorf("step60: average requires %d scores, got %d", len(canonicalDimensions), len(scores))
	}
	var total int
	for _, score := range scores {
		total += score.Score
	}
	return float64(total) / float64(len(scores)), nil
}

func makePairwiseEntry(in Input, agent contracts.AgentID, pass1Average, pass2Average float64) contracts.PairwiseEntry {
	winner := contracts.PairwiseWinnerTie
	switch {
	case pass2Average > pass1Average:
		winner = contracts.PairwiseWinnerB
	case pass1Average > pass2Average:
		winner = contracts.PairwiseWinnerA
	}

	margin := contracts.PairwiseMarginSlight
	delta := math.Abs(pass2Average - pass1Average)
	switch {
	case delta > 10:
		margin = contracts.PairwiseMarginDecisive
	case delta > 3:
		margin = contracts.PairwiseMarginClear
	}

	return contracts.PairwiseEntry{
		SchemaVersion: "1",
		RunID:         in.TaskPackage.RunID,
		AgentA:        agent,
		AgentB:        agent,
		Winner:        winner,
		Margin:        margin,
		Justification: fmt.Sprintf("pass1_avg=%.1f pass2_avg=%.1f", pass1Average, pass2Average),
		VerdictPath:   contracts.VerdictPathAgreement,
		RubricVersion: in.RubricVersion,
		PromptVersion: in.PromptVersion,
		ResolvedAt:    in.Now().UTC(),
	}
}

func scoreOutputHash(score contracts.ScoreEntry) string {
	return canonicalSHA256(score)
}

func complianceOutputHash(entry contracts.ComplianceEntry) string {
	return canonicalSHA256(entry)
}

func hashFileOrEmpty(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return sha256Hex(nil), nil
		}
		return "", err
	}
	return sha256Hex(data), nil
}

// Final-layer content hashes are defined as sha256(canonical-json(collapsed rows))
// rather than file-byte hashes so append/rewrite mechanics cannot affect the digest.
func hashFinalScores(entries []contracts.ScoreEntry) string {
	collapsed := internalio.CollapseByKey(entries, func(entry contracts.ScoreEntry) scoreKey {
		return scoreKey{Agent: entry.Agent, Dimension: entry.Dimension}
	})
	return canonicalSliceHash(collapsed)
}

func hashFinalCompliance(entries []contracts.ComplianceEntry) string {
	collapsed := internalio.CollapseByKey(entries, func(entry contracts.ComplianceEntry) complianceKey {
		return complianceKey{Agent: entry.Agent, RuleID: entry.RuleID}
	})
	return canonicalSliceHash(collapsed)
}

func hashFinalPairwise(entries []contracts.PairwiseEntry) string {
	collapsed := internalio.CollapseByKey(entries, func(entry contracts.PairwiseEntry) contracts.AgentID {
		return entry.AgentA
	})
	return canonicalSliceHash(collapsed)
}

func canonicalSliceHash[T any](entries []T) string {
	if entries == nil {
		entries = []T{}
	}
	return canonicalSHA256(entries)
}

func canonicalSHA256(v any) string {
	data, err := contracts.CanonicalMarshal(v)
	if err != nil {
		panic(fmt.Sprintf("step60: canonical marshal failed: %v", err))
	}
	return sha256Hex(data)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
