package step60_scorepairwise

import (
	"fmt"
	"os"
	"sort"

	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/nishimoto265/harnest/internal/steps/scorecore"
)

type step60RawState struct {
	scores     map[contracts.AgentID]map[contracts.JudgeRole]map[contracts.Dimension]contracts.RawScoreEntry
	compliance map[contracts.AgentID]map[contracts.JudgeRole]map[string]contracts.RawComplianceEntry
	final      map[contracts.AgentID]map[string]contracts.ComplianceEntry
}

func loadStep60RawState(paths step60Paths) (step60RawState, error) {
	scoreRows, err := readJSONLOrEmpty[contracts.RawScoreEntry](paths.ScoresRaw)
	if err != nil {
		return step60RawState{}, err
	}
	complianceRows, err := readJSONLOrEmpty[contracts.RawComplianceEntry](paths.ComplianceRaw)
	if err != nil {
		return step60RawState{}, err
	}
	finalComplianceRows, err := readJSONLOrEmpty[contracts.ComplianceEntry](paths.ComplianceFinal)
	if err != nil {
		return step60RawState{}, err
	}
	state := step60RawState{
		scores:     map[contracts.AgentID]map[contracts.JudgeRole]map[contracts.Dimension]contracts.RawScoreEntry{},
		compliance: map[contracts.AgentID]map[contracts.JudgeRole]map[string]contracts.RawComplianceEntry{},
		final:      map[contracts.AgentID]map[string]contracts.ComplianceEntry{},
	}
	for _, row := range reduceRawScores(scoreRows) {
		state.ensureAgent(row.Agent)
		state.scores[row.Agent][row.JudgeRole][row.Dimension] = row
	}
	for _, row := range reduceRawCompliance(complianceRows) {
		state.ensureAgent(row.Agent)
		state.compliance[row.Agent][row.JudgeRole][row.RuleID] = row
	}
	for _, row := range internalio.CollapseByKey(finalComplianceRows, func(entry contracts.ComplianceEntry) complianceKey {
		return complianceKey{Agent: entry.Agent, RuleID: entry.RuleID}
	}) {
		state.ensureAgent(row.Agent)
		state.final[row.Agent][row.RuleID] = row
	}
	return state, nil
}

func (s *step60RawState) ensureAgent(agent contracts.AgentID) {
	if _, ok := s.scores[agent]; !ok {
		s.scores[agent] = map[contracts.JudgeRole]map[contracts.Dimension]contracts.RawScoreEntry{
			contracts.JudgeRolePrimary:   {},
			contracts.JudgeRoleSecondary: {},
			contracts.JudgeRoleArbiter:   {},
		}
	}
	if _, ok := s.compliance[agent]; !ok {
		s.compliance[agent] = map[contracts.JudgeRole]map[string]contracts.RawComplianceEntry{
			contracts.JudgeRolePrimary:   {},
			contracts.JudgeRoleSecondary: {},
			contracts.JudgeRoleArbiter:   {},
		}
	}
	if _, ok := s.final[agent]; !ok {
		s.final[agent] = map[string]contracts.ComplianceEntry{}
	}
}

func (s step60RawState) scoreRows(agent contracts.AgentID, role contracts.JudgeRole) []contracts.RawScoreEntry {
	roleRows := s.scores[agent][role]
	out := make([]contracts.RawScoreEntry, 0, len(roleRows))
	for _, dimension := range canonicalDimensions {
		if row, ok := roleRows[dimension]; ok {
			out = append(out, row)
		}
	}
	return out
}

func (s step60RawState) complianceRows(agent contracts.AgentID, role contracts.JudgeRole) []contracts.RawComplianceEntry {
	roleRows := s.compliance[agent][role]
	ruleIDs := make([]string, 0, len(roleRows))
	for ruleID := range roleRows {
		ruleIDs = append(ruleIDs, ruleID)
	}
	sort.Strings(ruleIDs)
	out := make([]contracts.RawComplianceEntry, 0, len(ruleIDs))
	for _, ruleID := range ruleIDs {
		out = append(out, roleRows[ruleID])
	}
	return out
}

func tryReuseRawPanelResult(
	runIO internalio.RunContext,
	state step60RawState,
	agent contracts.AgentID,
	outputHash, rubricVersion, promptVersion string,
	expectedCompliance map[string]struct{},
	secondaryPresent bool,
) (scorecore.PanelResult, bool, error) {
	primaryScores := state.scoreRows(agent, contracts.JudgeRolePrimary)
	primaryCompliance := state.complianceRows(agent, contracts.JudgeRolePrimary)
	if !rawRoleUsable(primaryScores, outputHash, rubricVersion, promptVersion) {
		return scorecore.PanelResult{}, false, nil
	}
	if len(expectedCompliance) == 0 && len(primaryCompliance) > 0 {
		return scorecore.PanelResult{}, false, nil
	}
	if !rawComplianceUsable(primaryCompliance, expectedCompliance, outputHash, rubricVersion, promptVersion) {
		return scorecore.PanelResult{}, false, nil
	}
	if err := validateRawScoreOverflowRefs(runIO, primaryScores); err != nil {
		return scorecore.PanelResult{}, false, nil
	}
	if err := validateRawComplianceOverflowRefs(runIO, primaryCompliance); err != nil {
		return scorecore.PanelResult{}, false, nil
	}

	var secondaryScores []contracts.RawScoreEntry
	var secondaryCompliance []contracts.RawComplianceEntry
	var arbiterScores []contracts.RawScoreEntry
	var arbiterCompliance []contracts.RawComplianceEntry
	if !secondaryPresent {
		result, err := rebuildFinalResultFromRaw(
			primaryScores,
			nil,
			nil,
			primaryCompliance,
			nil,
			nil,
			defaultDisagreementThreshold,
			false,
			false,
		)
		if err != nil {
			return scorecore.PanelResult{}, false, nil
		}
		return result, true, nil
	}

	secondaryScores = state.scoreRows(agent, contracts.JudgeRoleSecondary)
	secondaryCompliance = state.complianceRows(agent, contracts.JudgeRoleSecondary)
	arbiterScores = state.scoreRows(agent, contracts.JudgeRoleArbiter)
	arbiterCompliance = state.complianceRows(agent, contracts.JudgeRoleArbiter)
	if !rawRoleUsable(secondaryScores, outputHash, rubricVersion, promptVersion) {
		return scorecore.PanelResult{}, false, nil
	}
	if len(expectedCompliance) == 0 && (len(secondaryCompliance) > 0 || len(arbiterCompliance) > 0) {
		return scorecore.PanelResult{}, false, nil
	}
	if !rawComplianceUsable(secondaryCompliance, expectedCompliance, outputHash, rubricVersion, promptVersion) {
		return scorecore.PanelResult{}, false, nil
	}
	if err := validateRawScoreOverflowRefs(runIO, secondaryScores); err != nil {
		return scorecore.PanelResult{}, false, nil
	}
	if err := validateRawComplianceOverflowRefs(runIO, secondaryCompliance); err != nil {
		return scorecore.PanelResult{}, false, nil
	}
	if !rawRoleUsable(arbiterScores, outputHash, rubricVersion, promptVersion) {
		arbiterScores = nil
	}
	if !rawArbiterComplianceUsable(arbiterCompliance, outputHash, rubricVersion, promptVersion) {
		arbiterCompliance = nil
	}
	if err := validateRawScoreOverflowRefs(runIO, arbiterScores); err != nil {
		arbiterScores = nil
	}
	if err := validateRawComplianceOverflowRefs(runIO, arbiterCompliance); err != nil {
		arbiterCompliance = nil
	}
	result, err := rebuildFinalResultFromRaw(
		primaryScores,
		secondaryScores,
		arbiterScores,
		primaryCompliance,
		secondaryCompliance,
		arbiterCompliance,
		defaultDisagreementThreshold,
		true,
		len(arbiterScores) > 0 || len(arbiterCompliance) > 0,
	)
	if err != nil {
		return scorecore.PanelResult{}, false, nil
	}
	return result, true, nil
}

func appendPanelFinals(paths step60Paths, meta finalMetadata, result scorecore.PanelResult) ([]contracts.ScoreEntry, []contracts.ComplianceEntry, error) {
	finalScores := make([]contracts.ScoreEntry, 0, len(result.FinalScores))
	finalCompliance := make([]contracts.ComplianceEntry, 0, len(result.FinalCompliance))
	for _, row := range result.FinalScores {
		finalScore := finalizeScore(meta, row, row.VerdictPath)
		if err := appendJSONLWithParentDirSync(paths.ScoresFinal, finalScore); err != nil {
			return nil, nil, err
		}
		finalScores = append(finalScores, finalScore)
	}
	for _, row := range result.FinalCompliance {
		finalEntry := finalizeCompliance(meta, row, row.VerdictPath)
		if err := appendJSONLWithParentDirSync(paths.ComplianceFinal, finalEntry); err != nil {
			return nil, nil, err
		}
		finalCompliance = append(finalCompliance, finalEntry)
	}
	return finalScores, finalCompliance, nil
}

func rebuildFinalResultFromRaw(
	primaryScores, secondaryScores, arbiterScores []contracts.RawScoreEntry,
	primaryCompliance, secondaryCompliance, arbiterCompliance []contracts.RawComplianceEntry,
	threshold int,
	secondaryPresent, arbiterPresent bool,
) (scorecore.PanelResult, error) {
	scoreResult, err := scorecore.BuildFinalResultFromRaw(
		primaryScores,
		secondaryScores,
		arbiterScores,
		nil,
		nil,
		nil,
		threshold,
		secondaryPresent,
		len(arbiterScores) > 0,
	)
	if err != nil {
		return scorecore.PanelResult{}, err
	}

	finalCompliance, err := rebuildFinalComplianceFromRaw(
		primaryCompliance,
		secondaryCompliance,
		arbiterCompliance,
		secondaryPresent,
		arbiterPresent,
	)
	if err != nil {
		return scorecore.PanelResult{}, err
	}

	scoreResult.RawCompliance = make([]contracts.RawComplianceEntry, 0, len(primaryCompliance)+len(secondaryCompliance)+len(arbiterCompliance))
	scoreResult.RawCompliance = append(scoreResult.RawCompliance, primaryCompliance...)
	scoreResult.RawCompliance = append(scoreResult.RawCompliance, secondaryCompliance...)
	scoreResult.RawCompliance = append(scoreResult.RawCompliance, arbiterCompliance...)
	scoreResult.FinalCompliance = finalCompliance
	return scoreResult, nil
}

func rebuildFinalComplianceFromRaw(
	primaryRows, secondaryRows, arbiterRows []contracts.RawComplianceEntry,
	secondaryPresent, arbiterPresent bool,
) ([]contracts.ComplianceEntry, error) {
	primary, err := rawComplianceEntriesAsFinal(primaryRows)
	if err != nil {
		return nil, err
	}
	if !secondaryPresent {
		ruleIDs := sortedComplianceRuleIDs(primary)
		finalEntries := make([]contracts.ComplianceEntry, 0, len(ruleIDs))
		for _, ruleID := range ruleIDs {
			entry := primary[ruleID]
			entry.VerdictPath = contracts.VerdictPathSingle
			finalEntries = append(finalEntries, entry)
		}
		return finalEntries, nil
	}

	secondary, err := rawComplianceEntriesAsFinal(secondaryRows)
	if err != nil {
		return nil, err
	}
	if !complianceRuleSetsMatch(primary, secondary) {
		return nil, fmt.Errorf("step60: compliance rule-set mismatch in raw reuse")
	}

	disputed := disputedComplianceRuleIDs(primary, secondary)
	arbiter, err := rawComplianceEntriesAsFinal(arbiterRows)
	if err != nil {
		return nil, err
	}
	if len(disputed) == 0 {
		ruleIDs := complianceRuleIDs(primary, secondary)
		finalEntries := make([]contracts.ComplianceEntry, 0, len(ruleIDs))
		for _, ruleID := range ruleIDs {
			entry := primary[ruleID]
			entry.VerdictPath = contracts.VerdictPathAgreement
			finalEntries = append(finalEntries, entry)
		}
		return finalEntries, nil
	}
	if !arbiterPresent || len(arbiter) == 0 {
		return nil, scorecore.ErrPanelArbiterRequired
	}
	if err := scorecore.ValidateArbiterComplianceRuleCoverage(disputed, disputed, sortedComplianceRuleIDs(arbiter)); err != nil {
		return nil, err
	}

	ruleIDs := complianceRuleIDs(primary, secondary)
	finalEntries := make([]contracts.ComplianceEntry, 0, len(ruleIDs))
	for _, ruleID := range ruleIDs {
		primaryEntry := primary[ruleID]
		secondaryEntry := secondary[ruleID]
		if primaryEntry.Verdict == secondaryEntry.Verdict {
			primaryEntry.VerdictPath = contracts.VerdictPathAgreement
			finalEntries = append(finalEntries, primaryEntry)
			continue
		}

		arbiterEntry, ok := arbiter[ruleID]
		if !ok {
			return nil, fmt.Errorf("step60: missing arbiter compliance rule=%s in raw reuse", ruleID)
		}
		arbiterEntry.VerdictPath = complianceVerdictPath(primaryEntry, secondaryEntry, arbiterEntry)
		finalEntries = append(finalEntries, arbiterEntry)
	}
	return finalEntries, nil
}

func rawComplianceEntriesAsFinal(rows []contracts.RawComplianceEntry) (map[string]contracts.ComplianceEntry, error) {
	final := make(map[string]contracts.ComplianceEntry, len(rows))
	for _, row := range rows {
		if _, exists := final[row.RuleID]; exists {
			return nil, fmt.Errorf("%w: rule_id=%s", ErrDuplicateComplianceRuleID, row.RuleID)
		}
		final[row.RuleID] = contracts.ComplianceEntry{
			SchemaVersion:        row.SchemaVersion,
			RunID:                row.RunID,
			Pass:                 row.Pass,
			Agent:                row.Agent,
			RuleID:               row.RuleID,
			Verdict:              row.Verdict,
			Rationale:            row.Rationale,
			RationaleOverflowRef: row.RationaleOverflowRef,
			RubricVersion:        row.RubricVersion,
			PromptVersion:        row.PromptVersion,
			ResolvedAt:           row.ResolvedAt,
		}
	}
	return final, nil
}

func rawRoleUsable(rows []contracts.RawScoreEntry, outputHash, rubricVersion, promptVersion string) bool {
	if len(rows) != len(canonicalDimensions) {
		return false
	}
	for _, row := range rows {
		if row.OutputSha256 != outputHash || row.RubricVersion != rubricVersion || row.PromptVersion != promptVersion {
			return false
		}
	}
	return true
}

func rawComplianceUsable(rows []contracts.RawComplianceEntry, expected map[string]struct{}, outputHash, rubricVersion, promptVersion string) bool {
	if len(expected) > 0 && len(rows) != len(expected) {
		return false
	}
	seen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		if row.OutputSha256 != outputHash || row.RubricVersion != rubricVersion || row.PromptVersion != promptVersion {
			return false
		}
		seen[row.RuleID] = struct{}{}
	}
	for ruleID := range expected {
		if _, ok := seen[ruleID]; !ok {
			return false
		}
	}
	return true
}

func rawArbiterComplianceUsable(rows []contracts.RawComplianceEntry, outputHash, rubricVersion, promptVersion string) bool {
	for _, row := range rows {
		if row.OutputSha256 != outputHash || row.RubricVersion != rubricVersion || row.PromptVersion != promptVersion {
			return false
		}
	}
	return true
}

func readJSONLOrEmpty[T any](path string) ([]T, error) {
	rows, err := internalio.ReadJSONL[T](path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return rows, nil
}
