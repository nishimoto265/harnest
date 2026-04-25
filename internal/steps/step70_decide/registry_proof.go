package step70_decide

import (
	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
)

type plannedRegistryMatch struct {
	EntryIndex int
	OpID       string
	Result     contracts.RegistryAppendResult
}

func findPlannedRegistryMatches(runCtx internalio.RunContext, intention contracts.IntentionRecord) ([]*plannedRegistryMatch, error) {
	if intention.PlannedAdoption == nil {
		return nil, contracts.ErrIntentionMissingPlannedAdoption
	}
	if err := intention.PlannedAdoption.Validate(intention.IdempotencyKey); err != nil {
		return nil, err
	}
	lines, err := registryLookupLines(runCtx)
	if err != nil {
		return nil, err
	}
	matches := make([]*plannedRegistryMatch, len(intention.PlannedAdoption.Entries))
	wanted := make(map[string]int, len(intention.PlannedAdoption.Entries))
	for idx, entry := range intention.PlannedAdoption.Entries {
		wanted[entry.OpID] = idx
	}
	for i := len(lines) - 1; i >= 0; i-- {
		switch v := lines[i].Entry.Value.(type) {
		case contracts.RuleRegistryAdded:
			if idx, ok := wanted[v.IdempotencyKey]; ok {
				if err := plannedRegistryEntryMatches(intention.PlannedAdoption.Entries[idx], lines[i].Entry); err != nil {
					return nil, err
				}
				if matches[idx] != nil {
					continue
				}
				matches[idx] = &plannedRegistryMatch{
					EntryIndex: idx,
					OpID:       v.IdempotencyKey,
					Result:     contracts.RegistryAppendResult{Offset: lines[i].Offset, Sha256: lines[i].Sha256},
				}
			}
		case contracts.RuleRegistryUpdated:
			if idx, ok := wanted[v.IdempotencyKey]; ok {
				if err := plannedRegistryEntryMatches(intention.PlannedAdoption.Entries[idx], lines[i].Entry); err != nil {
					return nil, err
				}
				if matches[idx] != nil {
					continue
				}
				matches[idx] = &plannedRegistryMatch{
					EntryIndex: idx,
					OpID:       v.IdempotencyKey,
					Result:     contracts.RegistryAppendResult{Offset: lines[i].Offset, Sha256: lines[i].Sha256},
				}
			}
		}
	}
	return matches, nil
}

func plannedRegistryEntryMatches(planned contracts.PlannedAdoptionEntry, entry contracts.RuleRegistryEntry) error {
	switch v := entry.Value.(type) {
	case contracts.RuleRegistryAdded:
		if planned.Kind != contracts.RegistryKindAdded || planned.RuleID != v.RuleID || planned.RulePath != v.RulePath || planned.Sha256 != v.Sha256 || planned.PrevSha256 != "" {
			return ErrRegistryDivergence
		}
	case contracts.RuleRegistryUpdated:
		if planned.Kind != contracts.RegistryKindUpdated || planned.RuleID != v.RuleID || planned.RulePath != v.RulePath || planned.Sha256 != v.Sha256 || planned.PrevSha256 != v.PrevSha256 {
			return ErrRegistryDivergence
		}
	default:
		return ErrRegistryDivergence
	}
	return nil
}

func completePlannedRegistryMatches(matches []*plannedRegistryMatch) (contracts.RegistryAppendResult, bool) {
	if len(matches) == 0 {
		return contracts.RegistryAppendResult{}, false
	}
	var last contracts.RegistryAppendResult
	for _, match := range matches {
		if match == nil {
			return contracts.RegistryAppendResult{}, false
		}
		last = match.Result
	}
	return last, true
}

func verifyPlannedRegistryAppendProof(runCtx internalio.RunContext, intention contracts.IntentionRecord) error {
	if intention.RegistryAppendResult == nil {
		return ErrRegistryDivergence
	}
	result, err := plannedRegistryAppendResult(runCtx, intention)
	if err != nil {
		return err
	}
	if result.Offset != intention.RegistryAppendResult.Offset || result.Sha256 != intention.RegistryAppendResult.Sha256 {
		return ErrRegistryDivergence
	}
	return nil
}

func plannedRegistryAppendResult(runCtx internalio.RunContext, intention contracts.IntentionRecord) (contracts.RegistryAppendResult, error) {
	matches, err := findPlannedRegistryMatches(runCtx, intention)
	if err != nil {
		return contracts.RegistryAppendResult{}, err
	}
	result, ok := completePlannedRegistryMatches(matches)
	if !ok {
		return contracts.RegistryAppendResult{}, ErrRegistryDivergence
	}
	return result, nil
}

func resumeIndexForPlannedRegistryEntries(intention contracts.IntentionRecord, matches []*plannedRegistryMatch, currentHead string) (int, error) {
	prefixLen, contiguous := plannedRegistryPrefixLen(matches)
	if !contiguous {
		return 0, ErrRegistryDivergence
	}
	if prefixLen == 0 {
		if currentHead != intention.RegistryHeadBefore {
			return 0, ErrRegistryDivergence
		}
		return 0, nil
	}
	if currentHead != matches[prefixLen-1].Result.Sha256 {
		return 0, ErrRegistryDivergence
	}
	return prefixLen, nil
}

func plannedRegistryPrefixLen(matches []*plannedRegistryMatch) (int, bool) {
	prefixLen := 0
	for prefixLen < len(matches) && matches[prefixLen] != nil {
		prefixLen++
	}
	for i := prefixLen; i < len(matches); i++ {
		if matches[i] != nil {
			return prefixLen, false
		}
	}
	return prefixLen, true
}

func committedPromotionEntries(runCtx internalio.RunContext, intention contracts.IntentionRecord) ([]plannedRegistryMatch, error) {
	matches, err := findPlannedRegistryMatches(runCtx, intention)
	if err != nil {
		return nil, err
	}
	committed := make([]plannedRegistryMatch, 0, len(matches))
	for _, match := range matches {
		if match != nil {
			committed = append(committed, *match)
		}
	}
	return committed, nil
}
