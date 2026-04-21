package contracts

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// ComputeAdoptIdempotencyKey returns the step70 adopt idempotency key.
//
// The `||` operator in io-contracts.md is documentation shorthand for raw
// string concatenation with no separator bytes:
// sha256(run_id + target_sha + best_sha_before + candidates_hash).
func ComputeAdoptIdempotencyKey(runID, targetSHA, bestSHABefore, candidatesHash string) string {
	sum := sha256.Sum256([]byte(runID + targetSHA + bestSHABefore + candidatesHash))
	return hex.EncodeToString(sum[:])
}

// ComputePlannedAdoptionEntryOpID returns the stable per-entry identifier used
// for promotion registry rows rebuilt from planned_adoption during recovery.
func ComputePlannedAdoptionEntryOpID(intentionIdempotencyKey string, entryIndex int, ruleID string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%s", intentionIdempotencyKey, entryIndex, ruleID)))
	return hex.EncodeToString(sum[:])
}
