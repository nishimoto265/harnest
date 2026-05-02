package archive

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/stretchr/testify/require"
	"path/filepath"
	"testing"
	"time"
)

func realTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	real, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	return real
}
func readRegistryLinesForTest(t *testing.T, path string) []registryLine {
	t.Helper()
	lines, err := readRegistryLines(path)
	require.NoError(t, err)
	return lines
}
func deprecateTransition(ruleID string) Transition {
	return Transition{
		RuleID:     ruleID,
		PrevStatus: contracts.RuleStatusActive,
		NewStatus:  contracts.RuleStatusDeprecated,
		Kind:       contracts.RegistryKindStatusChanged,
		Transition: contracts.SunsetTransitionDeprecate,
	}
}
func archiveTransition(ruleID string, prev contracts.RuleStatus) Transition {
	return Transition{
		RuleID:     ruleID,
		PrevStatus: prev,
		NewStatus:  contracts.RuleStatusArchived,
		Kind:       contracts.RegistryKindArchived,
		Transition: contracts.SunsetTransitionArchive,
	}
}
func restoreTransition(ruleID string) Transition {
	return Transition{
		RuleID:     ruleID,
		PrevStatus: contracts.RuleStatusArchived,
		NewStatus:  contracts.RuleStatusActive,
		Kind:       contracts.RegistryKindRestored,
		Transition: contracts.SunsetTransitionActivate,
	}
}
func opIDFromEntry(entry contracts.RuleRegistryEntry) string {
	switch v := entry.Value.(type) {
	case contracts.RuleRegistryStatusChanged:
		return v.OpID
	case contracts.RuleRegistryArchived:
		return v.OpID
	case contracts.RuleRegistryRestored:
		return v.OpID
	default:
		return ""
	}
}
func writeArchiveSeedRegistryAdds(t *testing.T, path string, count int) {
	t.Helper()

	var (
		buffer   bytes.Buffer
		prevHash string
	)
	for i := 0; i < count; i++ {
		entry := contracts.RuleRegistryEntry{
			Kind: contracts.RegistryKindAdded,
			Value: contracts.RuleRegistryAdded{
				Kind:           contracts.RegistryKindAdded,
				SchemaVersion:  "1",
				RuleID:         fmt.Sprintf("seed-%04d", i),
				RulePath:       fmt.Sprintf("rules/seed-%04d.md", i),
				Sha256:         fmt.Sprintf("%064x", i+1),
				IdempotencyKey: fmt.Sprintf("%064x", i+1000),
				VersionSeq:     int64(i + 1),
				PrevHash:       prevHash,
				ByRunID:        contracts.RunID(fmt.Sprintf("2026-04-21-PR%02d-abcdef0", (i%90)+10)),
				At:             time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC),
			},
		}
		var line bytes.Buffer
		require.NoError(t, contracts.EncodeStrict(&line, entry))
		payload := bytes.TrimSuffix(line.Bytes(), []byte{'\n'})
		_, err := buffer.Write(payload)
		require.NoError(t, err)
		require.NoError(t, buffer.WriteByte('\n'))

		sum := sha256.Sum256(payload)
		prevHash = hex.EncodeToString(sum[:])
	}
	require.NoError(t, internalio.WriteAtomic(path, buffer.Bytes()))
}
func appendArchiveSeedAdded(t *testing.T, registryPath, ruleID string, versionSeq int64, prevHash string) contracts.RegistryAppendResult {
	t.Helper()
	entry := contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindAdded,
		Value: contracts.RuleRegistryAdded{
			Kind:           contracts.RegistryKindAdded,
			SchemaVersion:  "1",
			RuleID:         ruleID,
			RulePath:       fmt.Sprintf("rules/%s.md", ruleID),
			Sha256:         fmt.Sprintf("%064x", versionSeq),
			IdempotencyKey: fmt.Sprintf("%064x", versionSeq+1000),
			VersionSeq:     versionSeq,
			PrevHash:       prevHash,
			ByRunID:        "2026-04-21-PR1-aaaaaaa",
			At:             time.Date(2026, 4, 21, 8, 0, 0, 0, time.UTC),
		},
	}
	result, err := internalio.AppendRegistryEntry(registryPath, entry)
	require.NoError(t, err)
	return result
}
func seedArchiveRuleState(t *testing.T, registryPath, ruleID string, status contracts.RuleStatus) {
	t.Helper()

	added := contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindAdded,
		Value: contracts.RuleRegistryAdded{
			Kind:           contracts.RegistryKindAdded,
			SchemaVersion:  "1",
			RuleID:         ruleID,
			RulePath:       fmt.Sprintf("rules/%s.md", ruleID),
			Sha256:         fmt.Sprintf("%064x", 1),
			IdempotencyKey: fmt.Sprintf("%064x", 1001),
			VersionSeq:     1,
			PrevHash:       "",
			ByRunID:        "2026-04-21-PR1-aaaaaaa",
			At:             time.Date(2026, 4, 21, 8, 0, 0, 0, time.UTC),
		},
	}
	result, err := internalio.AppendRegistryEntry(registryPath, added)
	require.NoError(t, err)

	if status == contracts.RuleStatusActive {
		return
	}

	entry := contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindStatusChanged,
		Value: contracts.RuleRegistryStatusChanged{
			Kind:          contracts.RegistryKindStatusChanged,
			SchemaVersion: "1",
			RuleID:        ruleID,
			PrevStatus:    contracts.RuleStatusActive,
			NewStatus:     status,
			Transition:    contracts.SunsetTransitionDeprecate,
			OpID:          ComputeOpID("seed-sunset", ruleID, string(contracts.SunsetTransitionDeprecate)),
			VersionSeq:    2,
			PrevHash:      result.Sha256,
			BySunsetRunID: "seed-sunset",
			At:            time.Date(2026, 4, 21, 8, 30, 0, 0, time.UTC),
		},
	}
	_, err = internalio.AppendRegistryEntry(registryPath, entry)
	require.NoError(t, err)
}
