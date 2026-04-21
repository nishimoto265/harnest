package scorecore

import (
	"fmt"
	"sort"

	"github.com/nishimoto265/auto-improve/internal/contracts"
)

func validateArbiterComplianceCoverage(
	primary, secondary, arbiter []contracts.RawComplianceEntry,
) error {
	primaryRuleIDs := complianceRuleIDSet(primary)
	secondaryRuleIDs := complianceRuleIDSet(secondary)
	if !sameRuleIDSet(primaryRuleIDs, secondaryRuleIDs) {
		return fmt.Errorf(
			"%w: primary=%v secondary=%v",
			ErrPanelArbiterRuleCoverage,
			sortedRuleIDs(primaryRuleIDs),
			sortedRuleIDs(secondaryRuleIDs),
		)
	}

	arbiterRuleIDs := complianceRuleIDSet(arbiter)
	if !sameRuleIDSet(primaryRuleIDs, arbiterRuleIDs) {
		return fmt.Errorf(
			"%w: primary=%v arbiter=%v",
			ErrPanelArbiterRuleCoverage,
			sortedRuleIDs(primaryRuleIDs),
			sortedRuleIDs(arbiterRuleIDs),
		)
	}
	return nil
}

func complianceRuleIDSet(rows []contracts.RawComplianceEntry) map[string]struct{} {
	ruleIDs := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		ruleIDs[row.RuleID] = struct{}{}
	}
	return ruleIDs
}

func sameRuleIDSet(left, right map[string]struct{}) bool {
	if len(left) != len(right) {
		return false
	}
	for ruleID := range left {
		if _, ok := right[ruleID]; !ok {
			return false
		}
	}
	return true
}

func sortedRuleIDs(ruleIDs map[string]struct{}) []string {
	out := make([]string, 0, len(ruleIDs))
	for ruleID := range ruleIDs {
		out = append(out, ruleID)
	}
	sort.Strings(out)
	return out
}
