package step30_score

import (
	"bytes"
	"os"
	"sort"

	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/nishimoto265/harnest/internal/judges"
	"github.com/nishimoto265/harnest/internal/steps/scorecore"
)

func expectedComplianceRuleSet(ruleIDs []string) map[string]struct{} {
	if len(ruleIDs) == 0 {
		return nil
	}
	rules := make(map[string]struct{}, len(ruleIDs))
	for _, ruleID := range ruleIDs {
		rules[ruleID] = struct{}{}
	}
	return rules
}

func expectedStep30ComplianceRuleIDs(rubricPath string) ([]string, error) {
	activeRuleIDs, err := judges.ActiveComplianceRuleIDs(rubricPath)
	if err != nil {
		return nil, err
	}
	if len(activeRuleIDs) > 0 {
		return activeRuleIDs, nil
	}
	return judges.ExpectedComplianceRuleIDs(rubricPath)
}

func complianceRuleExpected(ruleID string, expected map[string]struct{}) bool {
	if len(expected) == 0 {
		return false
	}
	_, ok := expected[ruleID]
	return ok
}

func filterRawScoreRows(rows []contracts.RawScoreEntry, agents []contracts.AgentID) []contracts.RawScoreEntry {
	agentSet := agentSet(agents)
	out := make([]contracts.RawScoreEntry, 0, len(rows))
	for _, row := range rows {
		if _, ok := agentSet[row.Agent]; ok && row.JudgeRole == contracts.JudgeRolePrimary {
			out = append(out, row)
		}
	}
	return out
}

func filterFinalScoreRows(rows []contracts.ScoreEntry, agents []contracts.AgentID) []contracts.ScoreEntry {
	agentSet := agentSet(agents)
	out := make([]contracts.ScoreEntry, 0, len(rows))
	for _, row := range rows {
		if _, ok := agentSet[row.Agent]; ok {
			out = append(out, row)
		}
	}
	return out
}

func filterRawComplianceRows(rows []contracts.RawComplianceEntry, agents []contracts.AgentID, expected map[string]struct{}) []contracts.RawComplianceEntry {
	agentSet := agentSet(agents)
	out := make([]contracts.RawComplianceEntry, 0, len(rows))
	for _, row := range rows {
		if _, ok := agentSet[row.Agent]; ok && row.JudgeRole == contracts.JudgeRolePrimary && complianceRuleExpected(row.RuleID, expected) {
			out = append(out, row)
		}
	}
	return out
}

func filterFinalComplianceRows(rows []contracts.ComplianceEntry, agents []contracts.AgentID, expected map[string]struct{}) []contracts.ComplianceEntry {
	agentSet := agentSet(agents)
	out := make([]contracts.ComplianceEntry, 0, len(rows))
	for _, row := range rows {
		if _, ok := agentSet[row.Agent]; ok && complianceRuleExpected(row.RuleID, expected) {
			out = append(out, row)
		}
	}
	return out
}

func appendExpectedFinalScores(paths stepPathsResult, state *resumeAgentState, result scorecore.PanelResult) error {
	for _, row := range result.FinalScores {
		current, ok := state.finalScores[row.Dimension]
		if ok {
			same, err := sameCanonicalJSON(current, row)
			if err != nil {
				return err
			}
			if same {
				continue
			}
		}
		if err := internalio.AppendJSONL(paths.ScoreFinal, row); err != nil {
			return err
		}
		state.upsertFinalScores([]contracts.ScoreEntry{row})
	}
	return nil
}

func appendExpectedFinalCompliance(paths stepPathsResult, state *resumeAgentState, result scorecore.PanelResult) error {
	for _, row := range result.FinalCompliance {
		current, ok := state.finalCompliance[row.RuleID]
		if ok {
			same, err := sameCanonicalJSON(current, row)
			if err != nil {
				return err
			}
			if same {
				continue
			}
		}
		if err := internalio.AppendJSONL(paths.ComplianceFinal, row); err != nil {
			return err
		}
		state.upsertFinalCompliance([]contracts.ComplianceEntry{row})
	}
	return nil
}

func appendIssueEntries(paths stepPathsResult, rows []contracts.IssueEntry) error {
	if len(rows) == 0 {
		return nil
	}
	existingRows, err := internalio.ReadJSONL[contracts.IssueEntry](paths.IssueFinal)
	if err != nil {
		return err
	}
	existingByID := make(map[string]contracts.IssueEntry, len(existingRows))
	for _, row := range existingRows {
		existingByID[row.IssueID] = row
	}
	for _, row := range rows {
		if current, ok := existingByID[row.IssueID]; ok {
			same, err := sameCanonicalJSON(current, row)
			if err != nil {
				return err
			}
			if same {
				continue
			}
		}
		if err := internalio.AppendJSONL(paths.IssueFinal, row); err != nil {
			return err
		}
		existingByID[row.IssueID] = row
	}
	return nil
}

func resetStep30FinalFiles(paths stepPathsResult) error {
	if err := rewriteJSONL[contracts.ScoreEntry](paths.ScoreFinal, nil); err != nil {
		return err
	}
	if err := rewriteJSONL[contracts.ComplianceEntry](paths.ComplianceFinal, nil); err != nil {
		return err
	}
	return rewriteJSONL[contracts.IssueEntry](paths.IssueFinal, nil)
}

func rewriteJSONL[T any](path string, rows []T) error {
	var buf bytes.Buffer
	for _, row := range rows {
		if _, err := contracts.MarshalStrict(row); err != nil {
			return err
		}
		payload, err := contracts.CanonicalMarshal(row)
		if err != nil {
			return err
		}
		if len(payload)+1 > internalio.JSONLMaxLineBytes {
			return internalio.ErrEntryTooLarge
		}
		buf.Write(payload)
		buf.WriteByte('\n')
	}
	return internalio.WriteAtomic(path, buf.Bytes())
}

func fileExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func step30DoneMarkerAgentsMatch(markerPath string, expectedAgents []contracts.AgentID) (bool, error) {
	marker, err := internalio.ReadJSON[contracts.Step30DoneMarker](markerPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return sameAgentSet(marker.CompletedAgents, expectedAgents), nil
}

func step30FinalRowsWithinCurrentScope(paths stepPathsResult, agents []contracts.AgentID, expectedComplianceRules map[string]struct{}) (bool, error) {
	scoreFinal, err := internalio.ReadJSONL[contracts.ScoreEntry](paths.ScoreFinal)
	if err != nil {
		return false, err
	}
	complianceFinal, err := internalio.ReadJSONL[contracts.ComplianceEntry](paths.ComplianceFinal)
	if err != nil {
		return false, err
	}
	agentSet := agentSet(agents)
	for _, row := range scorecore.CollapseFinalScores(scoreFinal) {
		if _, ok := agentSet[row.Agent]; !ok {
			return false, nil
		}
	}
	for _, row := range scorecore.CollapseFinalCompliance(complianceFinal) {
		if _, ok := agentSet[row.Agent]; !ok {
			return false, nil
		}
		if !complianceRuleExpected(row.RuleID, expectedComplianceRules) {
			return false, nil
		}
	}
	return true, nil
}

func step30FinalRowsCompleteForCurrentScope(paths stepPathsResult, agents []contracts.AgentID, expectedComplianceRules map[string]struct{}) (bool, error) {
	withinScope, err := step30FinalRowsWithinCurrentScope(paths, agents, expectedComplianceRules)
	if err != nil || !withinScope {
		return withinScope, err
	}

	scoreFinal, err := internalio.ReadJSONL[contracts.ScoreEntry](paths.ScoreFinal)
	if err != nil {
		return false, err
	}
	complianceFinal, err := internalio.ReadJSONL[contracts.ComplianceEntry](paths.ComplianceFinal)
	if err != nil {
		return false, err
	}

	scoreRows := scorecore.CollapseFinalScores(scoreFinal)
	if len(scoreRows) != len(agents)*len(allDimensions()) {
		return false, nil
	}
	scores := make(map[contracts.AgentID]map[contracts.Dimension]struct{}, len(agents))
	for _, row := range scoreRows {
		if scores[row.Agent] == nil {
			scores[row.Agent] = make(map[contracts.Dimension]struct{}, len(allDimensions()))
		}
		scores[row.Agent][row.Dimension] = struct{}{}
	}
	for _, agent := range agents {
		for _, dimension := range allDimensions() {
			if _, ok := scores[agent][dimension]; !ok {
				return false, nil
			}
		}
	}

	complianceRows := scorecore.CollapseFinalCompliance(complianceFinal)
	expectedComplianceCount := len(agents) * len(expectedComplianceRules)
	if len(complianceRows) != expectedComplianceCount {
		return false, nil
	}
	if len(expectedComplianceRules) == 0 {
		return true, nil
	}
	compliance := make(map[contracts.AgentID]map[string]struct{}, len(agents))
	for _, row := range complianceRows {
		if compliance[row.Agent] == nil {
			compliance[row.Agent] = make(map[string]struct{}, len(expectedComplianceRules))
		}
		compliance[row.Agent][row.RuleID] = struct{}{}
	}
	for _, agent := range agents {
		for ruleID := range expectedComplianceRules {
			if _, ok := compliance[agent][ruleID]; !ok {
				return false, nil
			}
		}
	}
	return true, nil
}

func agentSet(agents []contracts.AgentID) map[contracts.AgentID]struct{} {
	out := make(map[contracts.AgentID]struct{}, len(agents))
	for _, agent := range agents {
		out[agent] = struct{}{}
	}
	return out
}

func sameAgentSet(left, right []contracts.AgentID) bool {
	if len(left) != len(right) {
		return false
	}
	leftSorted := append([]contracts.AgentID(nil), left...)
	rightSorted := append([]contracts.AgentID(nil), right...)
	sort.Slice(leftSorted, func(i, j int) bool { return leftSorted[i] < leftSorted[j] })
	sort.Slice(rightSorted, func(i, j int) bool { return rightSorted[i] < rightSorted[j] })
	for i := range leftSorted {
		if leftSorted[i] != rightSorted[i] {
			return false
		}
	}
	return true
}

func sameCanonicalJSON(left, right any) (bool, error) {
	leftJSON, err := contracts.CanonicalMarshal(left)
	if err != nil {
		return false, err
	}
	rightJSON, err := contracts.CanonicalMarshal(right)
	if err != nil {
		return false, err
	}
	return bytes.Equal(leftJSON, rightJSON), nil
}

func hasAllDimensions(rows map[contracts.Dimension]contracts.RawScoreEntry) bool {
	for _, dim := range allDimensions() {
		if _, ok := rows[dim]; !ok {
			return false
		}
	}
	return true
}

func allDimensions() []contracts.Dimension {
	return []contracts.Dimension{
		contracts.DimensionFidelity,
		contracts.DimensionCorrectness,
		contracts.DimensionMaintainability,
		contracts.DimensionDiscipline,
		contracts.DimensionCommunication,
	}
}

func step30VersionsMatch(paths stepPathsResult, agents []contracts.AgentID, rubricVersion, promptVersion string, expectedComplianceRules map[string]struct{}) (bool, error) {
	scoreRaw, err := internalio.ReadJSONL[contracts.RawScoreEntry](paths.ScoreRaw)
	if err != nil {
		return false, err
	}
	complianceRaw, err := internalio.ReadJSONL[contracts.RawComplianceEntry](paths.ComplianceRaw)
	if err != nil {
		return false, err
	}
	scoreFinal, err := internalio.ReadJSONL[contracts.ScoreEntry](paths.ScoreFinal)
	if err != nil {
		return false, err
	}
	complianceFinal, err := internalio.ReadJSONL[contracts.ComplianceEntry](paths.ComplianceFinal)
	if err != nil {
		return false, err
	}
	rawScoreRows := filterRawScoreRows(scorecore.CollapseRawScores(scoreRaw), agents)
	rawComplianceRows := filterRawComplianceRows(scorecore.CollapseRawCompliance(complianceRaw), agents, expectedComplianceRules)
	finalScoreRows := filterFinalScoreRows(scorecore.CollapseFinalScores(scoreFinal), agents)
	finalComplianceRows := filterFinalComplianceRows(scorecore.CollapseFinalCompliance(complianceFinal), agents, expectedComplianceRules)
	return scorecore.RowsMatchVersion(rawScoreRows, func(row contracts.RawScoreEntry) (string, string) {
		return row.RubricVersion, row.PromptVersion
	}, rubricVersion, promptVersion) &&
		scorecore.RowsMatchVersion(rawComplianceRows, func(row contracts.RawComplianceEntry) (string, string) {
			return row.RubricVersion, row.PromptVersion
		}, rubricVersion, promptVersion) &&
		scorecore.RowsMatchVersion(finalScoreRows, func(row contracts.ScoreEntry) (string, string) {
			return row.RubricVersion, row.PromptVersion
		}, rubricVersion, promptVersion) &&
		scorecore.RowsMatchVersion(finalComplianceRows, func(row contracts.ComplianceEntry) (string, string) {
			return row.RubricVersion, row.PromptVersion
		}, rubricVersion, promptVersion), nil
}
