package contracts

import "errors"

// Sentinel errors shared across all contract schemas / step I/O boundaries.
//
// ErrTrailingJSON / ErrUnknownManifestKind 等は strict JSON reader および
// tagged union の custom UnmarshalJSON から返される。ステップ間 typed failure
// (ErrAgentTimeout / ErrAllAgentsFailed 等) は `internal/contracts/stepio`
// からも re-export 経由で参照される (see stepio/errors.go)。
var (
	// ErrTrailingJSON is returned when a strict JSON reader observes additional
	// tokens (or bytes) after the single expected top-level value.
	ErrTrailingJSON = errors.New("contracts: trailing data after JSON value")

	// ErrDuplicateJSONKey is returned when a strict JSON reader observes the
	// same object key twice at the same nesting level.
	ErrDuplicateJSONKey = errors.New("contracts: duplicate JSON object key")

	// ErrEmptyJSON is returned when a strict JSON reader receives an empty or
	// whitespace-only payload (including a bare UTF-8 BOM).
	ErrEmptyJSON = errors.New("contracts: empty JSON payload")

	// ErrUnknownManifestKind is returned when a Manifest envelope has a `kind`
	// outside the {success, error, timeout} set.
	ErrUnknownManifestKind = errors.New("contracts: unknown manifest kind")

	// ErrUnknownDecisionAction is returned when a Decision envelope has an
	// `action` outside the {adopt, reject, noop, rollback} set.
	ErrUnknownDecisionAction = errors.New("contracts: unknown decision action")

	// ErrUnknownRegistryKind is returned when a rules-registry.jsonl entry has
	// a `kind` outside the {added, updated, status_changed, archived, restored,
	// rolled_back} set.
	ErrUnknownRegistryKind = errors.New("contracts: unknown rules-registry kind")

	// ErrUnknownStateKind is returned when a processed.jsonl entry has a `kind`
	// outside the accepted state event enum.
	ErrUnknownStateKind = errors.New("contracts: unknown state event kind")

	// ErrUnknownCandidateKind is returned when a candidate kind is outside
	// {new, update, duplicate}.
	ErrUnknownCandidateKind = errors.New("contracts: unknown candidate kind")

	// ErrCandidatesHashMismatch is returned when candidates_hash is empty or
	// does not match the canonical hash of candidates[].
	ErrCandidatesHashMismatch = errors.New("contracts: candidates_hash mismatch")

	// ErrCanonicalNonInteger is returned when canonical JSON encounters a
	// number that is not representable as an int64 integer.
	ErrCanonicalNonInteger = errors.New("contracts: canonical marshal: number must be an int64 integer")

	// ErrCanonicalForbiddenKind is returned when canonical JSON encounters a Go
	// numeric kind that is forbidden by the integer-only contract before
	// encoding/json has a chance to erase the original type information.
	ErrCanonicalForbiddenKind = errors.New("contracts: canonical marshal: forbidden Go numeric kind")

	// ErrCanonicalUnsupportedMapKey is returned when canonical JSON encounters a
	// map whose keys are not plain JSON strings.
	ErrCanonicalUnsupportedMapKey = errors.New("contracts: canonical marshal: unsupported map key type")

	// ErrNilValidationValue is returned when runValidation receives a typed nil
	// pointer/interface that would otherwise panic when calling Validate().
	ErrNilValidationValue = errors.New("contracts: nil validation target")
)
