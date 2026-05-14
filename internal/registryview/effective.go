package registryview

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/nishimoto265/harnest/internal/contracts"
)

type RuleState struct {
	RuleID           string
	RulePath         string
	Sha256           string
	Status           contracts.RuleStatus
	Exists           bool
	LastPromotionSeq int
	// LastMutationSeq is the sequence index of the most recent mutation
	// (added / updated / status_changed / archived / restored) applied to
	// this rule. Rollback safety checks compare against this instead of
	// LastPromotionSeq so a promotion followed by a status change cannot be
	// silently erased by a rollback targeting the earlier promotion
	// (F17).
	LastMutationSeq int
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
			if err := applyAdded(states, history, v, seq, result); err != nil {
				return nil, err
			}
		case *contracts.RuleRegistryAdded:
			if v == nil {
				continue
			}
			if err := applyAdded(states, history, *v, seq, result); err != nil {
				return nil, err
			}
		case contracts.RuleRegistryUpdated:
			if err := applyUpdated(states, history, v, seq, result); err != nil {
				return nil, err
			}
		case *contracts.RuleRegistryUpdated:
			if v == nil {
				continue
			}
			if err := applyUpdated(states, history, *v, seq, result); err != nil {
				return nil, err
			}
		case contracts.RuleRegistryStatusChanged:
			applyStatusChanged(states, v, seq)
		case *contracts.RuleRegistryStatusChanged:
			if v == nil {
				continue
			}
			applyStatusChanged(states, *v, seq)
		case contracts.RuleRegistryArchived:
			applyArchived(states, v, seq)
		case *contracts.RuleRegistryArchived:
			if v == nil {
				continue
			}
			applyArchived(states, *v, seq)
		case contracts.RuleRegistryRestored:
			applyRestored(states, v, seq)
		case *contracts.RuleRegistryRestored:
			if v == nil {
				continue
			}
			applyRestored(states, *v, seq)
		case contracts.RuleRegistryRolledBack:
			if err := applyRollback(states, history, v.TargetOpID, v.TargetOffset, v.TargetSha256, seq); err != nil {
				return nil, err
			}
		case *contracts.RuleRegistryRolledBack:
			if v == nil {
				continue
			}
			if err := applyRollback(states, history, v.TargetOpID, v.TargetOffset, v.TargetSha256, seq); err != nil {
				return nil, err
			}
		}
	}

	return states, nil
}

func applyAdded(
	states map[string]RuleState,
	history map[string][]promotionSnapshot,
	v contracts.RuleRegistryAdded,
	seq int,
	result contracts.RegistryAppendResult,
) error {
	if err := contracts.ValidateRuleID(v.RuleID); err != nil {
		return err
	}
	if err := contracts.ValidateRulePath(v.RulePath); err != nil {
		return err
	}
	previous, hadPrevious, err := previousState(states, v.RuleID)
	if err != nil {
		return err
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
		LastMutationSeq:  seq,
	}
	return nil
}

func applyUpdated(
	states map[string]RuleState,
	history map[string][]promotionSnapshot,
	v contracts.RuleRegistryUpdated,
	seq int,
	result contracts.RegistryAppendResult,
) error {
	if err := contracts.ValidateRuleID(v.RuleID); err != nil {
		return err
	}
	if err := contracts.ValidateRulePath(v.RulePath); err != nil {
		return err
	}
	previous, hadPrevious, err := previousState(states, v.RuleID)
	if err != nil {
		return err
	}
	if !hadPrevious || previous.Sha256 != v.PrevSha256 {
		return fmt.Errorf("registryview: update prev_sha256 mismatch: rule_id=%s prev_sha256=%s effective_sha256=%s", v.RuleID, v.PrevSha256, previous.Sha256)
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
		LastMutationSeq:  seq,
	}
	return nil
}

func applyStatusChanged(states map[string]RuleState, v contracts.RuleRegistryStatusChanged, seq int) {
	state := states[v.RuleID]
	state.RuleID = v.RuleID
	state.Status = v.NewStatus
	state.Exists = v.NewStatus != contracts.RuleStatusArchived
	state.LastMutationSeq = seq
	states[v.RuleID] = state
}

func applyArchived(states map[string]RuleState, v contracts.RuleRegistryArchived, seq int) {
	state := states[v.RuleID]
	state.RuleID = v.RuleID
	state.Status = contracts.RuleStatusArchived
	state.Exists = false
	state.LastMutationSeq = seq
	states[v.RuleID] = state
}

func applyRestored(states map[string]RuleState, v contracts.RuleRegistryRestored, seq int) {
	state := states[v.RuleID]
	state.RuleID = v.RuleID
	state.Status = v.NewStatus
	state.Exists = true
	state.LastMutationSeq = seq
	states[v.RuleID] = state
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

// applyRollback restores the rule state that existed immediately before the
// promotion identified by (targetOpID, targetOffset, targetSha256). F17: the
// target must be the **latest mutation** on the rule, not merely the latest
// promotion. An intervening status_changed / archived / restored bumps the
// rule's LastMutationSeq but not LastPromotionSeq, and reverting to a
// pre-promotion snapshot would silently erase that later mutation.
func applyRollback(states map[string]RuleState, history map[string][]promotionSnapshot, targetOpID string, targetOffset int64, targetSha256 string, rollbackSeq int) error {
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
		if current.LastMutationSeq != target.seq {
			return fmt.Errorf("registryview: rollback target is stale: rule_id=%s target_op_id=%s target_seq=%d last_mutation_seq=%d", target.ruleID, targetOpID, target.seq, current.LastMutationSeq)
		}
		if !target.hadPrevious {
			delete(states, target.ruleID)
			return nil
		}
		restored := target.previous
		// Bump LastMutationSeq to reflect the rollback itself so a
		// subsequent rollback chain does not mistake the restored state
		// for the latest mutation.
		restored.LastMutationSeq = rollbackSeq
		states[target.ruleID] = restored
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
