package orchestrator

import (
	"fmt"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
)

func validatePersistedRunScopedArtifacts(runCtx internalio.RunContext) error {
	candidatesPath, err := runCtx.ResolveRunRelative("40/candidates.json")
	if err != nil {
		return err
	}
	if fileExists(candidatesPath) {
		if _, err := readCandidatesForRun(candidatesPath, runCtx.RunID); err != nil {
			return err
		}
	}
	decisionPath, err := runCtx.ResolveRunRelative("70/decision.json")
	if err != nil {
		return err
	}
	if fileExists(decisionPath) {
		if _, err := readDecisionForRun(decisionPath, runCtx.RunID); err != nil {
			return err
		}
	}
	intentionPath, err := runCtx.ResolveRunRelative("70/intention.json")
	if err != nil {
		return err
	}
	if fileExists(intentionPath) {
		if _, err := readIntentionRecordForRun(intentionPath, runCtx.RunID); err != nil {
			return err
		}
	}
	return nil
}

func readCandidatesForRun(path string, runID contracts.RunID) (contracts.Candidates, error) {
	candidates, err := internalio.ReadJSON[contracts.Candidates](path)
	if err != nil {
		return contracts.Candidates{}, err
	}
	if candidates.RunID != runID {
		return contracts.Candidates{}, fmt.Errorf("orchestrator: candidates run_id mismatch: expected=%s got=%s", runID, candidates.RunID)
	}
	return candidates, nil
}

func readDecisionForRun(path string, runID contracts.RunID) (contracts.Decision, error) {
	decision, err := internalio.ReadJSON[contracts.Decision](path)
	if err != nil {
		return contracts.Decision{}, err
	}
	if decisionRunID, ok := decisionRunID(decision); ok && decisionRunID != runID {
		return contracts.Decision{}, fmt.Errorf("orchestrator: decision run_id mismatch: expected=%s got=%s", runID, decisionRunID)
	}
	return decision, nil
}

func readIntentionRecordForRun(path string, runID contracts.RunID) (contracts.IntentionRecord, error) {
	record, err := internalio.ReadJSON[contracts.IntentionRecord](path)
	if err != nil {
		return contracts.IntentionRecord{}, err
	}
	if record.RunID != runID {
		return contracts.IntentionRecord{}, fmt.Errorf("orchestrator: intention run_id mismatch: expected=%s got=%s", runID, record.RunID)
	}
	return record, nil
}
