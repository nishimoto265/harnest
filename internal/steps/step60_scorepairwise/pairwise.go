package step60_scorepairwise

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/judges"
	"github.com/nishimoto265/auto-improve/internal/steps/scorecore"
)

func requireCompletePass1Scores(runIO internalio.RunContext, agent contracts.AgentID, scores []contracts.ScoreEntry) error {
	if len(scores) == len(canonicalDimensions) {
		return nil
	}
	path, err := runIO.ResolveRunRelative("30/scores-A.jsonl")
	if err != nil {
		return fmt.Errorf("step60: resolve pass1 scores path for agent=%s: %w", agent, err)
	}
	return fmt.Errorf("%w: agent=%s path=%s got=%d want=%d", ErrPass1ScoresIncomplete, agent, path, len(scores), len(canonicalDimensions))
}

func requireCompletePass2Scores(agent contracts.AgentID, scores []contracts.ScoreEntry) error {
	if len(scores) != len(canonicalDimensions) {
		return fmt.Errorf("step60: pass2 scores incomplete: agent=%s got=%d want=%d", agent, len(scores), len(canonicalDimensions))
	}
	return nil
}

func makePairwiseEntries(
	ctx context.Context,
	in Input,
	runs []scorableAgentRun,
	pass1ScoresByAgent map[contracts.AgentID][]contracts.ScoreEntry,
	pass2ScoresByAgent map[contracts.AgentID][]contracts.ScoreEntry,
	resolvedAt time.Time,
) ([]contracts.PairwiseEntry, error) {
	pairs := make([]judges.PairwisePair, 0, len(runs))
	for _, run := range runs {
		pass1Scores := pass1ScoresByAgent[run.Agent]
		if err := requireCompletePass1Scores(in.IO, run.Agent, pass1Scores); err != nil {
			return nil, err
		}
		pass2Scores := pass2ScoresByAgent[run.Agent]
		if err := requireCompletePass2Scores(run.Agent, pass2Scores); err != nil {
			return nil, err
		}
		pairs = append(pairs, judges.PairwisePair{
			Agent: run.Agent,
			A: judges.PairwiseCandidate{
				Label:      "pass1",
				OutputPath: run.Pass1OutputPath,
				Scores:     toPairwiseScores(pass1Scores),
			},
			B: judges.PairwiseCandidate{
				Label:      "pass2",
				OutputPath: run.JudgeInput.OutputPath,
				Scores:     toPairwiseScores(pass2Scores),
			},
		})
	}

	comparisons, err := runPairwiseComparisons(ctx, in, pairs)
	if err != nil {
		return nil, err
	}
	decision, err := in.PairwiseDecisionJudge.DecidePairwise(ctx, judges.PairwiseDecisionInput{
		RunID:       in.TaskPackage.RunID,
		Mode:        in.PairwiseMode,
		TaskPrompt:  in.TaskPackage.ReconstructedTaskPrompt,
		RubricPath:  in.RubricPath,
		Pairs:       pairs,
		Comparisons: comparisons,
	})
	if err != nil {
		return nil, fmt.Errorf("step60: pairwise decision: %w", err)
	}
	return pairwiseEntriesFromDecision(in, pairs, decision, comparisons, resolvedAt)
}

func runPairwiseComparisons(ctx context.Context, in Input, pairs []judges.PairwisePair) ([]judges.PairwiseComparison, error) {
	if in.PairwiseMode == judges.PairwiseModeSingle {
		return nil, nil
	}
	comparisons := make([]judges.PairwiseComparison, 0, len(pairs))
	if in.PairwiseMode == judges.PairwiseModeStrict {
		comparisons = make([]judges.PairwiseComparison, 0, len(pairs)*2)
	}
	for _, pair := range pairs {
		for _, ordered := range orderedPairwiseInputs(in, pair) {
			comparison, err := in.PairwiseJudge.ComparePairwise(ctx, ordered)
			if err != nil {
				return nil, fmt.Errorf("step60: pairwise compare agent=%s order=%s: %w", pair.Agent, ordered.Order, err)
			}
			if ordered.Order == "BA" {
				comparison = normalizeReversedComparison(comparison)
			}
			comparisons = append(comparisons, comparison)
		}
	}
	return comparisons, nil
}

func completedRuns(runs []scorableAgentRun, completedAgents []contracts.AgentID) []scorableAgentRun {
	completed := make(map[contracts.AgentID]struct{}, len(completedAgents))
	for _, agent := range completedAgents {
		completed[agent] = struct{}{}
	}
	out := make([]scorableAgentRun, 0, len(completedAgents))
	for _, run := range runs {
		if _, ok := completed[run.Agent]; ok {
			out = append(out, run)
		}
	}
	return out
}

func orderedPairwiseInputs(in Input, pair judges.PairwisePair) []judges.PairwiseInput {
	inputs := []judges.PairwiseInput{
		{
			RunID:      in.TaskPackage.RunID,
			Agent:      pair.Agent,
			Order:      "AB",
			TaskPrompt: in.TaskPackage.ReconstructedTaskPrompt,
			RubricPath: in.RubricPath,
			A:          pair.A,
			B:          pair.B,
		},
	}
	if in.PairwiseMode == judges.PairwiseModeStrict {
		inputs = append(inputs, judges.PairwiseInput{
			RunID:      in.TaskPackage.RunID,
			Agent:      pair.Agent,
			Order:      "BA",
			TaskPrompt: in.TaskPackage.ReconstructedTaskPrompt,
			RubricPath: in.RubricPath,
			A:          pair.B,
			B:          pair.A,
		})
	}
	return inputs
}

func normalizeReversedComparison(comparison judges.PairwiseComparison) judges.PairwiseComparison {
	comparison.Winner = invertPairwiseWinner(comparison.Winner)
	for i := range comparison.DimensionVotes {
		comparison.DimensionVotes[i].Winner = invertPairwiseWinner(comparison.DimensionVotes[i].Winner)
	}
	return comparison
}

func invertPairwiseWinner(winner contracts.PairwiseWinner) contracts.PairwiseWinner {
	switch winner {
	case contracts.PairwiseWinnerA:
		return contracts.PairwiseWinnerB
	case contracts.PairwiseWinnerB:
		return contracts.PairwiseWinnerA
	default:
		return contracts.PairwiseWinnerTie
	}
}

func pairwiseEntriesFromDecision(
	in Input,
	pairs []judges.PairwisePair,
	decision judges.PairwiseDecision,
	comparisons []judges.PairwiseComparison,
	resolvedAt time.Time,
) ([]contracts.PairwiseEntry, error) {
	agentDecisions := decisionByAgent(pairs, decision, comparisons)
	out := make([]contracts.PairwiseEntry, 0, len(pairs))
	for _, pair := range pairs {
		agentDecision := agentDecisions[pair.Agent]
		winner := winnerForFinalAction(decision.Action, agentDecision.Winner)
		entry := contracts.PairwiseEntry{
			SchemaVersion: "1",
			RunID:         in.TaskPackage.RunID,
			AgentA:        pair.Agent,
			AgentB:        pair.Agent,
			Winner:        winner,
			Margin:        agentDecision.Margin,
			Justification: pairwiseJustification(in.PairwiseMode, decision, agentDecision),
			VerdictPath:   contracts.VerdictPathSingle,
			RubricVersion: in.RubricVersion,
			PromptVersion: in.PairwisePromptVersion,
			ResolvedAt:    resolvedAt,
		}
		if entry.Margin == "" {
			entry.Margin = contracts.PairwiseMarginSlight
		}
		canonical, err := canonicalizePairwiseOverflow(in.IO, entry)
		if err != nil {
			return nil, err
		}
		out = append(out, canonical)
	}
	return out, nil
}

func decisionByAgent(
	pairs []judges.PairwisePair,
	decision judges.PairwiseDecision,
	comparisons []judges.PairwiseComparison,
) map[contracts.AgentID]judges.PairwiseAgentDecision {
	out := make(map[contracts.AgentID]judges.PairwiseAgentDecision, len(pairs))
	for _, row := range decision.AgentDecisions {
		out[row.Agent] = row
	}
	if len(out) == len(pairs) {
		return out
	}
	comparisonsByAgent := map[contracts.AgentID][]judges.PairwiseComparison{}
	for _, comparison := range comparisons {
		comparisonsByAgent[comparison.Agent] = append(comparisonsByAgent[comparison.Agent], comparison)
	}
	for _, pair := range pairs {
		if _, ok := out[pair.Agent]; ok {
			continue
		}
		derived, _ := judges.NewScoreDerivedPairwiseDecisionJudge().DecidePairwise(context.Background(), judges.PairwiseDecisionInput{
			Mode:        judges.PairwiseModeBasic,
			Pairs:       []judges.PairwisePair{pair},
			Comparisons: comparisonsByAgent[pair.Agent],
			RubricPath:  pair.A.OutputPath,
		})
		if len(derived.AgentDecisions) > 0 {
			out[pair.Agent] = derived.AgentDecisions[0]
		}
	}
	return out
}

func winnerForFinalAction(action judges.PairwiseDecisionAction, winner contracts.PairwiseWinner) contracts.PairwiseWinner {
	switch action {
	case judges.PairwiseDecisionAdopt:
		return winner
	case judges.PairwiseDecisionReject:
		return contracts.PairwiseWinnerA
	default:
		return contracts.PairwiseWinnerTie
	}
}

func pairwiseJustification(mode judges.PairwiseMode, decision judges.PairwiseDecision, agentDecision judges.PairwiseAgentDecision) string {
	parts := []string{fmt.Sprintf("mode=%s decision=%s", mode, decision.Action)}
	if agentDecision.Justification != "" {
		parts = append(parts, "agent="+agentDecision.Justification)
	}
	if decision.Justification != "" {
		parts = append(parts, "final="+decision.Justification)
	}
	return strings.Join(parts, " ")
}

func canonicalizePairwiseOverflow(runIO internalio.RunContext, entry contracts.PairwiseEntry) (contracts.PairwiseEntry, error) {
	entry.JustificationOverflowRef = nil
	if len([]rune(entry.Justification)) <= 500 {
		return entry, nil
	}
	ref, err := scorecore.WriteOverflowSidecar(runIO, "60", entry.Justification)
	if err != nil {
		return contracts.PairwiseEntry{}, err
	}
	entry.Justification = ""
	entry.JustificationOverflowRef = &ref
	return entry, nil
}

func toPairwiseScores(scores []contracts.ScoreEntry) []judges.PairwiseScore {
	out := make([]judges.PairwiseScore, 0, len(scores))
	for _, score := range scores {
		out = append(out, judges.PairwiseScore{
			Dimension: score.Dimension,
			Score:     score.Score,
			Reason:    score.Reasons,
		})
	}
	return out
}
