package registryview

import (
	"fmt"

	"github.com/nishimoto265/auto-improve/internal/contracts"
)

type RuleState struct {
	RuleID           string
	RulePath         string
	Sha256           string
	Status           contracts.RuleStatus
	Exists           bool
	LastPromotionSeq int
}

type promotionSnapshot struct {
	ruleID      string
	previous    RuleState
	hadPrevious bool
	seq         int
}

func Build(entries []contracts.RuleRegistryEntry) (map[string]RuleState, error) {
	states := make(map[string]RuleState)
	history := make(map[string][]promotionSnapshot)

	for idx, entry := range entries {
		seq := idx + 1
		switch v := entry.Value.(type) {
		case contracts.RuleRegistryAdded:
			if err := contracts.ValidateRuleID(v.RuleID); err != nil {
				return nil, err
			}
			if err := contracts.ValidateRulePath(v.RulePath); err != nil {
				return nil, err
			}
			previous, hadPrevious, err := previousState(states, v.RuleID)
			if err != nil {
				return nil, err
			}
			history[v.IdempotencyKey] = append(history[v.IdempotencyKey], promotionSnapshot{
				ruleID:      v.RuleID,
				previous:    previous,
				hadPrevious: hadPrevious,
				seq:         seq,
			})
			states[v.RuleID] = RuleState{
				RuleID:           v.RuleID,
				RulePath:         v.RulePath,
				Sha256:           v.Sha256,
				Status:           contracts.RuleStatusActive,
				Exists:           true,
				LastPromotionSeq: seq,
			}
		case *contracts.RuleRegistryAdded:
			if v == nil {
				continue
			}
			if err := contracts.ValidateRuleID(v.RuleID); err != nil {
				return nil, err
			}
			if err := contracts.ValidateRulePath(v.RulePath); err != nil {
				return nil, err
			}
			previous, hadPrevious, err := previousState(states, v.RuleID)
			if err != nil {
				return nil, err
			}
			history[v.IdempotencyKey] = append(history[v.IdempotencyKey], promotionSnapshot{
				ruleID:      v.RuleID,
				previous:    previous,
				hadPrevious: hadPrevious,
				seq:         seq,
			})
			states[v.RuleID] = RuleState{
				RuleID:           v.RuleID,
				RulePath:         v.RulePath,
				Sha256:           v.Sha256,
				Status:           contracts.RuleStatusActive,
				Exists:           true,
				LastPromotionSeq: seq,
			}
		case contracts.RuleRegistryUpdated:
			if err := contracts.ValidateRuleID(v.RuleID); err != nil {
				return nil, err
			}
			if err := contracts.ValidateRulePath(v.RulePath); err != nil {
				return nil, err
			}
			previous, hadPrevious, err := previousState(states, v.RuleID)
			if err != nil {
				return nil, err
			}
			status := contracts.RuleStatusActive
			if hadPrevious && previous.Status != "" {
				status = previous.Status
			}
			history[v.IdempotencyKey] = append(history[v.IdempotencyKey], promotionSnapshot{
				ruleID:      v.RuleID,
				previous:    previous,
				hadPrevious: hadPrevious,
				seq:         seq,
			})
			states[v.RuleID] = RuleState{
				RuleID:           v.RuleID,
				RulePath:         v.RulePath,
				Sha256:           v.Sha256,
				Status:           status,
				Exists:           true,
				LastPromotionSeq: seq,
			}
		case *contracts.RuleRegistryUpdated:
			if v == nil {
				continue
			}
			if err := contracts.ValidateRuleID(v.RuleID); err != nil {
				return nil, err
			}
			if err := contracts.ValidateRulePath(v.RulePath); err != nil {
				return nil, err
			}
			previous, hadPrevious, err := previousState(states, v.RuleID)
			if err != nil {
				return nil, err
			}
			status := contracts.RuleStatusActive
			if hadPrevious && previous.Status != "" {
				status = previous.Status
			}
			history[v.IdempotencyKey] = append(history[v.IdempotencyKey], promotionSnapshot{
				ruleID:      v.RuleID,
				previous:    previous,
				hadPrevious: hadPrevious,
				seq:         seq,
			})
			states[v.RuleID] = RuleState{
				RuleID:           v.RuleID,
				RulePath:         v.RulePath,
				Sha256:           v.Sha256,
				Status:           status,
				Exists:           true,
				LastPromotionSeq: seq,
			}
		case contracts.RuleRegistryStatusChanged:
			state := states[v.RuleID]
			state.RuleID = v.RuleID
			state.Status = v.NewStatus
			state.Exists = v.NewStatus != contracts.RuleStatusArchived
			states[v.RuleID] = state
		case *contracts.RuleRegistryStatusChanged:
			if v == nil {
				continue
			}
			state := states[v.RuleID]
			state.RuleID = v.RuleID
			state.Status = v.NewStatus
			state.Exists = v.NewStatus != contracts.RuleStatusArchived
			states[v.RuleID] = state
		case contracts.RuleRegistryArchived:
			state := states[v.RuleID]
			state.RuleID = v.RuleID
			state.Status = contracts.RuleStatusArchived
			state.Exists = false
			states[v.RuleID] = state
		case *contracts.RuleRegistryArchived:
			if v == nil {
				continue
			}
			state := states[v.RuleID]
			state.RuleID = v.RuleID
			state.Status = contracts.RuleStatusArchived
			state.Exists = false
			states[v.RuleID] = state
		case contracts.RuleRegistryRestored:
			state := states[v.RuleID]
			state.RuleID = v.RuleID
			state.Status = v.NewStatus
			state.Exists = true
			states[v.RuleID] = state
		case *contracts.RuleRegistryRestored:
			if v == nil {
				continue
			}
			state := states[v.RuleID]
			state.RuleID = v.RuleID
			state.Status = v.NewStatus
			state.Exists = true
			states[v.RuleID] = state
		case contracts.RuleRegistryRolledBack:
			applyRollback(states, history, v.TargetOpID)
		case *contracts.RuleRegistryRolledBack:
			if v == nil {
				continue
			}
			applyRollback(states, history, v.TargetOpID)
		}
	}

	return states, nil
}

func Active(states map[string]RuleState) map[string]RuleState {
	active := make(map[string]RuleState)
	for ruleID, state := range states {
		if !state.Exists {
			continue
		}
		active[ruleID] = state
	}
	return active
}

func previousState(states map[string]RuleState, ruleID string) (RuleState, bool, error) {
	state, ok := states[ruleID]
	if !ok {
		return RuleState{}, false, nil
	}
	if err := contracts.ValidateRuleID(ruleID); err != nil {
		return RuleState{}, false, err
	}
	if state.RulePath != "" {
		if err := contracts.ValidateRulePath(state.RulePath); err != nil {
			return RuleState{}, false, err
		}
	}
	return state, true, nil
}

func applyRollback(states map[string]RuleState, history map[string][]promotionSnapshot, targetOpID string) {
	targets, ok := history[targetOpID]
	if !ok {
		return
	}
	for i := len(targets) - 1; i >= 0; i-- {
		target := targets[i]
		current, ok := states[target.ruleID]
		if !ok {
			continue
		}
		if current.LastPromotionSeq != target.seq {
			continue
		}
		if !target.hadPrevious {
			delete(states, target.ruleID)
			return
		}
		states[target.ruleID] = target.previous
		return
	}
}

func MustGet(states map[string]RuleState, ruleID string) (RuleState, error) {
	state, ok := states[ruleID]
	if !ok {
		return RuleState{}, fmt.Errorf("registryview: rule %s not found", ruleID)
	}
	return state, nil
}
