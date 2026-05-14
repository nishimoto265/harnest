package step70_decide

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/stretchr/testify/require"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type seedRegistrySpec struct {
	RuleID         string
	IdempotencyKey string
	ByRunID        contracts.RunID
}

func adoptAddedEntry(runID contracts.RunID, candidatesHash string) contracts.RuleRegistryEntry {
	return adoptAddedEntryWithTarget(runID, candidatesHash, strings.Repeat("2", 40), "rule-seed")
}
func adoptAddedEntriesWithTarget(runID contracts.RunID, candidatesHash, targetSHA string, ruleIDs ...string) []contracts.RuleRegistryEntry {
	entries := make([]contracts.RuleRegistryEntry, 0, len(ruleIDs))
	for _, ruleID := range ruleIDs {
		entries = append(entries, adoptAddedEntryWithTarget(runID, candidatesHash, targetSHA, ruleID))
	}
	return entries
}
func adoptAddedEntryWithTarget(runID contracts.RunID, candidatesHash, targetSHA, ruleID string) contracts.RuleRegistryEntry {
	key := contracts.ComputeAdoptIdempotencyKey(string(runID), strings.Repeat("2", 40), strings.Repeat("1", 40), candidatesHash)
	v := contracts.RuleRegistryAdded{
		Kind:           contracts.RegistryKindAdded,
		SchemaVersion:  "1",
		RuleID:         ruleID,
		RulePath:       "rules/" + ruleID + ".md",
		Sha256:         sha256String(fixtureRuleBody(ruleID)),
		IdempotencyKey: key,
		VersionSeq:     1,
		PrevHash:       "",
		ByRunID:        runID,
		At:             time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
	}
	v.IdempotencyKey = contracts.ComputeAdoptIdempotencyKey(string(runID), targetSHA, strings.Repeat("1", 40), candidatesHash)
	return contracts.RuleRegistryEntry{Kind: v.Kind, Value: v}
}
func fixtureRuleBody(ruleID string) string {
	return ruleID + " body\n"
}
func adoptAddedEntryWithBody(runID contracts.RunID, ruleID, body string) contracts.RuleRegistryEntry {
	return contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindAdded,
		Value: contracts.RuleRegistryAdded{
			Kind:           contracts.RegistryKindAdded,
			SchemaVersion:  "1",
			RuleID:         ruleID,
			RulePath:       "rules/" + ruleID + ".md",
			Sha256:         sha256String(body),
			IdempotencyKey: strings.Repeat("0", 64),
			VersionSeq:     1,
			PrevHash:       "",
			ByRunID:        runID,
			At:             time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
		},
	}
}
func adoptUpdatedEntryWithBody(runID contracts.RunID, ruleID, prevBody, body string) contracts.RuleRegistryEntry {
	return contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindUpdated,
		Value: contracts.RuleRegistryUpdated{
			Kind:           contracts.RegistryKindUpdated,
			SchemaVersion:  "1",
			RuleID:         ruleID,
			RulePath:       "rules/" + ruleID + ".md",
			Sha256:         sha256String(body),
			PrevSha256:     sha256String(prevBody),
			IdempotencyKey: strings.Repeat("0", 64),
			VersionSeq:     1,
			PrevHash:       "",
			ByRunID:        runID,
			At:             time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
		},
	}
}
func planningIntention(runID contracts.RunID, target Target, candidatesHash string) contracts.IntentionRecord {
	idempotencyKey := contracts.ComputeAdoptIdempotencyKey(string(runID), target.TargetSHA, target.BestShaBefore, candidatesHash)
	return contracts.IntentionRecord{
		SchemaVersion:      "1",
		Stage:              contracts.IntentionStagePlanning,
		IdempotencyKey:     idempotencyKey,
		RunID:              runID,
		BestShaBefore:      target.BestShaBefore,
		TargetSha:          target.TargetSHA,
		CandidatesHash:     candidatesHash,
		RegistryHeadBefore: "",
		PlannedAdoption:    mustPlannedAdoption(nil, idempotencyKey, target.RulesToAppend),
		StartedAt:          time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
	}
}
func mustPlannedAdoption(t *testing.T, intentionIdempotencyKey string, entries []contracts.RuleRegistryEntry) *contracts.PlannedAdoption {
	if t != nil {
		t.Helper()
	}
	planned, err := plannedAdoptionFromRegistryEntries(intentionIdempotencyKey, entries)
	if t != nil {
		require.NoError(t, err)
	} else if err != nil {
		panic(err)
	}
	return planned
}
func seedRegistryAdd(t *testing.T, path string, resolver *fixtureResolver, runID contracts.RunID, candidatesHash string) (contracts.RegistryAppendResult, contracts.RuleRegistryEntry) {
	t.Helper()
	intention := planningIntention(runID, resolver.target, candidatesHash)
	entries, err := registryEntriesFromPlannedAdoption(intention, time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	entry := entries[0]
	result, err := internalio.AppendRegistryEntry(path, entry)
	require.NoError(t, err)
	ruleID, rulePath, sha, err := registryEntryRuleSidecar(entry)
	require.NoError(t, err)
	body := fixtureRuleBody(ruleID)
	if sha256String(body) == sha {
		require.NoError(t, internalio.WriteAtomic(filepath.Join(filepath.Dir(path), filepath.FromSlash(rulePath)), []byte(body)))
	}
	return result, entry
}
func seedRegistryUniqueAdd(t *testing.T, path, ruleID, idemKey, byRunID string) (contracts.RegistryAppendResult, contracts.RuleRegistryEntry) {
	t.Helper()
	prevHash, err := currentRegistryHead(path)
	require.NoError(t, err)
	versionSeq := int64(1)
	lines, err := readRegistryLines(path)
	require.NoError(t, err)
	if len(lines) > 0 {
		versionSeq = int64(len(lines) + 1)
	}
	entry := contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindAdded,
		Value: contracts.RuleRegistryAdded{
			Kind:           contracts.RegistryKindAdded,
			SchemaVersion:  "1",
			RuleID:         ruleID,
			RulePath:       "rules/" + ruleID + ".md",
			Sha256:         strings.Repeat("b", 64),
			IdempotencyKey: idemKey,
			VersionSeq:     versionSeq,
			PrevHash:       prevHash,
			ByRunID:        contracts.RunID(byRunID),
			At:             time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
		},
	}
	result, err := internalio.AppendRegistryEntry(path, entry)
	require.NoError(t, err)
	return result, entry
}
func writeSeedRegistryAdds(t *testing.T, path string, specs []seedRegistrySpec) map[string]contracts.RegistryAppendResult {
	t.Helper()

	var (
		buffer   bytes.Buffer
		offset   int64
		prevHash string
	)
	results := make(map[string]contracts.RegistryAppendResult, len(specs))
	for i, spec := range specs {
		entry := contracts.RuleRegistryEntry{
			Kind: contracts.RegistryKindAdded,
			Value: contracts.RuleRegistryAdded{
				Kind:           contracts.RegistryKindAdded,
				SchemaVersion:  "1",
				RuleID:         spec.RuleID,
				RulePath:       "rules/" + spec.RuleID + ".md",
				Sha256:         fmt.Sprintf("%064x", i+10000),
				IdempotencyKey: spec.IdempotencyKey,
				VersionSeq:     int64(i + 1),
				PrevHash:       prevHash,
				ByRunID:        spec.ByRunID,
				At:             time.Unix(100, 0).UTC(),
			},
		}
		var line bytes.Buffer
		require.NoError(t, contracts.EncodeStrict(&line, entry))
		payload := bytes.TrimSuffix(line.Bytes(), []byte{'\n'})
		_, err := buffer.Write(payload)
		require.NoError(t, err)
		require.NoError(t, buffer.WriteByte('\n'))

		sum := sha256.Sum256(payload)
		result := contracts.RegistryAppendResult{
			Offset: offset,
			Sha256: hex.EncodeToString(sum[:]),
		}
		results[spec.IdempotencyKey] = result
		prevHash = result.Sha256
		offset += int64(len(payload) + 1)
	}
	require.NoError(t, internalio.WriteAtomic(path, buffer.Bytes()))
	return results
}
func installAppendCASMismatchHook(t *testing.T, runCtx internalio.RunContext, mismatches int) {
	t.Helper()
	original := appendRegistryEntry
	count := 0
	appendRegistryEntry = func(path string, entry contracts.RuleRegistryEntry) (contracts.RegistryAppendResult, error) {
		if path == runCtx.RulesRegistryPath() && count < mismatches {
			switch entry.Kind {
			case contracts.RegistryKindAdded, contracts.RegistryKindUpdated:
				count++
				_, _ = seedRegistryUniqueAdd(
					t,
					path,
					fmt.Sprintf("race-%d", count),
					fmt.Sprintf("%064x", 9000+count),
					fmt.Sprintf("2026-04-21-PR9%d-abcdef0", count),
				)
			}
		}
		return original(path, entry)
	}
	t.Cleanup(func() {
		appendRegistryEntry = original
	})
}

// memStore is a minimal in-memory IntentionWriter replacement for tests that
// exercise stage transitions. Saves are also persisted to disk so that a
// subsequent Run() call (resume) sees them.
