package step60_scorepairwise

import (
	"sort"
	"time"

	"github.com/nishimoto265/harnest/internal/contracts"
)

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func finalizeScore(meta finalMetadata, score contracts.ScoreEntry, path contracts.VerdictPath) contracts.ScoreEntry {
	score.VerdictPath = path
	score.RubricVersion = meta.RubricVersion
	score.PromptVersion = meta.PromptVersion
	score.ResolvedAt = meta.ResolvedAt
	return score
}

func finalizeCompliance(meta finalMetadata, entry contracts.ComplianceEntry, path contracts.VerdictPath) contracts.ComplianceEntry {
	entry.VerdictPath = path
	entry.RubricVersion = meta.RubricVersion
	entry.PromptVersion = meta.PromptVersion
	entry.ResolvedAt = meta.ResolvedAt
	return entry
}

func complianceEntryOrMissed(
	meta finalMetadata,
	agent contracts.AgentID,
	ruleID string,
	entry contracts.ComplianceEntry,
	ok bool,
) contracts.ComplianceEntry {
	if ok {
		return entry
	}
	return contracts.ComplianceEntry{
		SchemaVersion: "1",
		RunID:         meta.RunID,
		Pass:          meta.Pass,
		Agent:         agent,
		RuleID:        ruleID,
		Verdict:       contracts.ComplianceVerdictMissed,
		RubricVersion: meta.RubricVersion,
		PromptVersion: meta.PromptVersion,
		ResolvedAt:    meta.ResolvedAt,
	}
}

func preferredComplianceAgreementSource(
	primary contracts.ComplianceEntry,
	secondary contracts.ComplianceEntry,
	primaryOK bool,
	secondaryOK bool,
) contracts.ComplianceEntry {
	if primaryOK {
		return primary
	}
	if secondaryOK {
		return secondary
	}
	return primary
}

func preferredComplianceSingleSource(
	primary contracts.ComplianceEntry,
	secondary contracts.ComplianceEntry,
	primaryOK bool,
	secondaryOK bool,
) contracts.ComplianceEntry {
	if primaryOK && primary.Verdict != contracts.ComplianceVerdictMissed {
		return primary
	}
	if secondaryOK && secondary.Verdict != contracts.ComplianceVerdictMissed {
		return secondary
	}
	if primaryOK {
		return primary
	}
	return secondary
}

func makeRawScoreEntry(
	score contracts.ScoreEntry,
	role contracts.JudgeRole,
	outputHash string,
	primaryRef *contracts.RawJudgeRef,
	secondaryRef *contracts.RawJudgeRef,
	resolvedAt time.Time,
) contracts.RawScoreEntry {
	return contracts.RawScoreEntry{
		SchemaVersion:      "1",
		RunID:              score.RunID,
		Pass:               score.Pass,
		Agent:              score.Agent,
		JudgeRole:          role,
		Dimension:          score.Dimension,
		Score:              score.Score,
		Reasons:            score.Reasons,
		ReasonsOverflowRef: score.ReasonsOverflowRef,
		OutputSha256:       outputHash,
		PrimaryRef:         primaryRef,
		SecondaryRef:       secondaryRef,
		RubricVersion:      score.RubricVersion,
		PromptVersion:      score.PromptVersion,
		ResolvedAt:         resolvedAt,
	}
}

func makeRawComplianceEntry(
	entry contracts.ComplianceEntry,
	role contracts.JudgeRole,
	outputHash string,
	primaryRef *contracts.RawJudgeRef,
	secondaryRef *contracts.RawJudgeRef,
	resolvedAt time.Time,
) contracts.RawComplianceEntry {
	return contracts.RawComplianceEntry{
		SchemaVersion:        "1",
		RunID:                entry.RunID,
		Pass:                 entry.Pass,
		Agent:                entry.Agent,
		JudgeRole:            role,
		RuleID:               entry.RuleID,
		Verdict:              entry.Verdict,
		Rationale:            entry.Rationale,
		RationaleOverflowRef: entry.RationaleOverflowRef,
		OutputSha256:         outputHash,
		PrimaryRef:           primaryRef,
		SecondaryRef:         secondaryRef,
		RubricVersion:        entry.RubricVersion,
		PromptVersion:        entry.PromptVersion,
		ResolvedAt:           resolvedAt,
	}
}

func complianceVerdictPath(primary, secondary, arbiter contracts.ComplianceEntry) contracts.VerdictPath {
	if primary.Verdict == secondary.Verdict {
		return contracts.VerdictPathAgreement
	}
	if arbiter.Verdict == primary.Verdict || arbiter.Verdict == secondary.Verdict {
		return contracts.VerdictPathArbitrated
	}
	return contracts.VerdictPathArbiterOverruled
}

func complianceRuleIDs(
	primary map[string]contracts.ComplianceEntry,
	secondary map[string]contracts.ComplianceEntry,
) []string {
	set := make(map[string]struct{}, len(primary)+len(secondary))
	for ruleID := range primary {
		set[ruleID] = struct{}{}
	}
	for ruleID := range secondary {
		set[ruleID] = struct{}{}
	}
	ruleIDs := make([]string, 0, len(set))
	for ruleID := range set {
		ruleIDs = append(ruleIDs, ruleID)
	}
	sort.Strings(ruleIDs)
	return ruleIDs
}

func disputedComplianceRuleIDs(primary, secondary map[string]contracts.ComplianceEntry) []string {
	ruleIDs := make([]string, 0, minInt(len(primary), len(secondary)))
	for ruleID, primaryEntry := range primary {
		secondaryEntry, ok := secondary[ruleID]
		if !ok || primaryEntry.Verdict == secondaryEntry.Verdict {
			continue
		}
		ruleIDs = append(ruleIDs, ruleID)
	}
	sort.Strings(ruleIDs)
	return ruleIDs
}

func sortedComplianceRuleIDs(entries map[string]contracts.ComplianceEntry) []string {
	ruleIDs := make([]string, 0, len(entries))
	for ruleID := range entries {
		ruleIDs = append(ruleIDs, ruleID)
	}
	sort.Strings(ruleIDs)
	return ruleIDs
}
