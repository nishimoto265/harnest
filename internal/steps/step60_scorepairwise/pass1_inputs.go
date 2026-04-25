package step60_scorepairwise

import (
	"fmt"
	"os"
	"sort"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/judges"
	"github.com/nishimoto265/auto-improve/internal/steps/scorecore"
	"github.com/nishimoto265/auto-improve/internal/steps/step60contract"
)

func loadPass1Scores(runIO internalio.RunContext, rubricVersion, promptVersion string) (map[contracts.AgentID][]contracts.ScoreEntry, error) {
	path, err := runIO.ResolveRunRelative("30/scores-A.jsonl")
	if err != nil {
		return nil, fmt.Errorf("step60: resolve pass1 scores path: %w", err)
	}
	rows, err := internalio.ReadJSONL[contracts.ScoreEntry](path)
	if err != nil {
		return nil, fmt.Errorf("step60: read pass1 scores: %w", err)
	}
	// F8 + r17 F1: the pass1 final layer is append-only and CollapseByKey
	// authoritatively selects the latest row per (agent, dimension). Version
	// parity therefore has to be checked against the collapsed (effective)
	// view, not every historical row — otherwise a rubric/prompt bump that
	// triggers a step30 rerun would leave the now-superseded old-version
	// rows behind forever, and step60 would perma-fail with
	// ErrPass1VersionMismatch even though step30 produced fresh rows that
	// match. This preserves the F8 guarantee (pass1 and pass2 share scoring
	// assumptions) while honoring the append-only contract in
	// io-contracts.md §3.
	collapsed := internalio.CollapseByKey(rows, func(entry contracts.ScoreEntry) scoreKey {
		return scoreKey{Agent: entry.Agent, Dimension: entry.Dimension}
	})
	for _, row := range collapsed {
		if row.RubricVersion != rubricVersion || row.PromptVersion != promptVersion {
			return nil, fmt.Errorf(
				"%w: path=%s agent=%s dimension=%s pass1_rubric=%s pass1_prompt=%s step60_rubric=%s step60_prompt=%s",
				ErrPass1VersionMismatch, path, row.Agent, row.Dimension,
				row.RubricVersion, row.PromptVersion, rubricVersion, promptVersion,
			)
		}
	}
	byAgent := make(map[contracts.AgentID][]contracts.ScoreEntry, len(collapsed))
	for _, entry := range collapsed {
		byAgent[entry.Agent] = append(byAgent[entry.Agent], entry)
	}
	return byAgent, nil
}

func loadPass1ComplianceState(runIO internalio.RunContext, rubricVersion, promptVersion string) (pass1ComplianceState, error) {
	path, err := runIO.ResolveRunRelative("30/compliance-A.jsonl")
	if err != nil {
		return pass1ComplianceState{}, fmt.Errorf("step60: resolve pass1 compliance path: %w", err)
	}
	rows, err := internalio.ReadJSONL[contracts.ComplianceEntry](path)
	if err != nil {
		if os.IsNotExist(err) {
			return pass1ComplianceState{RuleIDs: map[contracts.AgentID]map[string]struct{}{}}, nil
		}
		return pass1ComplianceState{}, fmt.Errorf("step60: read pass1 compliance: %w", err)
	}
	collapsed := scorecore.CollapseFinalCompliance(rows)
	// F8 + r17 F1: verify parity on the collapsed (effective) rows only —
	// see loadPass1Scores above for the full rationale. CollapseFinalCompliance
	// keys by (agent, rule_id); old-version rows superseded by fresh step30
	// output are authoritatively dropped.
	for _, row := range collapsed {
		if row.RubricVersion != rubricVersion || row.PromptVersion != promptVersion {
			return pass1ComplianceState{}, fmt.Errorf(
				"%w: path=%s agent=%s rule_id=%s pass1_rubric=%s pass1_prompt=%s step60_rubric=%s step60_prompt=%s",
				ErrPass1VersionMismatch, path, row.Agent, row.RuleID,
				row.RubricVersion, row.PromptVersion, rubricVersion, promptVersion,
			)
		}
	}
	byAgent := make(map[contracts.AgentID]map[string]struct{}, len(collapsed))
	for _, entry := range collapsed {
		if _, ok := byAgent[entry.Agent]; !ok {
			byAgent[entry.Agent] = map[string]struct{}{}
		}
		byAgent[entry.Agent][entry.RuleID] = struct{}{}
	}
	sort.Slice(collapsed, func(i, j int) bool {
		if collapsed[i].Agent != collapsed[j].Agent {
			return collapsed[i].Agent < collapsed[j].Agent
		}
		return collapsed[i].RuleID < collapsed[j].RuleID
	})
	return pass1ComplianceState{RuleIDs: byAgent, Rows: collapsed}, nil
}

// expectedComplianceRuleIDsForAgent computes the rule-id set that pass2 raw
// compliance rows must cover for the given agent. The set is derived from the
// active rubric rules, current pass1 compliance rows, and candidate rules.
// Stub fallback rules are used only when no explicit coverage source exists.
// Rule IDs that only appear in existing step60 raw/final artifacts are stale.
func expectedComplianceRuleIDsForAgent(
	agent contracts.AgentID,
	pass1Rules map[contracts.AgentID]map[string]struct{},
	activeRules []string,
	fallbackRules []string,
	candidateRules []judges.CandidateRule,
) map[string]struct{} {
	return step60contract.ExpectedComplianceRuleIDsForAgent(agent, pass1Rules, activeRules, fallbackRules, candidateRules)
}

func sortedExpectedComplianceRuleIDs(rules map[string]struct{}) []string {
	return step60contract.SortedRuleIDs(rules)
}

func expectedComplianceRuleIDsByAgent(
	agents []contracts.AgentID,
	pass1Rules map[contracts.AgentID]map[string]struct{},
	activeRules []string,
	fallbackRules []string,
	candidateRules []judges.CandidateRule,
) map[contracts.AgentID]map[string]struct{} {
	return step60contract.ExpectedComplianceRuleIDsByAgent(agents, pass1Rules, activeRules, fallbackRules, candidateRules)
}

func pass2OutputHashesByAgent(runs []scorableAgentRun) (map[contracts.AgentID]string, error) {
	hashes := make(map[contracts.AgentID]string, len(runs))
	for _, run := range runs {
		if run.OutputSha256 == "" {
			return nil, fmt.Errorf("step60: missing snapshotted pass2 output hash for agent=%s", run.Agent)
		}
		hashes[run.Agent] = run.OutputSha256
	}
	return hashes, nil
}
