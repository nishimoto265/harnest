package orchestrator

import (
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
)

func stateRunID(entry contracts.StateEntry) (contracts.RunID, bool) {
	switch value := entry.Value.(type) {
	case contracts.StateEntryStarted:
		return value.RunID, true
	case *contracts.StateEntryStarted:
		if value != nil {
			return value.RunID, true
		}
	case contracts.StateEntryStepDone:
		return value.RunID, true
	case *contracts.StateEntryStepDone:
		if value != nil {
			return value.RunID, true
		}
	case contracts.StateEntryInterrupted:
		return value.RunID, true
	case *contracts.StateEntryInterrupted:
		if value != nil {
			return value.RunID, true
		}
	case contracts.StateEntryPromoting:
		return value.RunID, true
	case *contracts.StateEntryPromoting:
		if value != nil {
			return value.RunID, true
		}
	case contracts.StateEntryWarning:
		if value.RunID != nil {
			return *value.RunID, true
		}
	case *contracts.StateEntryWarning:
		if value != nil && value.RunID != nil {
			return *value.RunID, true
		}
	case contracts.StateEntryCompleted:
		return value.RunID, true
	case *contracts.StateEntryCompleted:
		if value != nil {
			return value.RunID, true
		}
	case contracts.StateEntryFailed:
		return value.RunID, true
	case *contracts.StateEntryFailed:
		if value != nil {
			return value.RunID, true
		}
	case contracts.StateEntryPromoted:
		return value.RunID, true
	case *contracts.StateEntryPromoted:
		if value != nil {
			return value.RunID, true
		}
	case contracts.StateEntryRollback:
		return value.RunID, true
	case *contracts.StateEntryRollback:
		if value != nil {
			return value.RunID, true
		}
	case contracts.StateEntrySkipped:
		return value.RunID, true
	case *contracts.StateEntrySkipped:
		if value != nil {
			return value.RunID, true
		}
	case contracts.StateEntryTimeout:
		return value.RunID, true
	case *contracts.StateEntryTimeout:
		if value != nil {
			return value.RunID, true
		}
	case contracts.StateEntryNeedsManualRecovery:
		return value.RunID, true
	case *contracts.StateEntryNeedsManualRecovery:
		if value != nil {
			return value.RunID, true
		}
	}
	return "", false
}

func startedEntry(pr int, runID contracts.RunID, at time.Time) contracts.StateEntry {
	value := contracts.StateEntryStarted{
		Kind:  contracts.StateKindStarted,
		PR:    pr,
		RunID: runID,
		Step:  contracts.FailedStep10,
		At:    at,
	}
	return contracts.StateEntry{Kind: contracts.StateKindStarted, Value: value}
}

func stepDoneEntry(pr int, runID contracts.RunID, step contracts.FailedStep, at time.Time) contracts.StateEntry {
	value := contracts.StateEntryStepDone{
		Kind:  contracts.StateKindStepDone,
		PR:    pr,
		RunID: runID,
		Step:  step,
		At:    at,
	}
	return contracts.StateEntry{Kind: contracts.StateKindStepDone, Value: value}
}

func timeoutEntry(pr int, runID contracts.RunID, step contracts.FailedStep, at time.Time) contracts.StateEntry {
	value := contracts.StateEntryTimeout{
		Kind:  contracts.StateKindTimeout,
		PR:    pr,
		RunID: runID,
		Step:  step,
		At:    at,
	}
	return contracts.StateEntry{Kind: contracts.StateKindTimeout, Value: value}
}

func completedEntry(pr int, runID contracts.RunID, step contracts.FailedStep, at time.Time) contracts.StateEntry {
	value := contracts.StateEntryCompleted{
		Kind:  contracts.StateKindCompleted,
		PR:    pr,
		RunID: runID,
		Step:  step,
		At:    at,
	}
	return contracts.StateEntry{Kind: contracts.StateKindCompleted, Value: value}
}

func failedEntry(pr int, runID contracts.RunID, step contracts.FailedStep, reason, detail string, at time.Time) contracts.StateEntry {
	value := contracts.StateEntryFailed{
		Kind:   contracts.StateKindFailed,
		PR:     pr,
		RunID:  runID,
		Step:   step,
		Reason: reason,
		Detail: detail,
		At:     at,
	}
	return contracts.StateEntry{Kind: value.Kind, Value: value}
}

func promotedEntry(pr int, runID contracts.RunID, at time.Time) contracts.StateEntry {
	value := contracts.StateEntryPromoted{
		Kind:  contracts.StateKindPromoted,
		PR:    pr,
		RunID: runID,
		Step:  contracts.FailedStep70,
		At:    at,
	}
	return contracts.StateEntry{Kind: contracts.StateKindPromoted, Value: value}
}

func rollbackEntry(pr int, runID contracts.RunID, reason contracts.RollbackReason, failedStep contracts.FailedStep, at time.Time) contracts.StateEntry {
	value := contracts.StateEntryRollback{
		Kind:           contracts.StateKindRollback,
		PR:             pr,
		RunID:          runID,
		Step:           contracts.FailedStep70,
		RollbackReason: reason,
		FailedStep:     failedStep,
		At:             at,
	}
	return contracts.StateEntry{Kind: contracts.StateKindRollback, Value: value}
}

func decisionRunID(decision contracts.Decision) (contracts.RunID, bool) {
	switch value := decision.Value.(type) {
	case contracts.DecisionAdopt:
		return value.RunID, true
	case *contracts.DecisionAdopt:
		if value != nil {
			return value.RunID, true
		}
	case contracts.DecisionRollback:
		return value.RunID, true
	case *contracts.DecisionRollback:
		if value != nil {
			return value.RunID, true
		}
	case contracts.DecisionNoop:
		return value.RunID, true
	case *contracts.DecisionNoop:
		if value != nil {
			return value.RunID, true
		}
	case contracts.DecisionReject:
		return value.RunID, true
	case *contracts.DecisionReject:
		if value != nil {
			return value.RunID, true
		}
	}
	return "", false
}
