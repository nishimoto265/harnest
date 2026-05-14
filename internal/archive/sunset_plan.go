package archive

import (
	"errors"
	"path/filepath"
	"sort"

	"github.com/nishimoto265/harnest/internal/contracts"
	"github.com/nishimoto265/harnest/internal/registryview"
)

func BuildTransitionPlan(runsBase string) ([]Transition, error) {
	if runsBase == "" {
		return nil, errors.New("archive: runs_base is required")
	}
	registryPath := filepath.Join(runsBase, "rules-registry.jsonl")
	lines, err := readRegistryLines(registryPath)
	if err != nil {
		return nil, err
	}
	entries := make([]contracts.RuleRegistryEntry, 0, len(lines))
	for _, line := range lines {
		entries = append(entries, line.Entry)
	}
	states, err := registryview.Build(entries)
	if err != nil {
		return nil, err
	}
	ruleIDs := make([]string, 0, len(states))
	for ruleID, state := range states {
		if !state.Exists || state.Status != contracts.RuleStatusDeprecated {
			continue
		}
		ruleIDs = append(ruleIDs, ruleID)
	}
	sort.Strings(ruleIDs)

	transitions := make([]Transition, 0, len(ruleIDs))
	for _, ruleID := range ruleIDs {
		state := states[ruleID]
		transitions = append(transitions, Transition{
			RuleID:     ruleID,
			PrevStatus: state.Status,
			NewStatus:  contracts.RuleStatusArchived,
			Kind:       contracts.RegistryKindArchived,
			Transition: contracts.SunsetTransitionArchive,
		})
	}
	return transitions, nil
}

func transitionKey(t Transition) string {
	if t.Transition != "" {
		return string(t.Transition)
	}
	switch t.Kind {
	case contracts.RegistryKindArchived:
		return string(contracts.SunsetTransitionArchive)
	case contracts.RegistryKindRestored:
		if t.NewStatus == contracts.RuleStatusActive {
			return string(contracts.SunsetTransitionActivate)
		}
		return string(contracts.SunsetTransitionDeprecate)
	default:
		return string(t.Transition)
	}
}
