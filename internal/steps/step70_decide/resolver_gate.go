package step70_decide

import (
	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/steps/scorecore"
)

func promotionGatePassedWithArtifacts(runCtx internalio.RunContext, artifacts step60ArtifactSnapshot, agent contracts.AgentID, candidates *contracts.Candidates) (bool, error) {
	if ok := hasCompliantCandidateEvidence(artifacts.Compliance, agent, candidates); !ok {
		return false, nil
	}
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

func hasCompliantCandidateEvidence(rows []contracts.ComplianceEntry, agent contracts.AgentID, candidates *contracts.Candidates) bool {
	required := requiredCandidateComplianceRuleIDs(candidates)
	if len(required) == 0 {
		return true
	}
	collapsed := scorecore.CollapseFinalCompliance(rows)
	byRule := make(map[string]contracts.ComplianceEntry, len(collapsed))
	for _, row := range collapsed {
		if row.Agent == agent {
			byRule[row.RuleID] = row
		}
	}
	for _, ruleID := range required {
		row, ok := byRule[ruleID]
		if !ok || row.Verdict != contracts.ComplianceVerdictCompliant {
			return false
		}
	}
	return true
}

func requiredCandidateComplianceRuleIDs(candidates *contracts.Candidates) []string {
	if candidates == nil {
		return nil
	}
	ruleIDs := make([]string, 0, len(candidates.Candidates))
	for _, candidate := range candidates.Candidates {
		switch candidate.Kind {
		case contracts.CandidateKindNew, contracts.CandidateKindUpdate:
			ruleIDs = append(ruleIDs, candidate.CandidateID)
		}
	}
	return ruleIDs
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
