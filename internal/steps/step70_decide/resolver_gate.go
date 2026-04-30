package step70_decide

import (
	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/steps/scorecore"
)

func promotionGatePassedWithArtifacts(runCtx internalio.RunContext, artifacts step60ArtifactSnapshot, agent contracts.AgentID, candidates *contracts.Candidates) (bool, error) {
	pass1Scores, err := loadCollapsedScores(runCtx, "30/scores-A.jsonl")
	if err != nil {
		return false, err
	}
	pass2Scores := collapsedScoresByAgent(artifacts.Scores)
	pass1 := pass1Scores[agent]
	pass2 := pass2Scores[agent]
	if !completeScoreSet(pass1) || !completeScoreSet(pass2) {
		return false, nil
	}
	return true, nil
}

func hasDuplicateUpdateTarget(candidates []contracts.Candidate) bool {
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if candidate.Kind != contracts.CandidateKindUpdate {
			continue
		}
		if _, ok := seen[candidate.TargetRuleID]; ok {
			return true
		}
		seen[candidate.TargetRuleID] = struct{}{}
	}
	return false
}

func loadCollapsedScores(runCtx internalio.RunContext, rel string) (map[contracts.AgentID]map[contracts.Dimension]contracts.ScoreEntry, error) {
	path, err := runCtx.ResolveRunRelative(rel)
	if err != nil {
		return nil, err
	}
	rows, err := internalio.ReadJSONL[contracts.ScoreEntry](path)
	if err != nil {
		return nil, err
	}
	return collapsedScoresByAgent(rows), nil
}

func collapsedScoresByAgent(rows []contracts.ScoreEntry) map[contracts.AgentID]map[contracts.Dimension]contracts.ScoreEntry {
	collapsed := scorecore.CollapseFinalScores(rows)
	byAgent := make(map[contracts.AgentID]map[contracts.Dimension]contracts.ScoreEntry)
	for _, row := range collapsed {
		if _, ok := byAgent[row.Agent]; !ok {
			byAgent[row.Agent] = make(map[contracts.Dimension]contracts.ScoreEntry)
		}
		byAgent[row.Agent][row.Dimension] = row
	}
	return byAgent
}

func completeScoreSet(scores map[contracts.Dimension]contracts.ScoreEntry) bool {
	if len(scores) != 5 {
		return false
	}
	for _, dimension := range []contracts.Dimension{
		contracts.DimensionFidelity,
		contracts.DimensionCorrectness,
		contracts.DimensionMaintainability,
		contracts.DimensionDiscipline,
		contracts.DimensionCommunication,
	} {
		if _, ok := scores[dimension]; !ok {
			return false
		}
	}
	return true
}
