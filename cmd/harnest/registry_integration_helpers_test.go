package main

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
	"github.com/stretchr/testify/require"
)

func writeIntegrationConfigForRepo(t *testing.T, root, repoRoot, runsBase, worktreeBase string) {
	t.Helper()
	content := "repo:\n" +
		"  root: " + repoRoot + "\n" +
		"  default_branch: main\n" +
		"  best_branch: harnest/best\n" +
		"paths:\n" +
		"  runs: " + runsBase + "\n" +
		"worktree:\n" +
		"  base: " + worktreeBase + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "config.yaml"), []byte(content), 0o644))
}

func seedIntegrationDeprecatedRule(t *testing.T, registryPath, ruleID string) {
	t.Helper()
	added := contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindAdded,
		Value: contracts.RuleRegistryAdded{
			Kind:           contracts.RegistryKindAdded,
			SchemaVersion:  "1",
			RuleID:         ruleID,
			RulePath:       "rules/" + ruleID + ".md",
			Sha256:         strings.Repeat("1", 64),
			IdempotencyKey: strings.Repeat("a", 64),
			VersionSeq:     1,
			PrevHash:       "",
			ByRunID:        "2026-04-21-PR1-abcdef0",
			At:             time.Date(2026, 4, 21, 8, 0, 0, 0, time.UTC),
		},
	}
	result, err := internalio.AppendRegistryEntry(registryPath, added)
	require.NoError(t, err)
	deprecated := contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindStatusChanged,
		Value: contracts.RuleRegistryStatusChanged{
			Kind:          contracts.RegistryKindStatusChanged,
			SchemaVersion: "1",
			RuleID:        ruleID,
			PrevStatus:    contracts.RuleStatusActive,
			NewStatus:     contracts.RuleStatusDeprecated,
			Transition:    contracts.SunsetTransitionDeprecate,
			OpID:          strings.Repeat("b", 64),
			VersionSeq:    2,
			PrevHash:      result.Sha256,
			BySunsetRunID: "seed-sunset",
			At:            time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC),
		},
	}
	_, err = internalio.AppendRegistryEntry(registryPath, deprecated)
	require.NoError(t, err)
}

func readIntegrationManifest(t *testing.T, runDir string, pass int, agent contracts.AgentID) contracts.ManifestSuccess {
	t.Helper()
	prefix := fmt.Sprintf("20-pass%d", pass)
	if pass == 2 {
		prefix = "50-pass2"
	}
	manifest, err := internalio.ReadJSON[contracts.Manifest](filepath.Join(runDir, prefix, string(agent), "manifest.json"))
	require.NoError(t, err, "run files:\n%s", listIntegrationRunFiles(t, runDir))
	require.Equal(t, contracts.ManifestKindSuccess, manifest.Kind)
	success, ok := manifest.Value.(contracts.ManifestSuccess)
	require.True(t, ok)
	return success
}

func listIntegrationRunFiles(t *testing.T, runDir string) string {
	t.Helper()
	var out strings.Builder
	err := filepath.WalkDir(runDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(runDir, path)
		if err != nil {
			return err
		}
		out.WriteString(rel)
		out.WriteByte('\n')
		return nil
	})
	require.NoError(t, err)
	return out.String()
}

func sha256String(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func appendPolicyBranchConfig(t *testing.T, path, policyBranch string) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	content := string(data)
	replacement := "  best_branch: harnest/best\n"
	if policyBranch != "" {
		replacement += "  policy_branch: " + policyBranch + "\n"
	}
	content = strings.Replace(content, "  best_branch: harnest/best\n", replacement, 1)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}
