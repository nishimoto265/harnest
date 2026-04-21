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
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/judges"
)

const (
	defaultRubricVersion = "default"
	defaultPromptVersion = "phase0-stub"
)

var dimensions = []contracts.Dimension{
	contracts.DimensionFidelity,
	contracts.DimensionCorrectness,
	contracts.DimensionMaintainability,
	contracts.DimensionDiscipline,
	contracts.DimensionCommunication,
}

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

type paths struct {
	scoresRaw       string
	scoresFinal     string
	complianceRaw   string
	complianceFinal string
	pairwiseFinal   string
	doneMarker      string
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

type agentResult struct {
	finalScores     []contracts.ScoreEntry
	finalCompliance []contracts.ComplianceEntry
}

func Run(ctx context.Context, in Input) error {
	normalized, err := normalizeInput(in)
	if err != nil {
		return err
	}

	stepPaths, err := resolvePaths(normalized.IO)
	if err != nil {
		return err
	}
	if exists(stepPaths.doneMarker) {
		return nil
	}

	resolvedAt := normalized.Now().UTC()
	agents := deriveAgents(normalized)
	pass1Scores, err := loadPass1ScoreMap(normalized.IO)
	if err != nil {
		return err
	}

	finalScores := make([]contracts.ScoreEntry, 0, len(agents)*len(dimensions))
	finalCompliance := make([]contracts.ComplianceEntry, 0, len(agents))
	pairwise := make([]contracts.PairwiseEntry, 0, len(agents))
	finalScoreMap := make(map[contracts.AgentID][]contracts.ScoreEntry, len(agents))
	completedAgents := make([]contracts.AgentID, 0, len(agents))

	for _, agent := range agents {
		if err := ctx.Err(); err != nil {
			return err
		}

		pass2Manifest, err := internalio.LoadScorableManifest(normalized.IO, 2, agent)
		if err != nil {
			if errors.Is(err, internalio.ErrNotScorable) || os.IsNotExist(err) {
				continue
			}
			return err
		}

		agentResult, err := scorePass2Agent(ctx, normalized, stepPaths, agent, pass2Manifest, resolvedAt)
		if err != nil {
			return err
		}

		finalScores = append(finalScores, agentResult.finalScores...)
		finalCompliance = append(finalCompliance, agentResult.finalCompliance...)
		finalScoreMap[agent] = append([]contracts.ScoreEntry(nil), agentResult.finalScores...)
		if len(agentResult.finalScores) == len(dimensions) {
			completedAgents = append(completedAgents, agent)
		}
	}

	for _, agent := range completedAgents {
		entry, err := buildPairwiseEntry(ctx, normalized, agent, pass1Scores[agent], finalScoreMap[agent], resolvedAt)
		if err != nil {
			return err
		}
		if err := internalio.AppendJSONL(stepPaths.pairwiseFinal, *entry); err != nil {
			return err
		}
		pairwise = append(pairwise, *entry)
	}

	if err := writeDoneMarker(stepPaths, completedAgents, finalCompliance, pairwise, resolvedAt); err != nil {
		return err
	}
	return nil
}

func normalizeInput(in Input) (Input, error) {
	if in.TaskPackage == nil {
		return Input{}, errors.New("step60_scorepairwise: task package is required")
	}
	if err := in.TaskPackage.Validate(); err != nil {
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
	if in.Now == nil {
		in.Now = time.Now
	}
	if in.RubricVersion == "" {
		in.RubricVersion = defaultRubricVersion
	}
	if in.PromptVersion == "" {
		in.PromptVersion = defaultPromptVersion
	}
	if in.RubricPath == "" {
		in.RubricPath = filepath.Join(in.IO.RunDir(), "rubrics", "default.md")
	}
	if err := contracts.EnsureCleanAbsolutePath(in.RubricPath); err != nil {
		return Input{}, err
	}
	return in, nil
}

func deriveAgents(in Input) []contracts.AgentID {
	if len(in.ScorableAgents) == 0 {
		seen := make(map[contracts.AgentID]struct{}, len(in.TaskPackage.Worktrees))
		agents := make([]contracts.AgentID, 0, len(in.TaskPackage.Worktrees))
		for _, worktree := range in.TaskPackage.Worktrees {
			if worktree.Pass != 2 {
				continue
			}
			if _, ok := seen[worktree.Agent]; ok {
				continue
			}
			seen[worktree.Agent] = struct{}{}
			agents = append(agents, worktree.Agent)
		}
		sortAgents(agents)
		return agents
	}
	agents := append([]contracts.AgentID(nil), in.ScorableAgents...)
	sortAgents(agents)
	return agents
}

func sortAgents(agents []contracts.AgentID) {
	sort.Slice(agents, func(i, j int) bool {
		return string(agents[i]) < string(agents[j])
	})
}

func resolvePaths(runCtx internalio.RunContext) (paths, error) {
	scoresRaw, err := runCtx.ResolveRunRelative("60/scores-B-raw.jsonl")
	if err != nil {
		return paths{}, err
	}
	scoresFinal, err := runCtx.ResolveRunRelative("60/scores-B.jsonl")
	if err != nil {
		return paths{}, err
	}
	complianceRaw, err := runCtx.ResolveRunRelative("60/compliance-B-raw.jsonl")
	if err != nil {
		return paths{}, err
	}
	complianceFinal, err := runCtx.ResolveRunRelative("60/compliance-B.jsonl")
	if err != nil {
		return paths{}, err
	}
	pairwiseFinal, err := runCtx.ResolveRunRelative("60/pairwise.jsonl")
	if err != nil {
		return paths{}, err
	}
	doneMarker, err := runCtx.ResolveRunRelative("60/done.marker")
	if err != nil {
		return paths{}, err
	}
	return paths{
		scoresRaw:       scoresRaw,
		scoresFinal:     scoresFinal,
		complianceRaw:   complianceRaw,
		complianceFinal: complianceFinal,
		pairwiseFinal:   pairwiseFinal,
		doneMarker:      doneMarker,
	}, nil
}

func scorePass2Agent(ctx context.Context, in Input, stepPaths paths, agent contracts.AgentID, manifest *contracts.ManifestSuccess, resolvedAt time.Time) (agentResult, error) {
	input, err := judgeInput(in, 2, agent, manifest)
	if err != nil {
		return agentResult{}, err
	}
	primary, err := in.Primary.ScoreOutput(ctx, input)
	if err != nil {
		return agentResult{}, err
	}
	secondary, err := in.Secondary.ScoreOutput(ctx, input)
	if err != nil {
		return agentResult{}, err
	}

	primaryScores, err := scoreMap(primary.Scores)
	if err != nil {
		return agentResult{}, err
	}
	secondaryScores, err := scoreMap(secondary.Scores)
	if err != nil {
		return agentResult{}, err
	}
	primaryCompliance, primaryComplianceOrder, err := complianceMap(primary.Compliance)
	if err != nil {
		return agentResult{}, err
	}
	secondaryCompliance, _, err := complianceMap(secondary.Compliance)
	if err != nil {
		return agentResult{}, err
	}

	needsArbiter := scoreDisagreement(primaryScores, secondaryScores) || complianceDisagreement(primaryCompliance, secondaryCompliance, primaryComplianceOrder)

	var arbiter judges.JudgeOutput
	var arbiterScores map[contracts.Dimension]contracts.ScoreEntry
	var arbiterCompliance map[string]contracts.ComplianceEntry
	if needsArbiter {
		arbiter, err = in.Arbiter.ScoreOutput(ctx, input)
		if err != nil {
			return agentResult{}, err
		}
		arbiterScores, err = scoreMap(arbiter.Scores)
		if err != nil {
			return agentResult{}, err
		}
		arbiterCompliance, _, err = complianceMap(arbiter.Compliance)
		if err != nil {
			return agentResult{}, err
		}
	}

	result := agentResult{
		finalScores:     make([]contracts.ScoreEntry, 0, len(dimensions)),
		finalCompliance: make([]contracts.ComplianceEntry, 0, len(primaryComplianceOrder)),
	}
	rawScoreHashes := make(map[contracts.Dimension]map[contracts.JudgeRole]string, len(dimensions))
	for _, dimension := range dimensions {
		rawScoreHashes[dimension] = make(map[contracts.JudgeRole]string, 3)
		primaryScore := primaryScores[dimension]
		secondaryScore := secondaryScores[dimension]
		primaryHash, err := outputHash(primaryScore)
		if err != nil {
			return agentResult{}, err
		}
		secondaryHash, err := outputHash(secondaryScore)
		if err != nil {
			return agentResult{}, err
		}
		rawScoreHashes[dimension][contracts.JudgeRolePrimary] = primaryHash
		rawScoreHashes[dimension][contracts.JudgeRoleSecondary] = secondaryHash

		if err := internalio.AppendJSONL(stepPaths.scoresRaw, buildRawScoreEntry(primaryScore, contracts.JudgeRolePrimary, primaryHash, in, resolvedAt, nil, nil)); err != nil {
			return agentResult{}, err
		}
		if err := internalio.AppendJSONL(stepPaths.scoresRaw, buildRawScoreEntry(secondaryScore, contracts.JudgeRoleSecondary, secondaryHash, in, resolvedAt, nil, nil)); err != nil {
			return agentResult{}, err
		}

		finalScore := buildFinalScoreEntry(primaryScore, contracts.VerdictPathAgreement, in, resolvedAt)
		if primaryScore.Score != secondaryScore.Score || primaryScore.Reasons != secondaryScore.Reasons {
			arbiterScore := arbiterScores[dimension]
			arbiterHash, err := outputHash(arbiterScore)
			if err != nil {
				return agentResult{}, err
			}
			rawScoreHashes[dimension][contracts.JudgeRoleArbiter] = arbiterHash
			if err := internalio.AppendJSONL(stepPaths.scoresRaw, buildRawScoreEntry(
				arbiterScore,
				contracts.JudgeRoleArbiter,
				arbiterHash,
				in,
				resolvedAt,
				&contracts.RawJudgeRef{Role: contracts.JudgeRolePrimary, Sha256: primaryHash},
				&contracts.RawJudgeRef{Role: contracts.JudgeRoleSecondary, Sha256: secondaryHash},
			)); err != nil {
				return agentResult{}, err
			}
			finalScore = buildFinalScoreEntry(arbiterScore, contracts.VerdictPathArbitrated, in, resolvedAt)
		}

		if err := internalio.AppendJSONL(stepPaths.scoresFinal, finalScore); err != nil {
			return agentResult{}, err
		}
		result.finalScores = append(result.finalScores, finalScore)
	}

	for _, ruleID := range primaryComplianceOrder {
		primaryRow := primaryCompliance[ruleID]
		secondaryRow, ok := secondaryCompliance[ruleID]
		if !ok {
			return agentResult{}, fmt.Errorf("step60_scorepairwise: secondary compliance missing rule_id=%s agent=%s", ruleID, agent)
		}

		primaryHash, err := outputHash(primaryRow)
		if err != nil {
			return agentResult{}, err
		}
		secondaryHash, err := outputHash(secondaryRow)
		if err != nil {
			return agentResult{}, err
		}
		if err := internalio.AppendJSONL(stepPaths.complianceRaw, buildRawComplianceEntry(primaryRow, contracts.JudgeRolePrimary, primaryHash, in, resolvedAt, nil, nil)); err != nil {
			return agentResult{}, err
		}
		if err := internalio.AppendJSONL(stepPaths.complianceRaw, buildRawComplianceEntry(secondaryRow, contracts.JudgeRoleSecondary, secondaryHash, in, resolvedAt, nil, nil)); err != nil {
			return agentResult{}, err
		}

		finalRow := buildFinalComplianceEntry(primaryRow, contracts.VerdictPathAgreement, in, resolvedAt)
		if primaryRow.Verdict != secondaryRow.Verdict {
			arbiterRow, ok := arbiterCompliance[ruleID]
			if !ok {
				return agentResult{}, fmt.Errorf("step60_scorepairwise: arbiter compliance missing rule_id=%s agent=%s", ruleID, agent)
			}
			arbiterHash, err := outputHash(arbiterRow)
			if err != nil {
				return agentResult{}, err
			}
			if err := internalio.AppendJSONL(stepPaths.complianceRaw, buildRawComplianceEntry(
				arbiterRow,
				contracts.JudgeRoleArbiter,
				arbiterHash,
				in,
				resolvedAt,
				&contracts.RawJudgeRef{Role: contracts.JudgeRolePrimary, Sha256: primaryHash},
				&contracts.RawJudgeRef{Role: contracts.JudgeRoleSecondary, Sha256: secondaryHash},
			)); err != nil {
				return agentResult{}, err
			}
			finalRow = buildFinalComplianceEntry(arbiterRow, contracts.VerdictPathArbitrated, in, resolvedAt)
		}

		if err := internalio.AppendJSONL(stepPaths.complianceFinal, finalRow); err != nil {
			return agentResult{}, err
		}
		result.finalCompliance = append(result.finalCompliance, finalRow)
	}

	return result, nil
}

func judgeInput(in Input, pass int, agent contracts.AgentID, manifest *contracts.ManifestSuccess) (judges.JudgeInput, error) {
	outputPath, err := in.IO.ResolveRunRelative(manifest.DiffPath)
	if err != nil {
		return judges.JudgeInput{}, err
	}
	input := judges.JudgeInput{
		RunID:      in.TaskPackage.RunID,
		Pass:       pass,
		Agent:      agent,
		OutputPath: outputPath,
		RubricPath: in.RubricPath,
	}
	if err := input.Validate(); err != nil {
		return judges.JudgeInput{}, err
	}
	return input, nil
}

func scoreMap(scores []contracts.ScoreEntry) (map[contracts.Dimension]contracts.ScoreEntry, error) {
	indexed := make(map[contracts.Dimension]contracts.ScoreEntry, len(scores))
	for _, score := range scores {
		if _, ok := indexed[score.Dimension]; ok {
			return nil, fmt.Errorf("step60_scorepairwise: duplicate score dimension=%s", score.Dimension)
		}
		indexed[score.Dimension] = score
	}
	for _, dimension := range dimensions {
		if _, ok := indexed[dimension]; !ok {
			return nil, fmt.Errorf("step60_scorepairwise: missing score dimension=%s", dimension)
		}
	}
	return indexed, nil
}

func complianceMap(rows []contracts.ComplianceEntry) (map[string]contracts.ComplianceEntry, []string, error) {
	indexed := make(map[string]contracts.ComplianceEntry, len(rows))
	order := make([]string, 0, len(rows))
	for _, row := range rows {
		if _, ok := indexed[row.RuleID]; ok {
			return nil, nil, fmt.Errorf("step60_scorepairwise: duplicate compliance rule_id=%s", row.RuleID)
		}
		indexed[row.RuleID] = row
		order = append(order, row.RuleID)
	}
	return indexed, order, nil
}

func scoreDisagreement(primary, secondary map[contracts.Dimension]contracts.ScoreEntry) bool {
	for _, dimension := range dimensions {
		if primary[dimension].Score != secondary[dimension].Score || primary[dimension].Reasons != secondary[dimension].Reasons {
			return true
		}
	}
	return false
}

func complianceDisagreement(primary, secondary map[string]contracts.ComplianceEntry, order []string) bool {
	for _, ruleID := range order {
		secondaryRow, ok := secondary[ruleID]
		if !ok || primary[ruleID].Verdict != secondaryRow.Verdict {
			return true
		}
	}
	return false
}

func buildRawScoreEntry(
	score contracts.ScoreEntry,
	role contracts.JudgeRole,
	outputSHA string,
	in Input,
	resolvedAt time.Time,
	primaryRef *contracts.RawJudgeRef,
	secondaryRef *contracts.RawJudgeRef,
) contracts.RawScoreEntry {
	return contracts.RawScoreEntry{
		SchemaVersion: score.SchemaVersion,
		RunID:         score.RunID,
		Pass:          score.Pass,
		Agent:         score.Agent,
		JudgeRole:     role,
		Dimension:     score.Dimension,
		Score:         score.Score,
		Reasons:       score.Reasons,
		OutputSha256:  outputSHA,
		PrimaryRef:    primaryRef,
		SecondaryRef:  secondaryRef,
		RubricVersion: in.RubricVersion,
		PromptVersion: in.PromptVersion,
		ResolvedAt:    resolvedAt,
	}
}

func buildRawComplianceEntry(
	row contracts.ComplianceEntry,
	role contracts.JudgeRole,
	outputSHA string,
	in Input,
	resolvedAt time.Time,
	primaryRef *contracts.RawJudgeRef,
	secondaryRef *contracts.RawJudgeRef,
) contracts.RawComplianceEntry {
	return contracts.RawComplianceEntry{
		SchemaVersion: row.SchemaVersion,
		RunID:         row.RunID,
		Pass:          row.Pass,
		Agent:         row.Agent,
		JudgeRole:     role,
		RuleID:        row.RuleID,
		Verdict:       row.Verdict,
		Rationale:     row.Rationale,
		OutputSha256:  outputSHA,
		PrimaryRef:    primaryRef,
		SecondaryRef:  secondaryRef,
		RubricVersion: in.RubricVersion,
		PromptVersion: in.PromptVersion,
		ResolvedAt:    resolvedAt,
	}
}

func buildFinalScoreEntry(score contracts.ScoreEntry, verdictPath contracts.VerdictPath, in Input, resolvedAt time.Time) contracts.ScoreEntry {
	return contracts.ScoreEntry{
		SchemaVersion: score.SchemaVersion,
		RunID:         score.RunID,
		Pass:          score.Pass,
		Agent:         score.Agent,
		Dimension:     score.Dimension,
		Score:         score.Score,
		Reasons:       score.Reasons,
		VerdictPath:   verdictPath,
		RubricVersion: in.RubricVersion,
		PromptVersion: in.PromptVersion,
		ResolvedAt:    resolvedAt,
	}
}

func buildFinalComplianceEntry(row contracts.ComplianceEntry, verdictPath contracts.VerdictPath, in Input, resolvedAt time.Time) contracts.ComplianceEntry {
	return contracts.ComplianceEntry{
		SchemaVersion: row.SchemaVersion,
		RunID:         row.RunID,
		Pass:          row.Pass,
		Agent:         row.Agent,
		RuleID:        row.RuleID,
		Verdict:       row.Verdict,
		Rationale:     row.Rationale,
		VerdictPath:   verdictPath,
		RubricVersion: in.RubricVersion,
		PromptVersion: in.PromptVersion,
		ResolvedAt:    resolvedAt,
	}
}

func loadPass1ScoreMap(runCtx internalio.RunContext) (map[contracts.AgentID][]contracts.ScoreEntry, error) {
	pass1FinalPath, err := runCtx.ResolveRunRelative("30/scores-A.jsonl")
	if err != nil {
		return nil, err
	}
	rows, err := internalio.ReadJSONL[contracts.ScoreEntry](pass1FinalPath)
	if err != nil {
		return nil, err
	}
	rows = internalio.CollapseByKey(rows, func(row contracts.ScoreEntry) scoreKey {
		return scoreKey{Agent: row.Agent, Dimension: row.Dimension}
	})
	indexed := make(map[contracts.AgentID][]contracts.ScoreEntry, len(rows))
	for _, row := range rows {
		if row.Pass != 1 {
			continue
		}
		indexed[row.Agent] = append(indexed[row.Agent], row)
	}
	for agent := range indexed {
		sort.Slice(indexed[agent], func(i, j int) bool {
			return dimensionIndex(indexed[agent][i].Dimension) < dimensionIndex(indexed[agent][j].Dimension)
		})
	}
	return indexed, nil
}

func buildPairwiseEntry(
	ctx context.Context,
	in Input,
	agent contracts.AgentID,
	pass1Scores []contracts.ScoreEntry,
	pass2Scores []contracts.ScoreEntry,
	resolvedAt time.Time,
) (*contracts.PairwiseEntry, error) {
	if len(pass2Scores) != len(dimensions) {
		return nil, fmt.Errorf("step60_scorepairwise: pass2 scores incomplete for agent=%s", agent)
	}
	if len(pass1Scores) != len(dimensions) {
		manifest, err := internalio.LoadScorableManifest(in.IO, 1, agent)
		if err != nil {
			return nil, err
		}
		input, err := judgeInput(in, 1, agent, manifest)
		if err != nil {
			return nil, err
		}
		output, err := in.Primary.ScoreOutput(ctx, input)
		if err != nil {
			return nil, err
		}
		pass1Scores = append([]contracts.ScoreEntry(nil), output.Scores...)
		sort.Slice(pass1Scores, func(i, j int) bool {
			return dimensionIndex(pass1Scores[i].Dimension) < dimensionIndex(pass1Scores[j].Dimension)
		})
	}

	avgA := averageScore(pass1Scores)
	avgB := averageScore(pass2Scores)
	diff := avgB - avgA
	winner := contracts.PairwiseWinnerTie
	switch {
	case diff > 0:
		winner = contracts.PairwiseWinnerB
	case diff < 0:
		winner = contracts.PairwiseWinnerA
	}

	margin := contracts.PairwiseMarginSlight
	absDiff := diff
	if absDiff < 0 {
		absDiff = -absDiff
	}
	switch {
	case absDiff > 10:
		margin = contracts.PairwiseMarginDecisive
	case absDiff > 3:
		margin = contracts.PairwiseMarginClear
	}

	return &contracts.PairwiseEntry{
		SchemaVersion: "1",
		RunID:         in.TaskPackage.RunID,
		AgentA:        agent,
		AgentB:        agent,
		Winner:        winner,
		Margin:        margin,
		Justification: fmt.Sprintf("pass2 average %.1f versus pass1 average %.1f for %s.", avgB, avgA, agent),
		VerdictPath:   contracts.VerdictPathAgreement,
		RubricVersion: in.RubricVersion,
		PromptVersion: in.PromptVersion,
		ResolvedAt:    resolvedAt,
	}, nil
}

func averageScore(scores []contracts.ScoreEntry) float64 {
	if len(scores) == 0 {
		return 0
	}
	total := 0
	for _, score := range scores {
		total += score.Score
	}
	return float64(total) / float64(len(scores))
}

func writeDoneMarker(stepPaths paths, completedAgents []contracts.AgentID, finalCompliance []contracts.ComplianceEntry, pairwise []contracts.PairwiseEntry, resolvedAt time.Time) error {
	persistedScores, err := internalio.ReadJSONL[contracts.ScoreEntry](stepPaths.scoresFinal)
	if err != nil {
		return err
	}
	persistedCompliance, err := internalio.ReadJSONL[contracts.ComplianceEntry](stepPaths.complianceFinal)
	if err != nil {
		return err
	}
	persistedPairwise, err := internalio.ReadJSONL[contracts.PairwiseEntry](stepPaths.pairwiseFinal)
	if err != nil {
		return err
	}

	scoreHash, err := hashCanonicalRows(
		internalio.CollapseByKey(persistedScores, func(row contracts.ScoreEntry) scoreKey {
			return scoreKey{Agent: row.Agent, Dimension: row.Dimension}
		}),
	)
	if err != nil {
		return err
	}
	complianceHash, err := hashCanonicalRows(
		internalio.CollapseByKey(persistedCompliance, func(row contracts.ComplianceEntry) complianceKey {
			return complianceKey{Agent: row.Agent, RuleID: row.RuleID}
		}),
	)
	if err != nil {
		return err
	}
	pairwiseHash, err := hashCanonicalRows(
		internalio.CollapseByKey(persistedPairwise, func(row contracts.PairwiseEntry) pairwiseKey {
			return pairwiseKey{AgentA: row.AgentA, AgentB: row.AgentB}
		}),
	)
	if err != nil {
		return err
	}

	marker := contracts.Step60DoneMarker{
		CompletedAgents: append([]contracts.AgentID(nil), completedAgents...),
		Dimensions:      append([]contracts.Dimension(nil), dimensions...),
		ExpectedCounts: contracts.Step60ExpectedCounts{
			Scores:     int64(len(completedAgents) * len(dimensions)),
			Compliance: int64(len(finalCompliance)),
			Pairwise:   int64(len(completedAgents)),
		},
		ContentHashes: contracts.Step60DoneContentHashes{
			ScoresFinal:     scoreHash,
			ComplianceFinal: complianceHash,
			PairwiseFinal:   pairwiseHash,
		},
		RawHashes: contracts.StepDoneRawHashes{
			ScoresRaw:     hashFileBytes(stepPaths.scoresRaw),
			ComplianceRaw: hashFileBytes(stepPaths.complianceRaw),
		},
		ResolvedAt: resolvedAt,
	}
	if err := marker.Validate(); err != nil {
		return err
	}
	return internalio.WriteJSONAtomic(stepPaths.doneMarker, marker)
}

func outputHash(v any) (string, error) {
	data, err := contracts.CanonicalMarshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func hashFileBytes(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		data = nil
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func hashCanonicalRows[T any](rows []T) (string, error) {
	if len(rows) == 0 {
		rows = []T{}
	}
	data, err := contracts.CanonicalMarshal(rows)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func dimensionIndex(d contracts.Dimension) int {
	for i, current := range dimensions {
		if current == d {
			return i
		}
	}
	return len(dimensions)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
