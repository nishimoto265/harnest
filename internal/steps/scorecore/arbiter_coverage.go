package scorecore

import (
	"fmt"
	"sort"

	"github.com/nishimoto265/auto-improve/internal/contracts"
)

// validateArbiterComplianceCoverage checks that the primary and secondary
// panel vote on the same compliance rule set (cardinality guard) and that
// the arbiter covers exactly the set of rules where the panel disagreed.
//
// The disputed-only shape matches the step60 contract: panel agreement
// rules finalize directly from primary/secondary, so arbiter rows outside
// the disputed set would be dead weight. Step30 shares this contract to
// avoid failing the whole agent when a future arbiter implementation
// (per Go実装計画.md) returns disputed-only rationale.
func validateArbiterComplianceCoverage(
	primary []contracts.RawComplianceEntry,
	secondary []contracts.RawComplianceEntry,
	arbiter []contracts.RawComplianceEntry,
) error {
	primaryIDs := uniqueSortedComplianceRuleIDs(primary)
	secondaryIDs := uniqueSortedComplianceRuleIDs(secondary)
	if !equalRuleIDSets(primaryIDs, secondaryIDs) {
		return fmt.Errorf(
			"%w: primary=%v secondary=%v",
			ErrPanelArbiterRuleCoverage,
			primaryIDs,
			secondaryIDs,
		)
	}
	disputed := disputedComplianceRuleIDsFromRaw(primary, secondary)
	arbiterIDs := uniqueSortedComplianceRuleIDs(arbiter)
	return validateArbiterCoversDisputed(disputed, arbiterIDs)
}

// validateArbiterCoversDisputed requires the arbiter rule-id set to cover
// every disputed rule. Extra arbiter rows (full-coverage arbiters, legacy
// stubs) are tolerated — they simply go unused during compliance
// finalization. Missing disputed rules fail closed.
func validateArbiterCoversDisputed(disputedRuleIDs, arbiterRuleIDs []string) error {
	arbiterSet := make(map[string]struct{}, len(arbiterRuleIDs))
	for _, id := range arbiterRuleIDs {
		arbiterSet[id] = struct{}{}
	}
	for _, id := range disputedRuleIDs {
		if _, ok := arbiterSet[id]; !ok {
			return fmt.Errorf(
				"%w: disputed=%v arbiter=%v",
				ErrPanelArbiterRuleCoverage,
				disputedRuleIDs,
				arbiterRuleIDs,
			)
		}
	}
	return nil
}

// ValidateArbiterComplianceRuleCoverage requires primary, secondary, and
// arbiter rule-id sets to match exactly. Step60 uses this strict helper for
// raw reuse, where accepting legacy full-coverage arbiter rows would bypass
// the current disputed-only arbiter contract.
func ValidateArbiterComplianceRuleCoverage(primaryRuleIDs, secondaryRuleIDs, arbiterRuleIDs []string) error {
	if !equalRuleIDSets(primaryRuleIDs, secondaryRuleIDs) {
		return fmt.Errorf(
			"%w: primary=%v secondary=%v",
			ErrPanelArbiterRuleCoverage,
			primaryRuleIDs,
			secondaryRuleIDs,
		)
	}
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

// disputedComplianceRuleIDsFromRaw returns the sorted rule-id set where the
// primary and secondary verdicts differ. Rules missing on one side are
// treated as implicit disagreement so the arbiter is required to break the
// tie.
func disputedComplianceRuleIDsFromRaw(primary, secondary []contracts.RawComplianceEntry) []string {
	primaryByRule := make(map[string]contracts.ComplianceVerdict, len(primary))
	for _, row := range primary {
		primaryByRule[row.RuleID] = row.Verdict
	}
	secondaryByRule := make(map[string]contracts.ComplianceVerdict, len(secondary))
	for _, row := range secondary {
		secondaryByRule[row.RuleID] = row.Verdict
	}
	seen := make(map[string]struct{})
	for ruleID, pv := range primaryByRule {
		sv, ok := secondaryByRule[ruleID]
		if !ok || pv != sv {
			seen[ruleID] = struct{}{}
		}
	}
	for ruleID := range secondaryByRule {
		if _, ok := primaryByRule[ruleID]; !ok {
			seen[ruleID] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for ruleID := range seen {
		out = append(out, ruleID)
	}
	sort.Strings(out)
	return out
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
