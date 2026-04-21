package scorecore

import (
	"fmt"
	"sort"

	"github.com/nishimoto265/auto-improve/internal/contracts"
)

func validateArbiterComplianceCoverage(
	primary []contracts.RawComplianceEntry,
	secondary []contracts.RawComplianceEntry,
	arbiter []contracts.RawComplianceEntry,
) error {
	primaryRuleIDs := uniqueSortedComplianceRuleIDs(primary)
	secondaryRuleIDs := uniqueSortedComplianceRuleIDs(secondary)
	if !equalRuleIDSets(primaryRuleIDs, secondaryRuleIDs) {
		return fmt.Errorf(
			"%w: primary=%v secondary=%v",
			ErrPanelArbiterRuleCoverage,
			primaryRuleIDs,
			secondaryRuleIDs,
		)
	}

	arbiterRuleIDs := uniqueSortedComplianceRuleIDs(arbiter)
	if !equalRuleIDSets(primaryRuleIDs, arbiterRuleIDs) {
		return fmt.Errorf(
			"%w: expected=%v arbiter=%v",
			ErrPanelArbiterRuleCoverage,
			primaryRuleIDs,
			arbiterRuleIDs,
		)
	}
	return nil
}

func uniqueSortedComplianceRuleIDs(rows []contracts.RawComplianceEntry) []string {
	ruleIDSet := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		ruleIDSet[row.RuleID] = struct{}{}
	}

	ruleIDs := make([]string, 0, len(ruleIDSet))
	for ruleID := range ruleIDSet {
		ruleIDs = append(ruleIDs, ruleID)
	}
	sort.Strings(ruleIDs)
	return ruleIDs
}

func equalRuleIDSets(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
