package archive

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/registryview"
)

func ComputeOpID(sunsetRunID, ruleID, transition string) string {
	var payload bytes.Buffer
	for _, field := range []string{sunsetRunID, ruleID, transition} {
		fmt.Fprintf(&payload, "%08x:", len(field))
		payload.WriteString(field)
	}
	sum := sha256.Sum256(payload.Bytes())
	return hex.EncodeToString(sum[:])
}

// computeLegacyOpID reproduces the pre-length-prefixed plain-concat op-id
// encoding that predates the unambiguous tuple scheme. Kept for backward
// compatibility with in-flight marker / registry state written before the
// encoding change so findByOpID / markerTailProgressPrefixLen can recognise
// partially-applied sunset transitions after an upgrade (F19).
func computeLegacyOpID(sunsetRunID, ruleID, transition string) string {
	sum := sha256.Sum256([]byte(sunsetRunID + ruleID + transition))
	return hex.EncodeToString(sum[:])
}

// opIDCandidates returns the current and legacy op-id encodings. Callers that
// reconcile persisted state (registry lookup, stale marker replay) must accept
// both so an upgrade mid-sunset does not re-apply or strand entries.
func opIDCandidates(sunsetRunID, ruleID, transition string) []string {
	current := ComputeOpID(sunsetRunID, ruleID, transition)
	legacy := computeLegacyOpID(sunsetRunID, ruleID, transition)
	if current == legacy {
		return []string{current}
	}
	return []string{current, legacy}
}

func registryPrevHashForVersion(versionSeq int64, prevHash string) string {
	if versionSeq == 1 {
		return ""
	}
	return prevHash
}

func buildRegistryEntry(path string, t Transition, sunsetRunID, opID string, at time.Time) (contracts.RuleRegistryEntry, error) {
	lines, err := readRegistryLines(path)
	if err != nil {
		return contracts.RuleRegistryEntry{}, err
	}
	if err := validateTransitionAgainstRegistry(lines, t); err != nil {
		return contracts.RuleRegistryEntry{}, err
	}
	prevHash := ""
	if len(lines) > 0 {
		prevHash = lines[len(lines)-1].Sha256
	}
	versionSeq := nextRegistryVersion(lines, t.RuleID)

	switch t.Kind {
	case contracts.RegistryKindStatusChanged:
		v := contracts.RuleRegistryStatusChanged{
			Kind:          contracts.RegistryKindStatusChanged,
			SchemaVersion: "1",
			RuleID:        t.RuleID,
			PrevStatus:    t.PrevStatus,
			NewStatus:     t.NewStatus,
			Transition:    t.Transition,
			OpID:          opID,
			VersionSeq:    versionSeq,
			PrevHash:      registryPrevHashForVersion(versionSeq, prevHash),
			BySunsetRunID: sunsetRunID,
			At:            at,
		}
		return contracts.RuleRegistryEntry{Kind: v.Kind, Value: v}, nil
	case contracts.RegistryKindArchived:
		v := contracts.RuleRegistryArchived{
			Kind:          contracts.RegistryKindArchived,
			SchemaVersion: "1",
			RuleID:        t.RuleID,
			PrevStatus:    t.PrevStatus,
			NewStatus:     contracts.RuleStatusArchived,
			OpID:          opID,
			VersionSeq:    versionSeq,
			PrevHash:      registryPrevHashForVersion(versionSeq, prevHash),
			BySunsetRunID: sunsetRunID,
			At:            at,
		}
		return contracts.RuleRegistryEntry{Kind: v.Kind, Value: v}, nil
	case contracts.RegistryKindRestored:
		v := contracts.RuleRegistryRestored{
			Kind:          contracts.RegistryKindRestored,
			SchemaVersion: "1",
			RuleID:        t.RuleID,
			PrevStatus:    contracts.RuleStatusArchived,
			NewStatus:     t.NewStatus,
			OpID:          opID,
			VersionSeq:    versionSeq,
			PrevHash:      registryPrevHashForVersion(versionSeq, prevHash),
			BySunsetRunID: sunsetRunID,
			At:            at,
		}
		return contracts.RuleRegistryEntry{Kind: v.Kind, Value: v}, nil
	default:
		return contracts.RuleRegistryEntry{}, fmt.Errorf("archive: unsupported transition kind=%q", t.Kind)
	}
}

func validateTransitionAgainstRegistry(lines []registryLine, t Transition) error {
	entries := make([]contracts.RuleRegistryEntry, 0, len(lines))
	for _, line := range lines {
		entries = append(entries, line.Entry)
	}
	states, err := registryview.Build(entries)
	if err != nil {
		return err
	}
	state, ok := states[t.RuleID]
	if !ok || (state.RuleID == "" && state.Status == "" && !state.Exists) {
		return fmt.Errorf("archive: rule not found in registry: rule_id=%s", t.RuleID)
	}
	if state.Status != t.PrevStatus {
		return fmt.Errorf("archive: registry status mismatch: rule_id=%s have=%s want=%s", t.RuleID, state.Status, t.PrevStatus)
	}
	return nil
}

func nextRegistryVersion(lines []registryLine, _ string) int64 {
	if len(lines) == 0 {
		return 1
	}
	return registryVersionSeq(lines[len(lines)-1].Entry) + 1
}

func registryVersionSeq(entry contracts.RuleRegistryEntry) int64 {
	switch v := entry.Value.(type) {
	case contracts.RuleRegistryAdded:
		return v.VersionSeq
	case contracts.RuleRegistryUpdated:
		return v.VersionSeq
	case contracts.RuleRegistryRolledBack:
		return v.VersionSeq
	case contracts.RuleRegistryStatusChanged:
		return v.VersionSeq
	case contracts.RuleRegistryArchived:
		return v.VersionSeq
	case contracts.RuleRegistryRestored:
		return v.VersionSeq
	default:
		return 0
	}
}

func findByOpID(path, opID string) (contracts.RegistryAppendResult, bool, error) {
	lines, err := registryLookupLines(path)
	if err != nil {
		return contracts.RegistryAppendResult{}, false, err
	}
	for i := len(lines) - 1; i >= 0; i-- {
		switch v := lines[i].Entry.Value.(type) {
		case contracts.RuleRegistryStatusChanged:
			if v.OpID == opID {
				return contracts.RegistryAppendResult{Offset: lines[i].Offset, Sha256: lines[i].Sha256}, true, nil
			}
		case contracts.RuleRegistryArchived:
			if v.OpID == opID {
				return contracts.RegistryAppendResult{Offset: lines[i].Offset, Sha256: lines[i].Sha256}, true, nil
			}
		case contracts.RuleRegistryRestored:
			if v.OpID == opID {
				return contracts.RegistryAppendResult{Offset: lines[i].Offset, Sha256: lines[i].Sha256}, true, nil
			}
		}
	}
	return contracts.RegistryAppendResult{}, false, nil
}

func registryOpID(entry contracts.RuleRegistryEntry) string {
	switch v := entry.Value.(type) {
	case contracts.RuleRegistryStatusChanged:
		return v.OpID
	case contracts.RuleRegistryArchived:
		return v.OpID
	case contracts.RuleRegistryRestored:
		return v.OpID
	case *contracts.RuleRegistryStatusChanged:
		if v != nil {
			return v.OpID
		}
	case *contracts.RuleRegistryArchived:
		if v != nil {
			return v.OpID
		}
	case *contracts.RuleRegistryRestored:
		if v != nil {
			return v.OpID
		}
	}
	return ""
}

func registryLookupLines(path string) ([]registryLine, error) {
	indexPath := filepath.Join(filepath.Dir(path), "rules-idempotency-index.jsonl")
	return internalio.RegistryLookupLinesByIdempotencyIndex(path, indexPath)
}

func readRegistryLines(path string) ([]registryLine, error) {
	return internalio.RegistryLines(path)
}

func registryLineCount(path string) (int, error) {
	return internalio.RegistryLineCount(path)
}
