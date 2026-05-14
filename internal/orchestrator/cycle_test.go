package orchestrator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun_CycleConformance_GoldenFixture(t *testing.T) {
	cfg := testConfig(t)
	orch, err := NewOrchestrator(cfg)
	require.NoError(t, err)

	orch.steps.Step10 = stubStep10{}
	orch.steps.Step20 = stubAgentSteps()
	orch.steps.Step40 = forcedCandidateStep{}
	orch.steps.Step50 = stubAgentSteps()
	orch.steps.Step60 = scriptedStep60Step{decode: orch.decoders.Step60}
	orch.steps.Step70 = orchestratorStep70{
		git: testStep70Git{
			head: strings.Repeat("1", 40),
		},
		resolver: testStep70Resolver{},
	}

	runID := contracts.RunID("2026-04-21-PR43-abcdeff")
	require.NoError(t, orch.Run(context.Background(), 43, RunOptions{RunID: runID}))

	runCtx, err := internalio.NewRunContext(runID, cfg.Paths.Runs, cfg.Worktree.Base)
	require.NoError(t, err)

	got, err := buildCycleArtifactBundle(runCtx)
	require.NoError(t, err)

	fixturePath := filepath.Join(repoRootFromTestFile(t), "testdata", "golden_run", "full_cycle_adopt", "bundle.json")
	if update := strings.TrimSpace(getenv("UPDATE_GOLDEN_BUNDLE")); update == "1" {
		data, err := json.MarshalIndent(got, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(fixturePath, append(data, '\n'), 0o644))
		return
	}
	fixtureBytes, err := os.ReadFile(fixturePath)
	require.NoError(t, err)
	var want cycleArtifactBundle
	require.NoError(t, json.Unmarshal(fixtureBytes, &want))
	require.NoError(t, err)

	assert.Equal(t, want, got)
}

type cycleArtifactBundle map[string]any

func buildCycleArtifactBundle(runCtx internalio.RunContext) (cycleArtifactBundle, error) {
	artifacts := cycleArtifactPaths(runCtx)
	if candidates, err := internalio.ReadJSON[contracts.Candidates](filepath.Join(runCtx.RunDir(), "40", "candidates.json")); err == nil {
		for _, candidate := range candidates.Candidates {
			artifacts[candidate.ProposedBodyPath] = filepath.Join(runCtx.RunDir(), filepath.FromSlash(candidate.ProposedBodyPath))
		}
	}
	if entries, err := internalio.ReadJSONL[contracts.RuleRegistryEntry](runCtx.RulesRegistryPath()); err == nil {
		for _, entry := range entries {
			switch v := entry.Value.(type) {
			case contracts.RuleRegistryAdded:
				artifacts[v.RulePath] = filepath.Join(runCtx.RunsBase, filepath.FromSlash(v.RulePath))
			case contracts.RuleRegistryUpdated:
				artifacts[v.RulePath] = filepath.Join(runCtx.RunsBase, filepath.FromSlash(v.RulePath))
			}
		}
	}
	bundle := make(cycleArtifactBundle, len(artifacts))
	for rel, path := range artifacts {
		value, err := readAndNormalizeArtifact(path, runCtx)
		if err != nil {
			return nil, err
		}
		bundle[rel] = value
	}
	return bundle, nil
}

func cycleArtifactPaths(runCtx internalio.RunContext) map[string]string {
	paths := map[string]string{
		"task-package.json":       runCtx.TaskPackagePath(),
		"30/done.marker":          filepath.Join(runCtx.RunDir(), "30", "done.marker"),
		"40/candidates.json":      filepath.Join(runCtx.RunDir(), "40", "candidates.json"),
		"40/classification.jsonl": filepath.Join(runCtx.RunDir(), "40", "classification.jsonl"),
		"60/done.marker":          filepath.Join(runCtx.RunDir(), "60", "done.marker"),
		"60/pairwise.jsonl":       filepath.Join(runCtx.RunDir(), "60", "pairwise.jsonl"),
		"70/decision.json":        filepath.Join(runCtx.RunDir(), "70", "decision.json"),
		"processed.jsonl":         filepath.Join(runCtx.RunsBase, "processed.jsonl"),
		"rules-registry.jsonl":    filepath.Join(runCtx.RunsBase, "rules-registry.jsonl"),
	}
	for _, agent := range []string{"a1", "a2", "a3"} {
		paths[filepath.Join("20-pass1", agent, "manifest.json")] = filepath.Join(runCtx.RunDir(), "20-pass1", agent, "manifest.json")
		paths[filepath.Join("50-pass2", agent, "manifest.json")] = filepath.Join(runCtx.RunDir(), "50-pass2", agent, "manifest.json")
	}
	return paths
}

func readAndNormalizeArtifact(path string, runCtx internalio.RunContext) (any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if strings.HasSuffix(path, ".jsonl") {
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		if len(lines) == 1 && lines[0] == "" {
			return []any{}, nil
		}
		rows := make([]any, 0, len(lines))
		for _, line := range lines {
			var value any
			if err := json.Unmarshal([]byte(line), &value); err != nil {
				return nil, err
			}
			rows = append(rows, normalizeArtifactValue(value, runCtx, ""))
		}
		return rows, nil
	}
	trimmed := strings.TrimSpace(string(data))
	if !strings.HasSuffix(path, ".json") {
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			var value any
			if err := json.Unmarshal([]byte(trimmed), &value); err == nil {
				return normalizeArtifactValue(value, runCtx, ""), nil
			}
		}
		return normalizeArtifactString(trimmed, runCtx), nil
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	return normalizeArtifactValue(value, runCtx, ""), nil
}

func normalizeArtifactValue(value any, runCtx internalio.RunContext, key string) any {
	switch vv := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(vv))
		for k, v := range vv {
			out[k] = normalizeArtifactValue(v, runCtx, k)
		}
		return out
	case []any:
		out := make([]any, 0, len(vv))
		for _, v := range vv {
			out = append(out, normalizeArtifactValue(v, runCtx, key))
		}
		return out
	case string:
		if isTimeKey(key) {
			return "__TIME__"
		}
		if isHashKey(key) {
			return "__HASH__"
		}
		if isMarkerHashKey(key) {
			return "__HASH__"
		}
		return normalizeArtifactString(vv, runCtx)
	default:
		return vv
	}
}

func normalizeArtifactString(value string, runCtx internalio.RunContext) string {
	value = filepath.ToSlash(value)
	runDir := filepath.ToSlash(runCtx.RunDir())
	runsBase := filepath.ToSlash(runCtx.RunsBase)
	worktreeBase := filepath.ToSlash(runCtx.WorktreeBase)
	switch {
	case strings.HasPrefix(value, runDir+"/"):
		return "${RUN_DIR}/" + strings.TrimPrefix(value, runDir+"/")
	case value == runDir:
		return "${RUN_DIR}"
	case strings.HasPrefix(value, runsBase+"/"):
		return "${RUNS_BASE}/" + strings.TrimPrefix(value, runsBase+"/")
	case value == runsBase:
		return "${RUNS_BASE}"
	case strings.HasPrefix(value, worktreeBase+"/"):
		return "${WORKTREE_BASE}/" + strings.TrimPrefix(value, worktreeBase+"/")
	case value == worktreeBase:
		return "${WORKTREE_BASE}"
	default:
		return value
	}
}

func isTimeKey(key string) bool {
	switch key {
	case "at", "created_at", "started_at", "finished_at", "resolved_at", "decided_at":
		return true
	default:
		return false
	}
}

func isMarkerHashKey(key string) bool {
	switch key {
	case "scores_final", "compliance_final", "pairwise_final", "scores_raw", "compliance_raw",
		"pass1_scores", "pass1_compliance", "pass2_outputs", "candidate_rules", "expected_compliance":
		return true
	default:
		return false
	}
}

func isHashKey(key string) bool {
	return key == "sha256" || strings.HasSuffix(key, "_sha256")
}

func getenv(key string) string {
	return strings.TrimSpace(os.Getenv(key))
}
