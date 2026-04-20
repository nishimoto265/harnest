package contracts

import (
	"crypto/sha256"
	"encoding/hex"
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
