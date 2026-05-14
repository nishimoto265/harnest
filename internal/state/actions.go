package state

import (
	"sort"

	"github.com/nishimoto265/harnest/internal/contracts"
)

func ResumeTarget(entries []contracts.StateEntry) []ResumeRequest {
	grouped := make(map[int][]contracts.StateEntry)
	for _, entry := range entries {
		pr, ok := stateEntryPR(entry)
		if !ok {
			continue
		}
		grouped[pr] = append(grouped[pr], entry)
	}
	if len(grouped) == 0 {
		return nil
	}
	prs := make([]int, 0, len(grouped))
	for pr := range grouped {
		prs = append(prs, pr)
	}
	sort.Ints(prs)
	requests := make([]ResumeRequest, 0, len(prs))
	for _, pr := range prs {
		entry := latestActionEntry(grouped[pr])
		if entry == nil || NextActionForEntry(entry) != NextActionResume {
			continue
		}
		runID, ok := stateEntryRunID(*entry)
		if !ok {
			continue
		}
		step, ok := stateEntryStep(*entry)
		if !ok {
			continue
		}
		requests = append(requests, ResumeRequest{
			PR:    pr,
			RunID: runID,
			Step:  step,
		})
	}
	if len(requests) == 0 {
		return nil
	}
	return requests
}

func ClassifyNextAction(entries []contracts.StateEntry) NextAction {
	return NextActionForEntry(latestActionEntry(entries))
}

func classifyNextActionKind(kind contracts.StateKind) NextAction {
	switch kind {
	case contracts.StateKindStarted,
		contracts.StateKindStepDone,
		contracts.StateKindInterrupted,
		contracts.StateKindPromoting,
		contracts.StateKindWarningRegistrySizeHigh,
		contracts.StateKindWarningRegistrySizeCritical,
		contracts.StateKindWarningRescueRetry:
		return NextActionResume
	case contracts.StateKindNeedsManualRecovery:
		return NextActionNeedsManualRecovery
	default:
		return NextActionFreshStart
	}
}

func NextActionForEntry(entry *contracts.StateEntry) NextAction {
	if entry == nil {
		return NextActionFreshStart
	}
	return classifyNextActionKind(entry.Kind)
}
