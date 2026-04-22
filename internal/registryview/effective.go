package registryview

import (
	"crypto/sha256"
	"encoding/hex"
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
	offset      int64
	sha256      string
}

func Build(entries []contracts.RuleRegistryEntry) (map[string]RuleState, error) {
	states := make(map[string]RuleState)
	history := make(map[string][]promotionSnapshot)
	var offset int64

	for idx, entry := range entries {
		seq := idx + 1
		result, nextOffset, err := registryAppendResult(entry, offset)
		if err != nil {
			return nil, err
		}
		offset = nextOffset
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
				offset:      result.Offset,
				sha256:      result.Sha256,
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
				offset:      result.Offset,
				sha256:      result.Sha256,
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
			if !hadPrevious || previous.Sha256 != v.PrevSha256 {
				return nil, fmt.Errorf("registryview: update prev_sha256 mismatch: rule_id=%s prev_sha256=%s effective_sha256=%s", v.RuleID, v.PrevSha256, previous.Sha256)
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
				offset:      result.Offset,
				sha256:      result.Sha256,
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
			if !hadPrevious || previous.Sha256 != v.PrevSha256 {
				return nil, fmt.Errorf("registryview: update prev_sha256 mismatch: rule_id=%s prev_sha256=%s effective_sha256=%s", v.RuleID, v.PrevSha256, previous.Sha256)
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
				offset:      result.Offset,
				sha256:      result.Sha256,
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
			if err := applyRollback(states, history, v.TargetOpID, v.TargetOffset, v.TargetSha256); err != nil {
				return nil, err
			}
		case *contracts.RuleRegistryRolledBack:
			if v == nil {
				continue
			}
			if err := applyRollback(states, history, v.TargetOpID, v.TargetOffset, v.TargetSha256); err != nil {
				return nil, err
			}
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

func applyRollback(states map[string]RuleState, history map[string][]promotionSnapshot, targetOpID string, targetOffset int64, targetSha256 string) error {
	targets, ok := history[targetOpID]
	if !ok {
		return fmt.Errorf("registryview: rollback target op_id not found: op_id=%s", targetOpID)
	}
	for i := len(targets) - 1; i >= 0; i-- {
		target := targets[i]
		if target.offset != targetOffset || target.sha256 != targetSha256 {
			continue
		}
		current, ok := states[target.ruleID]
		if !ok {
			return fmt.Errorf("registryview: rollback target is not current promotion: rule_id=%s target_op_id=%s", target.ruleID, targetOpID)
		}
		if current.LastPromotionSeq != target.seq {
			return fmt.Errorf("registryview: rollback target is not current promotion: rule_id=%s target_op_id=%s current_seq=%d target_seq=%d", target.ruleID, targetOpID, current.LastPromotionSeq, target.seq)
		}
		if !target.hadPrevious {
			delete(states, target.ruleID)
			return nil
		}
		states[target.ruleID] = target.previous
		return nil
	}
	return fmt.Errorf("registryview: rollback target mismatch: op_id=%s target_offset=%d target_sha256=%s", targetOpID, targetOffset, targetSha256)
}

func MustGet(states map[string]RuleState, ruleID string) (RuleState, error) {
	state, ok := states[ruleID]
	if !ok {
		return RuleState{}, fmt.Errorf("registryview: rule %s not found", ruleID)
	}
	return state, nil
}

func registryAppendResult(entry contracts.RuleRegistryEntry, offset int64) (contracts.RegistryAppendResult, int64, error) {
	payload, err := contracts.CanonicalMarshal(entry)
	if err != nil {
		return contracts.RegistryAppendResult{}, 0, err
	}
	sum := sha256.Sum256(payload)
	result := contracts.RegistryAppendResult{
		Offset: offset,
		Sha256: hex.EncodeToString(sum[:]),
	}
	return result, offset + int64(len(payload)+1), nil
}
