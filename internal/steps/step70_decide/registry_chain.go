package step70_decide

import (
	"errors"
	"fmt"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
)

func deriveRegistryChain(entry contracts.RuleRegistryEntry, path string) (contracts.RuleRegistryEntry, error) {
	lines, err := readRegistryLines(path)
	if err != nil {
		return contracts.RuleRegistryEntry{}, err
	}
	prevHash := ""
	if len(lines) > 0 {
		prevHash = lines[len(lines)-1].Sha256
	}

	switch v := entry.Value.(type) {
	case contracts.RuleRegistryAdded:
		v.VersionSeq = nextRegistryVersion(lines)
		v.PrevHash = registryPrevHashForVersion(v.VersionSeq, prevHash)
		return contracts.RuleRegistryEntry{Kind: entry.Kind, Value: v}, nil
	case contracts.RuleRegistryUpdated:
		v.VersionSeq = nextRegistryVersion(lines)
		v.PrevHash = registryPrevHashForVersion(v.VersionSeq, prevHash)
		return contracts.RuleRegistryEntry{Kind: entry.Kind, Value: v}, nil
	case contracts.RuleRegistryRolledBack:
		v.VersionSeq = nextRegistryVersion(lines)
		v.PrevHash = registryPrevHashForVersion(v.VersionSeq, prevHash)
		return contracts.RuleRegistryEntry{Kind: entry.Kind, Value: v}, nil
	default:
		return entry, nil
	}
}

func registryPrevHashForVersion(versionSeq int64, prevHash string) string {
	if versionSeq == 1 {
		return ""
	}
	return prevHash
}

func plannedAdoptionFromRegistryEntries(intentionIdempotencyKey string, entries []contracts.RuleRegistryEntry) (*contracts.PlannedAdoption, error) {
	if len(entries) == 0 {
		return nil, errors.New("step70: adopt target must include at least one registry entry")
	}
	planned := &contracts.PlannedAdoption{
		IdempotencyKey: intentionIdempotencyKey,
		Entries:        make([]contracts.PlannedAdoptionEntry, 0, len(entries)),
	}
	for i, entry := range entries {
		switch v := entry.Value.(type) {
		case contracts.RuleRegistryAdded:
			planned.Entries = append(planned.Entries, contracts.PlannedAdoptionEntry{
				OpID:     contracts.ComputePlannedAdoptionEntryOpID(intentionIdempotencyKey, i, v.RuleID),
				Kind:     contracts.RegistryKindAdded,
				RuleID:   v.RuleID,
				RulePath: v.RulePath,
				Sha256:   v.Sha256,
			})
		case contracts.RuleRegistryUpdated:
			planned.Entries = append(planned.Entries, contracts.PlannedAdoptionEntry{
				OpID:       contracts.ComputePlannedAdoptionEntryOpID(intentionIdempotencyKey, i, v.RuleID),
				Kind:       contracts.RegistryKindUpdated,
				RuleID:     v.RuleID,
				RulePath:   v.RulePath,
				Sha256:     v.Sha256,
				PrevSha256: v.PrevSha256,
			})
		default:
			return nil, fmt.Errorf("step70: unsupported planned adoption registry kind=%q", entry.Kind)
		}
	}
	if err := planned.Validate(intentionIdempotencyKey); err != nil {
		return nil, err
	}
	return planned, nil
}

func registryEntriesFromPlannedAdoption(intention contracts.IntentionRecord, at time.Time) ([]contracts.RuleRegistryEntry, error) {
	if intention.PlannedAdoption == nil {
		return nil, contracts.ErrIntentionMissingPlannedAdoption
	}
	if err := intention.PlannedAdoption.Validate(intention.IdempotencyKey); err != nil {
		return nil, err
	}
	entries := make([]contracts.RuleRegistryEntry, 0, len(intention.PlannedAdoption.Entries))
	for _, planned := range intention.PlannedAdoption.Entries {
		switch planned.Kind {
		case contracts.RegistryKindAdded:
			entry := contracts.RuleRegistryAdded{
				Kind:           contracts.RegistryKindAdded,
				SchemaVersion:  "1",
				RuleID:         planned.RuleID,
				RulePath:       planned.RulePath,
				Sha256:         planned.Sha256,
				IdempotencyKey: planned.OpID,
				VersionSeq:     1,
				PrevHash:       "",
				ByRunID:        intention.RunID,
				At:             at,
			}
			entries = append(entries, contracts.RuleRegistryEntry{Kind: entry.Kind, Value: entry})
		case contracts.RegistryKindUpdated:
			entry := contracts.RuleRegistryUpdated{
				Kind:           contracts.RegistryKindUpdated,
				SchemaVersion:  "1",
				RuleID:         planned.RuleID,
				RulePath:       planned.RulePath,
				Sha256:         planned.Sha256,
				PrevSha256:     planned.PrevSha256,
				IdempotencyKey: planned.OpID,
				VersionSeq:     1,
				PrevHash:       "",
				ByRunID:        intention.RunID,
				At:             at,
			}
			entries = append(entries, contracts.RuleRegistryEntry{Kind: entry.Kind, Value: entry})
		default:
			return nil, fmt.Errorf("step70: unsupported planned adoption kind=%q", planned.Kind)
		}
	}
	return entries, nil
}

func targetFromIntention(pkg *contracts.TaskPackage, intention contracts.IntentionRecord) Target {
	bestBranch := ""
	if pkg != nil {
		bestBranch = pkg.BestBranch
	}
	return Target{
		BestBranch:    bestBranch,
		BestShaBefore: intention.BestShaBefore,
		TargetSHA:     intention.TargetSha,
		PolicyOnly:    intention.PolicyOnly,
	}
}

func nextRegistryVersion(lines []registryLine) int64 {
	if len(lines) == 0 {
		return 1
	}
	return registryVersionSeq(lines[len(lines)-1].Entry) + 1
}

func appendIfMissing(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func registryVersionSeq(entry contracts.RuleRegistryEntry) int64 {
	switch v := entry.Value.(type) {
	case contracts.RuleRegistryAdded:
		return v.VersionSeq
	case contracts.RuleRegistryUpdated:
		return v.VersionSeq
	case contracts.RuleRegistryRolledBack:
		return v.VersionSeq
	case contracts.RuleRegistryStatusChanged:
		return v.VersionSeq
	case contracts.RuleRegistryArchived:
		return v.VersionSeq
	case contracts.RuleRegistryRestored:
		return v.VersionSeq
	default:
		return 0
	}
}
