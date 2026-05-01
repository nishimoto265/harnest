package step30_score

import (
	"sort"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/steps/scorecore"
)

type resumeState struct {
	agents map[contracts.AgentID]*resumeAgentState
}

type resumeAgentState struct {
	rawScores       map[contracts.JudgeRole]map[contracts.Dimension]contracts.RawScoreEntry
	rawCompliance   map[contracts.JudgeRole]map[string]contracts.RawComplianceEntry
	finalScores     map[contracts.Dimension]contracts.ScoreEntry
	finalCompliance map[string]contracts.ComplianceEntry
}

func loadResumeState(paths stepPathsResult, expectedComplianceRules map[string]struct{}) (*resumeState, error) {
	scoreRaw, err := internalio.ReadJSONL[contracts.RawScoreEntry](paths.ScoreRaw)
	if err != nil {
		return nil, err
	}
	complianceRaw, err := internalio.ReadJSONL[contracts.RawComplianceEntry](paths.ComplianceRaw)
	if err != nil {
		return nil, err
	}
	scoreFinal, err := internalio.ReadJSONL[contracts.ScoreEntry](paths.ScoreFinal)
	if err != nil {
		return nil, err
	}
	complianceFinal, err := internalio.ReadJSONL[contracts.ComplianceEntry](paths.ComplianceFinal)
	if err != nil {
		return nil, err
	}

	state := &resumeState{agents: make(map[contracts.AgentID]*resumeAgentState)}
	for _, row := range scorecore.CollapseRawScores(scoreRaw) {
		if row.JudgeRole != contracts.JudgeRolePrimary {
			continue
		}
		state.agent(row.Agent).upsertRawScores([]contracts.RawScoreEntry{row})
	}
	for _, row := range scorecore.CollapseRawCompliance(complianceRaw) {
		if row.JudgeRole != contracts.JudgeRolePrimary {
			continue
		}
		if !complianceRuleExpected(row.RuleID, expectedComplianceRules) {
			continue
		}
		state.agent(row.Agent).upsertRawCompliance([]contracts.RawComplianceEntry{row})
	}
	for _, row := range scorecore.CollapseFinalScores(scoreFinal) {
		state.agent(row.Agent).upsertFinalScores([]contracts.ScoreEntry{row})
	}
	for _, row := range scorecore.CollapseFinalCompliance(complianceFinal) {
		if !complianceRuleExpected(row.RuleID, expectedComplianceRules) {
			continue
		}
		state.agent(row.Agent).upsertFinalCompliance([]contracts.ComplianceEntry{row})
	}
	return state, nil
}

func (s *resumeState) agent(agent contracts.AgentID) *resumeAgentState {
	if existing, ok := s.agents[agent]; ok {
		return existing
	}
	state := &resumeAgentState{
		rawScores: map[contracts.JudgeRole]map[contracts.Dimension]contracts.RawScoreEntry{
			contracts.JudgeRolePrimary: {},
		},
		rawCompliance: map[contracts.JudgeRole]map[string]contracts.RawComplianceEntry{
			contracts.JudgeRolePrimary: {},
		},
		finalScores:     make(map[contracts.Dimension]contracts.ScoreEntry),
		finalCompliance: make(map[string]contracts.ComplianceEntry),
	}
	s.agents[agent] = state
	return state
}

func (s *resumeState) clearFinals() {
	for _, agentState := range s.agents {
		agentState.clearFinal()
	}
}

func (s *resumeAgentState) upsertRawScores(rows []contracts.RawScoreEntry) {
	for _, row := range rows {
		s.rawScores[row.JudgeRole][row.Dimension] = row
	}
}

func (s *resumeAgentState) replaceRawScores(role contracts.JudgeRole, rows []contracts.RawScoreEntry) {
	s.rawScores[role] = make(map[contracts.Dimension]contracts.RawScoreEntry, len(rows))
	s.upsertRawScores(rows)
}

func (s *resumeAgentState) upsertRawCompliance(rows []contracts.RawComplianceEntry) {
	for _, row := range rows {
		s.rawCompliance[row.JudgeRole][row.RuleID] = row
	}
}

func (s *resumeAgentState) replaceRawCompliance(role contracts.JudgeRole, rows []contracts.RawComplianceEntry) {
	s.rawCompliance[role] = make(map[string]contracts.RawComplianceEntry, len(rows))
	s.upsertRawCompliance(rows)
}

func (s *resumeAgentState) upsertFinalScores(rows []contracts.ScoreEntry) {
	for _, row := range rows {
		s.finalScores[row.Dimension] = row
	}
}

func (s *resumeAgentState) upsertFinalCompliance(rows []contracts.ComplianceEntry) {
	for _, row := range rows {
		s.finalCompliance[row.RuleID] = row
	}
}

func (s *resumeAgentState) clearFinal() {
	s.finalScores = make(map[contracts.Dimension]contracts.ScoreEntry)
	s.finalCompliance = make(map[string]contracts.ComplianceEntry)
}

func (s *resumeAgentState) roleComplete(role contracts.JudgeRole, outputSha, rubricVersion, promptVersion string, expectedRules map[string]struct{}) bool {
	if !hasAllDimensions(s.rawScores[role]) {
		return false
	}
	if !s.roleOutputShaMatches(role, outputSha) || !s.roleVersionMatches(role, rubricVersion, promptVersion) {
		return false
	}
	return s.roleComplianceCoverage(role, expectedRules)
}

func (s *resumeAgentState) rawScoreSlice(role contracts.JudgeRole) []contracts.RawScoreEntry {
	out := make([]contracts.RawScoreEntry, 0, len(s.rawScores[role]))
	for _, dim := range allDimensions() {
		row, ok := s.rawScores[role][dim]
		if ok {
			out = append(out, row)
		}
	}
	return out
}

func (s *resumeAgentState) rawComplianceSlice(role contracts.JudgeRole) []contracts.RawComplianceEntry {
	rules := make([]string, 0, len(s.rawCompliance[role]))
	for ruleID := range s.rawCompliance[role] {
		rules = append(rules, ruleID)
	}
	sort.Strings(rules)
	out := make([]contracts.RawComplianceEntry, 0, len(rules))
	for _, ruleID := range rules {
		out = append(out, s.rawCompliance[role][ruleID])
	}
	return out
}

func (s *resumeAgentState) roleVersionMatches(role contracts.JudgeRole, rubricVersion, promptVersion string) bool {
	for _, row := range s.rawScores[role] {
		if row.RubricVersion != rubricVersion || row.PromptVersion != promptVersion {
			return false
		}
	}
	for _, row := range s.rawCompliance[role] {
		if row.RubricVersion != rubricVersion || row.PromptVersion != promptVersion {
			return false
		}
	}
	return true
}

func (s *resumeAgentState) roleOutputShaMatches(role contracts.JudgeRole, outputSha string) bool {
	for _, row := range s.rawScores[role] {
		if row.OutputSha256 != outputSha {
			return false
		}
	}
	for _, row := range s.rawCompliance[role] {
		if row.OutputSha256 != outputSha {
			return false
		}
	}
	return true
}

func (s *resumeAgentState) roleComplianceCoverage(role contracts.JudgeRole, expected map[string]struct{}) bool {
	if len(expected) == 0 {
		return len(s.rawCompliance[role]) == 0
	}
	if len(s.rawCompliance[role]) != len(expected) {
		return false
	}
	for ruleID := range expected {
		if _, ok := s.rawCompliance[role][ruleID]; !ok {
			return false
		}
	}
	return true
}
