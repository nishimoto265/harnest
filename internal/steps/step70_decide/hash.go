package step70_decide

import (
	"crypto/sha256"
	"encoding/hex"
)

// hashOfRegistryLine returns the hex-encoded sha256 of a single registry JSONL
// row. Callers must pass the line without its trailing newline, matching the
// framing used by internal/io.AppendRegistryEntry.
func hashOfRegistryLine(line []byte) string {
	sum := sha256.Sum256(line)
	return hex.EncodeToString(sum[:])
}
