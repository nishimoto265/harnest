package state

import "github.com/nishimoto265/harnest/internal/contracts"

func stateEntryPR(entry contracts.StateEntry) (int, bool) {
	switch value := entry.Value.(type) {
	case contracts.StateEntryStarted:
		return value.PR, true
	case *contracts.StateEntryStarted:
		return derefPR(value, func(v *contracts.StateEntryStarted) int { return v.PR })
	case contracts.StateEntryStepDone:
		return value.PR, true
	case *contracts.StateEntryStepDone:
		return derefPR(value, func(v *contracts.StateEntryStepDone) int { return v.PR })
	case contracts.StateEntryInterrupted:
		return value.PR, true
	case *contracts.StateEntryInterrupted:
		return derefPR(value, func(v *contracts.StateEntryInterrupted) int { return v.PR })
	case contracts.StateEntryPromoting:
		return value.PR, true
	case *contracts.StateEntryPromoting:
		return derefPR(value, func(v *contracts.StateEntryPromoting) int { return v.PR })
	case contracts.StateEntryWarning:
		if value.PR == nil {
			return 0, false
		}
		return *value.PR, true
	case *contracts.StateEntryWarning:
		if value == nil || value.PR == nil {
			return 0, false
		}
		return *value.PR, true
	case contracts.StateEntryCompleted:
		return value.PR, true
	case *contracts.StateEntryCompleted:
		return derefPR(value, func(v *contracts.StateEntryCompleted) int { return v.PR })
	case contracts.StateEntryFailed:
		return value.PR, true
	case *contracts.StateEntryFailed:
		return derefPR(value, func(v *contracts.StateEntryFailed) int { return v.PR })
	case contracts.StateEntryPromoted:
		return value.PR, true
	case *contracts.StateEntryPromoted:
		return derefPR(value, func(v *contracts.StateEntryPromoted) int { return v.PR })
	case contracts.StateEntryRollback:
		return value.PR, true
	case *contracts.StateEntryRollback:
		return derefPR(value, func(v *contracts.StateEntryRollback) int { return v.PR })
	case contracts.StateEntrySkipped:
		return value.PR, true
	case *contracts.StateEntrySkipped:
		return derefPR(value, func(v *contracts.StateEntrySkipped) int { return v.PR })
	case contracts.StateEntryTimeout:
		return value.PR, true
	case *contracts.StateEntryTimeout:
		return derefPR(value, func(v *contracts.StateEntryTimeout) int { return v.PR })
	case contracts.StateEntryNeedsManualRecovery:
		return value.PR, true
	case *contracts.StateEntryNeedsManualRecovery:
		return derefPR(value, func(v *contracts.StateEntryNeedsManualRecovery) int { return v.PR })
	default:
		return 0, false
	}
}

func EntryPR(entry contracts.StateEntry) (int, bool) {
	return stateEntryPR(entry)
}

func stateEntryRunID(entry contracts.StateEntry) (contracts.RunID, bool) {
	switch value := entry.Value.(type) {
	case contracts.StateEntryStarted:
		return value.RunID, true
	case *contracts.StateEntryStarted:
		return derefRunID(value, func(v *contracts.StateEntryStarted) contracts.RunID { return v.RunID })
	case contracts.StateEntryStepDone:
		return value.RunID, true
	case *contracts.StateEntryStepDone:
		return derefRunID(value, func(v *contracts.StateEntryStepDone) contracts.RunID { return v.RunID })
	case contracts.StateEntryInterrupted:
		return value.RunID, true
	case *contracts.StateEntryInterrupted:
		return derefRunID(value, func(v *contracts.StateEntryInterrupted) contracts.RunID { return v.RunID })
	case contracts.StateEntryPromoting:
		return value.RunID, true
	case *contracts.StateEntryPromoting:
		return derefRunID(value, func(v *contracts.StateEntryPromoting) contracts.RunID { return v.RunID })
	case contracts.StateEntryWarning:
		if value.RunID == nil {
			return "", false
		}
		return *value.RunID, true
	case *contracts.StateEntryWarning:
		if value == nil || value.RunID == nil {
			return "", false
		}
		return *value.RunID, true
	case contracts.StateEntryCompleted:
		return value.RunID, true
	case *contracts.StateEntryCompleted:
		return derefRunID(value, func(v *contracts.StateEntryCompleted) contracts.RunID { return v.RunID })
	case contracts.StateEntryFailed:
		return value.RunID, true
	case *contracts.StateEntryFailed:
		return derefRunID(value, func(v *contracts.StateEntryFailed) contracts.RunID { return v.RunID })
	case contracts.StateEntryPromoted:
		return value.RunID, true
	case *contracts.StateEntryPromoted:
		return derefRunID(value, func(v *contracts.StateEntryPromoted) contracts.RunID { return v.RunID })
	case contracts.StateEntryRollback:
		return value.RunID, true
	case *contracts.StateEntryRollback:
		return derefRunID(value, func(v *contracts.StateEntryRollback) contracts.RunID { return v.RunID })
	case contracts.StateEntrySkipped:
		return value.RunID, true
	case *contracts.StateEntrySkipped:
		return derefRunID(value, func(v *contracts.StateEntrySkipped) contracts.RunID { return v.RunID })
	case contracts.StateEntryTimeout:
		return value.RunID, true
	case *contracts.StateEntryTimeout:
		return derefRunID(value, func(v *contracts.StateEntryTimeout) contracts.RunID { return v.RunID })
	case contracts.StateEntryNeedsManualRecovery:
		return value.RunID, true
	case *contracts.StateEntryNeedsManualRecovery:
		return derefRunID(value, func(v *contracts.StateEntryNeedsManualRecovery) contracts.RunID { return v.RunID })
	default:
		return "", false
	}
}

func stateEntryStep(entry contracts.StateEntry) (contracts.FailedStep, bool) {
	switch value := entry.Value.(type) {
	case contracts.StateEntryStarted:
		return value.Step, true
	case *contracts.StateEntryStarted:
		return derefStep(value, func(v *contracts.StateEntryStarted) contracts.FailedStep { return v.Step })
	case contracts.StateEntryStepDone:
		return value.Step, true
	case *contracts.StateEntryStepDone:
		return derefStep(value, func(v *contracts.StateEntryStepDone) contracts.FailedStep { return v.Step })
	case contracts.StateEntryInterrupted:
		return value.Step, true
	case *contracts.StateEntryInterrupted:
		return derefStep(value, func(v *contracts.StateEntryInterrupted) contracts.FailedStep { return v.Step })
	case contracts.StateEntryPromoting:
		return value.Step, true
	case *contracts.StateEntryPromoting:
		return derefStep(value, func(v *contracts.StateEntryPromoting) contracts.FailedStep { return v.Step })
	case contracts.StateEntryWarning:
		if value.Step == nil {
			return "", false
		}
		return *value.Step, true
	case *contracts.StateEntryWarning:
		if value == nil || value.Step == nil {
			return "", false
		}
		return *value.Step, true
	case contracts.StateEntryCompleted:
		return value.Step, true
	case *contracts.StateEntryCompleted:
		return derefStep(value, func(v *contracts.StateEntryCompleted) contracts.FailedStep { return v.Step })
	case contracts.StateEntryFailed:
		return value.Step, true
	case *contracts.StateEntryFailed:
		return derefStep(value, func(v *contracts.StateEntryFailed) contracts.FailedStep { return v.Step })
	case contracts.StateEntryPromoted:
		return value.Step, true
	case *contracts.StateEntryPromoted:
		return derefStep(value, func(v *contracts.StateEntryPromoted) contracts.FailedStep { return v.Step })
	case contracts.StateEntryRollback:
		return value.Step, true
	case *contracts.StateEntryRollback:
		return derefStep(value, func(v *contracts.StateEntryRollback) contracts.FailedStep { return v.Step })
	case contracts.StateEntrySkipped:
		return value.Step, true
	case *contracts.StateEntrySkipped:
		return derefStep(value, func(v *contracts.StateEntrySkipped) contracts.FailedStep { return v.Step })
	case contracts.StateEntryTimeout:
		return value.Step, true
	case *contracts.StateEntryTimeout:
		return derefStep(value, func(v *contracts.StateEntryTimeout) contracts.FailedStep { return v.Step })
	case contracts.StateEntryNeedsManualRecovery:
		return value.Step, true
	case *contracts.StateEntryNeedsManualRecovery:
		return derefStep(value, func(v *contracts.StateEntryNeedsManualRecovery) contracts.FailedStep { return v.Step })
	default:
		return "", false
	}
}

func derefPR[T any](value *T, fn func(*T) int) (int, bool) {
	if value == nil {
		return 0, false
	}
	return fn(value), true
}

func derefRunID[T any](value *T, fn func(*T) contracts.RunID) (contracts.RunID, bool) {
	if value == nil {
		return "", false
	}
	return fn(value), true
}

func derefStep[T any](value *T, fn func(*T) contracts.FailedStep) (contracts.FailedStep, bool) {
	if value == nil {
		return "", false
	}
	return fn(value), true
}
