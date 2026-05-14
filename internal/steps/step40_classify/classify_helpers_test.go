package step40_classify

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/nishimoto265/harnest/internal/steps/scorecore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestConfig(t *testing.T) Config {
	t.Helper()

	baseDir := t.TempDir()
	runsBase := filepath.Join(baseDir, "runs")
	worktreeBase := filepath.Join(baseDir, "worktrees")
	require.NoError(t, os.MkdirAll(runsBase, 0o755))
	require.NoError(t, os.MkdirAll(worktreeBase, 0o755))

	runID := contracts.RunID("2026-04-21-PR42-abcdef0")
	runIO, err := internalio.NewRunContext(runID, runsBase, worktreeBase)
	require.NoError(t, err)

	return Config{
		IO:           runIO,
		RegistryPath: runIO.RulesRegistryPath(),
		TaskPackage:  validTaskPackage(t, runIO),
		Now: func() time.Time {
			return time.Date(2026, 4, 21, 12, 34, 56, 0, time.UTC)
		},
	}
}

func validTaskPackage(t *testing.T, runIO internalio.RunContext) *contracts.TaskPackage {
	t.Helper()

	baseSHA := strings.Repeat("a", 40)
	worktrees := make([]contracts.WorktreeAllocation, 0, 6)
	for pass := 1; pass <= 2; pass++ {
		for agentIndex, agent := range []contracts.AgentID{"a1", "a2", "a3"} {
			path := filepath.Join(runIO.WorktreeBase, string(runIO.RunID), fmt.Sprintf("pass%d-%s", pass, agent))
			require.NoError(t, os.MkdirAll(path, 0o755))
			worktrees = append(worktrees, contracts.WorktreeAllocation{
				Agent:   agent,
				Pass:    pass,
				Path:    path,
				Branch:  fmt.Sprintf("auto-improve/%s/pass%d/%d", runIO.RunID, pass, agentIndex+1),
				BaseSHA: baseSHA,
				HeadSHA: baseSHA,
			})
		}
	}

	pkg := &contracts.TaskPackage{
		SchemaVersion:           "1",
		RunID:                   runIO.RunID,
		PR:                      42,
		Title:                   "PR #42",
		BaseSHA:                 baseSHA,
		BestBranch:              "main",
		ReconstructedTaskPrompt: "stub prompt",
		Worktrees:               worktrees,
		CreatedAt:               time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
	}
	require.NoError(t, pkg.Validate())
	return pkg
}

func writeScores(t *testing.T, runIO internalio.RunContext, entries ...contracts.ScoreEntry) {
	t.Helper()
	path, err := runIO.ResolveRunRelative(scoresPath)
	require.NoError(t, err)
	writeJSONL(t, path, entries...)
	refreshStep30Marker(t, runIO)
}

func writeCompliance(t *testing.T, runIO internalio.RunContext, entries ...contracts.ComplianceEntry) {
	t.Helper()
	path, err := runIO.ResolveRunRelative(compliancePath)
	require.NoError(t, err)
	writeJSONL(t, path, entries...)
	refreshStep30Marker(t, runIO)
}

func writeIssues(t *testing.T, runIO internalio.RunContext, entries ...contracts.IssueEntry) {
	t.Helper()
	path, err := runIO.ResolveRunRelative(issuesPath)
	require.NoError(t, err)
	writeJSONL(t, path, entries...)
}

func writeRegistry(t *testing.T, path string, entries ...contracts.RuleRegistryEntry) {
	t.Helper()
	lastSha := make(map[string]string)
	appended := make(map[string][]contracts.RegistryAppendResult)
	registryBase := filepath.Dir(path)
	require.NoError(t, internalio.WriteAtomic(path, nil))
	prevHash := ""
	for idx, entry := range entries {
		switch value := entry.Value.(type) {
		case contracts.RuleRegistryAdded:
			absPath := filepath.Join(registryBase, value.RulePath)
			if _, err := os.Stat(absPath); os.IsNotExist(err) {
				body := fmt.Sprintf("# %s added\n", value.RuleID)
				value.Sha256 = sha256String(body)
				writeRegistryRuleSidecar(t, registryBase, value.RulePath, body)
			}
			lastSha[value.RuleID] = value.Sha256
			entry = contracts.RuleRegistryEntry{Kind: entry.Kind, Value: value}
		case contracts.RuleRegistryUpdated:
			body := fmt.Sprintf("# %s updated\n", value.RuleID)
			if prev, ok := lastSha[value.RuleID]; ok {
				value.PrevSha256 = prev
			}
			value.Sha256 = sha256String(body)
			writeRegistryRuleSidecar(t, registryBase, value.RulePath, body)
			lastSha[value.RuleID] = value.Sha256
			entry = contracts.RuleRegistryEntry{Kind: entry.Kind, Value: value}
		case contracts.RuleRegistryRolledBack:
			targets := appended[value.TargetOpID]
			require.NotEmpty(t, targets, "rollback target must have been appended before rollback entry")
			target := targets[len(targets)-1]
			value.TargetOffset = target.Offset
			value.TargetSha256 = target.Sha256
			entry = contracts.RuleRegistryEntry{Kind: entry.Kind, Value: value}
		}
		entry = setRegistryChainFields(entry, int64(idx+1), prevHash)
		result, err := internalio.AppendRegistryEntry(path, entry)
		require.NoError(t, err)
		if key, ok := registryPromotionKey(entry); ok {
			appended[key] = append(appended[key], result)
		}
		prevHash = result.Sha256
	}
}

func setRegistryChainFields(entry contracts.RuleRegistryEntry, versionSeq int64, prevHash string) contracts.RuleRegistryEntry {
	switch value := entry.Value.(type) {
	case contracts.RuleRegistryAdded:
		value.VersionSeq = versionSeq
		value.PrevHash = prevHash
		return contracts.RuleRegistryEntry{Kind: entry.Kind, Value: value}
	case contracts.RuleRegistryUpdated:
		value.VersionSeq = versionSeq
		value.PrevHash = prevHash
		return contracts.RuleRegistryEntry{Kind: entry.Kind, Value: value}
	case contracts.RuleRegistryStatusChanged:
		value.VersionSeq = versionSeq
		value.PrevHash = prevHash
		return contracts.RuleRegistryEntry{Kind: entry.Kind, Value: value}
	case contracts.RuleRegistryArchived:
		value.VersionSeq = versionSeq
		value.PrevHash = prevHash
		return contracts.RuleRegistryEntry{Kind: entry.Kind, Value: value}
	case contracts.RuleRegistryRestored:
		value.VersionSeq = versionSeq
		value.PrevHash = prevHash
		return contracts.RuleRegistryEntry{Kind: entry.Kind, Value: value}
	case contracts.RuleRegistryRolledBack:
		value.VersionSeq = versionSeq
		value.PrevHash = prevHash
		return contracts.RuleRegistryEntry{Kind: entry.Kind, Value: value}
	default:
		return entry
	}
}

func registryPromotionKey(entry contracts.RuleRegistryEntry) (string, bool) {
	switch value := entry.Value.(type) {
	case contracts.RuleRegistryAdded:
		return value.IdempotencyKey, true
	case contracts.RuleRegistryUpdated:
		return value.IdempotencyKey, true
	default:
		return "", false
	}
}

func writeRegistryRuleSidecar(t *testing.T, registryBase, rulePath, body string) {
	t.Helper()
	absPath := filepath.Join(registryBase, rulePath)
	require.NoError(t, os.MkdirAll(filepath.Dir(absPath), 0o755))
	require.NoError(t, os.WriteFile(absPath, []byte(body), 0o644))
}

func writeJSONL[T any](t *testing.T, path string, entries ...T) {
	t.Helper()
	require.NoError(t, internalio.WriteAtomic(path, nil))
	for _, entry := range entries {
		require.NoError(t, internalio.AppendJSONL(path, entry))
	}
}

func readCandidatesFile(t *testing.T, runIO internalio.RunContext) contracts.Candidates {
	t.Helper()
	path, err := runIO.ResolveRunRelative(candidatesJSONPath)
	require.NoError(t, err)
	got, err := internalio.ReadJSON[contracts.Candidates](path)
	require.NoError(t, err)
	return got
}

func mustResolveClassifyPath(t *testing.T, runIO internalio.RunContext, rel string) string {
	t.Helper()
	path, err := runIO.ResolveRunRelative(rel)
	require.NoError(t, err)
	return path
}

func readClassificationFile(t *testing.T, runIO internalio.RunContext) []contracts.ClassificationEntry {
	t.Helper()
	path, err := runIO.ResolveRunRelative(classificationJSONLPath)
	require.NoError(t, err)
	got, err := internalio.ReadJSONL[contracts.ClassificationEntry](path)
	require.NoError(t, err)
	return got
}

func refreshStep30Marker(t *testing.T, runIO internalio.RunContext) {
	t.Helper()
	scoreFinalPath, err := runIO.ResolveRunRelative(scoresPath)
	require.NoError(t, err)
	complianceFinalPath, err := runIO.ResolveRunRelative(compliancePath)
	require.NoError(t, err)
	scoreRawPath, err := runIO.ResolveRunRelative("30/scores-A-raw.jsonl")
	require.NoError(t, err)
	complianceRawPath, err := runIO.ResolveRunRelative("30/compliance-A-raw.jsonl")
	require.NoError(t, err)

	scoreFinal, err := internalio.ReadJSONL[contracts.ScoreEntry](scoreFinalPath)
	if err != nil {
		scoreFinal = nil
	}
	complianceFinal, err := internalio.ReadJSONL[contracts.ComplianceEntry](complianceFinalPath)
	if err != nil {
		complianceFinal = nil
	}

	scoreRaw := make([]contracts.RawScoreEntry, 0, len(scoreFinal))
	for _, row := range scoreFinal {
		scoreRaw = append(scoreRaw, contracts.RawScoreEntry{
			SchemaVersion: "1",
			RunID:         row.RunID,
			Pass:          row.Pass,
			Agent:         row.Agent,
			JudgeRole:     contracts.JudgeRolePrimary,
			Dimension:     row.Dimension,
			Score:         row.Score,
			Reasons:       row.Reasons,
			OutputSha256:  strings.Repeat("a", 64),
			RubricVersion: row.RubricVersion,
			PromptVersion: row.PromptVersion,
			ResolvedAt:    row.ResolvedAt,
		})
	}
	complianceRaw := make([]contracts.RawComplianceEntry, 0, len(complianceFinal))
	for _, row := range complianceFinal {
		complianceRaw = append(complianceRaw, contracts.RawComplianceEntry{
			SchemaVersion: "1",
			RunID:         row.RunID,
			Pass:          row.Pass,
			Agent:         row.Agent,
			JudgeRole:     contracts.JudgeRolePrimary,
			RuleID:        row.RuleID,
			Verdict:       row.Verdict,
			Rationale:     row.Rationale,
			OutputSha256:  strings.Repeat("a", 64),
			RubricVersion: row.RubricVersion,
			PromptVersion: row.PromptVersion,
			ResolvedAt:    row.ResolvedAt,
		})
	}
	writeJSONL(t, scoreRawPath, scoreRaw...)
	writeJSONL(t, complianceRawPath, complianceRaw...)

	agents := make([]contracts.AgentID, 0)
	seen := map[contracts.AgentID]struct{}{}
	for _, row := range scoreFinal {
		if _, ok := seen[row.Agent]; ok {
			continue
		}
		seen[row.Agent] = struct{}{}
		agents = append(agents, row.Agent)
	}
	syncPass1Manifests(t, runIO, agents)
	if len(agents) == 0 {
		return
	}
	marker, err := scorecore.BuildStep30DoneMarker(scorecore.Step30MarkerInputs{
		Agents: agents,
		Paths: scorecore.Step30MarkerPaths{
			ScoreFinal:      scoreFinalPath,
			ComplianceFinal: complianceFinalPath,
			ScoreRaw:        scoreRawPath,
			ComplianceRaw:   complianceRawPath,
		},
		ResolvedAt: time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	require.NoError(t, scorecore.WriteStep30DoneMarker(runIO, marker))
}

func syncPass1Manifests(t *testing.T, runIO internalio.RunContext, scorableAgents []contracts.AgentID) {
	t.Helper()
	scorable := make(map[contracts.AgentID]bool, len(scorableAgents))
	for _, agent := range scorableAgents {
		scorable[agent] = true
	}
	for _, agent := range []contracts.AgentID{"a1", "a2", "a3"} {
		writePass1Manifest(t, runIO, runIO.RunID, agent, scorable[agent])
	}
}

func writePass1Manifest(t *testing.T, runIO internalio.RunContext, runID contracts.RunID, agent contracts.AgentID, success bool) {
	t.Helper()
	path, err := runIO.ManifestPath(1, agent)
	require.NoError(t, err)
	if success {
		require.NoError(t, internalio.WriteJSONAtomic(path, contracts.Manifest{
			Kind: contracts.ManifestKindSuccess,
			Value: contracts.ManifestSuccess{
				Kind:          contracts.ManifestKindSuccess,
				SchemaVersion: "1",
				RunID:         runID,
				Pass:          1,
				Agent:         agent,
				BranchName:    "auto-improve/fixture",
				HeadSHA:       strings.Repeat("b", 40),
				BaseSHA:       strings.Repeat("a", 40),
				DiffPath:      filepath.ToSlash(filepath.Join("20-pass1", string(agent), "diff.patch")),
				SessionPath:   filepath.ToSlash(filepath.Join("20-pass1", string(agent), "session.jsonl")),
				ChecklistPath: filepath.ToSlash(filepath.Join("20-pass1", string(agent), "checklist-result.json")),
				PromptVersion: "phase0",
				StartedAt:     time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
				FinishedAt:    time.Date(2026, 4, 21, 10, 1, 0, 0, time.UTC),
			},
		}))
		return
	}
	require.NoError(t, internalio.WriteJSONAtomic(path, contracts.Manifest{
		Kind: contracts.ManifestKindError,
		Value: contracts.ManifestError{
			Kind:          contracts.ManifestKindError,
			SchemaVersion: "1",
			RunID:         runID,
			Pass:          1,
			Agent:         agent,
			ExitCode:      1,
			Reason:        "unknown",
			Detail:        "fixture non-scorable manifest",
			StartedAt:     time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
			FinishedAt:    time.Date(2026, 4, 21, 10, 1, 0, 0, time.UTC),
		},
	}))
}

func assertCandidateBodies(t *testing.T, runIO internalio.RunContext, candidates []contracts.Candidate) {
	t.Helper()
	for _, candidate := range candidates {
		path, err := runIO.ResolveRunRelative(candidate.ProposedBodyPath)
		require.NoError(t, err)
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, candidate.ProposedBodySha256, sha256String(string(data)))
		assert.Contains(t, string(data), "# "+experimentLessonTitleID(candidate.Title))
	}
}

func assertExperimentChecklist(t *testing.T, runIO internalio.RunContext, lessonIDs ...string) {
	t.Helper()
	path, err := runIO.ResolveRunRelative("40/experiment/checklist.md")
	require.NoError(t, err)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	text := string(data)
	assert.Contains(t, text, "# Checklist")
	for _, lessonID := range lessonIDs {
		assert.Contains(t, text, "`"+lessonID+"`")
	}
}

func experimentLessonTitleID(title string) string {
	return strings.TrimPrefix(title, "Experiment lesson for ")
}

func sha256String(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func testScoreEntries(runID contracts.RunID) []contracts.ScoreEntry {
	now := time.Date(2026, 4, 21, 11, 0, 0, 0, time.UTC)
	return []contracts.ScoreEntry{
		{
			SchemaVersion: "1",
			RunID:         runID,
			Pass:          1,
			Agent:         "a1",
			Dimension:     contracts.DimensionFidelity,
			Score:         80,
			Reasons:       "Missing the guard lets regressions slip into the changed code path.",
			VerdictPath:   contracts.VerdictPathSingle,
			RubricVersion: "default",
			PromptVersion: "phase0",
			ResolvedAt:    now,
		},
	}
}

func testComplianceEntry(runID contracts.RunID, ruleID string, verdict contracts.ComplianceVerdict) contracts.ComplianceEntry {
	return contracts.ComplianceEntry{
		SchemaVersion: "1",
		RunID:         runID,
		Pass:          1,
		Agent:         "a1",
		RuleID:        ruleID,
		Verdict:       verdict,
		Rationale:     fmt.Sprintf("Rule %s was skipped when the implementation touched the guarded path.", ruleID),
		VerdictPath:   contracts.VerdictPathSingle,
		RubricVersion: "default",
		PromptVersion: "phase0",
		ResolvedAt:    time.Date(2026, 4, 21, 11, 0, 0, 0, time.UTC),
	}
}

func registryAdded(ruleID, idempotencyKey string) contracts.RuleRegistryEntry {
	body := fmt.Sprintf("# %s added\n", ruleID)
	return contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindAdded,
		Value: contracts.RuleRegistryAdded{
			Kind:           contracts.RegistryKindAdded,
			SchemaVersion:  "1",
			RuleID:         ruleID,
			RulePath:       filepath.Join("rules", ruleID+".md"),
			Sha256:         sha256String(body),
			IdempotencyKey: idempotencyKey,
			VersionSeq:     1,
			ByRunID:        "2026-04-20-PR1-aaaaaaa",
			At:             time.Date(2026, 4, 20, 9, 0, 0, 0, time.UTC),
		},
	}
}

func registryUpdated(ruleID, idempotencyKey string) contracts.RuleRegistryEntry {
	prevBody := fmt.Sprintf("# %s added\n", ruleID)
	return contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindUpdated,
		Value: contracts.RuleRegistryUpdated{
			Kind:           contracts.RegistryKindUpdated,
			SchemaVersion:  "1",
			RuleID:         ruleID,
			RulePath:       filepath.Join("rules", ruleID+".md"),
			Sha256:         strings.Repeat("b", 64),
			PrevSha256:     sha256String(prevBody),
			IdempotencyKey: idempotencyKey,
			VersionSeq:     2,
			PrevHash:       strings.Repeat("c", 64),
			ByRunID:        "2026-04-20-PR2-bbbbbbb",
			At:             time.Date(2026, 4, 20, 9, 30, 0, 0, time.UTC),
		},
	}
}

func registryStatusChanged(ruleID string, prevStatus, newStatus contracts.RuleStatus, transition contracts.SunsetTransition, opID string) contracts.RuleRegistryEntry {
	return contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindStatusChanged,
		Value: contracts.RuleRegistryStatusChanged{
			Kind:          contracts.RegistryKindStatusChanged,
			SchemaVersion: "1",
			RuleID:        ruleID,
			PrevStatus:    prevStatus,
			NewStatus:     newStatus,
			Transition:    transition,
			OpID:          opID,
			VersionSeq:    1,
			BySunsetRunID: "sunset-3",
			At:            time.Date(2026, 4, 20, 10, 30, 0, 0, time.UTC),
		},
	}
}

func registryArchived(ruleID, opID string) contracts.RuleRegistryEntry {
	return contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindArchived,
		Value: contracts.RuleRegistryArchived{
			Kind:          contracts.RegistryKindArchived,
			SchemaVersion: "1",
			RuleID:        ruleID,
			PrevStatus:    contracts.RuleStatusActive,
			NewStatus:     contracts.RuleStatusArchived,
			OpID:          opID,
			VersionSeq:    1,
			BySunsetRunID: "sunset-1",
			At:            time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC),
		},
	}
}

func registryRolledBackForEntries(t *testing.T, registryPath string, entries []contracts.RuleRegistryEntry, targetOpID string) contracts.RuleRegistryEntry {
	t.Helper()
	normalized := append([]contracts.RuleRegistryEntry(nil), entries...)
	if registryPath != "" {
		lastSha := make(map[string]string)
		for idx, entry := range normalized {
			switch value := entry.Value.(type) {
			case contracts.RuleRegistryAdded:
				body := fmt.Sprintf("# %s added\n", value.RuleID)
				value.Sha256 = sha256String(body)
				lastSha[value.RuleID] = value.Sha256
				normalized[idx] = contracts.RuleRegistryEntry{Kind: entry.Kind, Value: value}
			case contracts.RuleRegistryUpdated:
				body := fmt.Sprintf("# %s updated\n", value.RuleID)
				if prev, ok := lastSha[value.RuleID]; ok {
					value.PrevSha256 = prev
				}
				value.Sha256 = sha256String(body)
				lastSha[value.RuleID] = value.Sha256
				normalized[idx] = contracts.RuleRegistryEntry{Kind: entry.Kind, Value: value}
			}
		}
	}
	var (
		offset      int64
		targetFound bool
		targetHash  string
		targetOff   int64
	)
	for _, entry := range normalized {
		payload, err := contracts.CanonicalMarshal(entry)
		require.NoError(t, err)
		sum := sha256.Sum256(payload)
		hash := hex.EncodeToString(sum[:])
		switch v := entry.Value.(type) {
		case contracts.RuleRegistryAdded:
			if v.IdempotencyKey == targetOpID {
				targetFound = true
				targetHash = hash
				targetOff = offset
			}
		case contracts.RuleRegistryUpdated:
			if v.IdempotencyKey == targetOpID {
				targetFound = true
				targetHash = hash
				targetOff = offset
			}
		}
		offset += int64(len(payload) + 1)
	}
	require.True(t, targetFound)
	return contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindRolledBack,
		Value: contracts.RuleRegistryRolledBack{
			Kind:           contracts.RegistryKindRolledBack,
			SchemaVersion:  "1",
			TargetOpID:     targetOpID,
			TargetOffset:   targetOff,
			TargetSha256:   targetHash,
			ByRunID:        "2026-04-20-PR3-ccccccc",
			RollbackReason: contracts.RollbackReasonTransactionalFailure,
			FailedStep:     contracts.FailedStep70,
			VersionSeq:     2,
			PrevHash:       strings.Repeat("f", 64),
			At:             time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC),
		},
	}
}

func registryRestored(ruleID, opID string) contracts.RuleRegistryEntry {
	return contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindRestored,
		Value: contracts.RuleRegistryRestored{
			Kind:          contracts.RegistryKindRestored,
			SchemaVersion: "1",
			RuleID:        ruleID,
			PrevStatus:    contracts.RuleStatusArchived,
			NewStatus:     contracts.RuleStatusActive,
			OpID:          opID,
			VersionSeq:    1,
			BySunsetRunID: "sunset-2",
			At:            time.Date(2026, 4, 20, 11, 0, 0, 0, time.UTC),
		},
	}
}

func indexOfCandidate(t *testing.T, candidates []contracts.Candidate, candidateID string) int {
	t.Helper()
	for i, candidate := range candidates {
		if candidate.CandidateID == candidateID {
			return i
		}
	}
	t.Fatalf("candidate not found: %s", candidateID)
	return -1
}
