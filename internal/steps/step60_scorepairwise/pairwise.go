package step60_scorepairwise

import (
	"fmt"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
)

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
