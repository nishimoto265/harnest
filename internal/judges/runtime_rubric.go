package judges

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/registryview"
)

type activeRuleSnapshot struct {
	RuleID   string
	RulePath string
	Body     []byte
	Sha256   string
}

// ResolveRunRubricPath returns the default rubric when there are no active
// rules, otherwise it snapshots the default rubric plus active rule ids/bodies
// into the run directory so step30/60 judge against a stable per-run view.
func ResolveRunRubricPath(runCtx internalio.RunContext) (string, error) {
	activeRules, err := loadActiveRuleSnapshots(runCtx)
	if err != nil {
		return "", err
	}
	if len(activeRules) == 0 {
		return DefaultRubricPath()
	}
	path, err := runCtx.ResolveRunRelative("rubrics/runtime.md")
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err == nil {
		return path, nil
	} else if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("judges: stat runtime rubric: %w", err)
	}
	content, err := buildRuntimeRubric(activeRules)
	if err != nil {
		return "", err
	}
	if err := internalio.WriteAtomic(path, content); err != nil {
		return "", fmt.Errorf("judges: write runtime rubric: %w", err)
	}
	return path, nil
}

func loadActiveRuleSnapshots(runCtx internalio.RunContext) ([]activeRuleSnapshot, error) {
	registryPath := policyRegistryPath(runCtx)
	if _, err := os.Stat(registryPath); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("judges: stat rules registry: %w", err)
	}
	entries, err := internalio.ReadJSONL[contracts.RuleRegistryEntry](registryPath)
	if err != nil {
		return nil, fmt.Errorf("judges: read rules registry: %w", err)
	}
	states, err := registryview.Build(entries)
	if err != nil {
		return nil, fmt.Errorf("judges: build active rules view: %w", err)
	}
	active := registryview.Active(states)
	if len(active) == 0 {
		return nil, nil
	}
	registryBase := filepath.Dir(registryPath)
	ruleIDs := make([]string, 0, len(active))
	for ruleID := range active {
		ruleIDs = append(ruleIDs, ruleID)
	}
	sort.Strings(ruleIDs)
	snapshots := make([]activeRuleSnapshot, 0, len(ruleIDs))
	for _, ruleID := range ruleIDs {
		state := active[ruleID]
		bodyPath := filepath.Join(registryBase, state.RulePath)
		body, err := os.ReadFile(bodyPath)
		if err != nil {
			return nil, fmt.Errorf("judges: read active rule body rule_id=%s: %w", ruleID, err)
		}
		if got := runtimeRubricSHA256Hex(body); got != state.Sha256 {
			return nil, fmt.Errorf("judges: active rule body sha mismatch: rule_id=%s got=%s want=%s", ruleID, got, state.Sha256)
		}
		snapshots = append(snapshots, activeRuleSnapshot{
			RuleID:   ruleID,
			RulePath: state.RulePath,
			Body:     body,
			Sha256:   state.Sha256,
		})
	}
	return snapshots, nil
}

func policyRegistryPath(runCtx internalio.RunContext) string {
	if _, err := os.Stat(runCtx.PolicySnapshotRegistryPath()); err == nil {
		return runCtx.PolicySnapshotRegistryPath()
	}
	return runCtx.RulesRegistryPath()
}

func buildRuntimeRubric(activeRules []activeRuleSnapshot) ([]byte, error) {
	if len(activeRules) == 0 {
		return nil, ErrDefaultRubricEmpty
	}
	var out bytes.Buffer
	out.Write(defaultRubricContent)
	out.WriteString("\n\n## Active Rule IDs\n")
	for _, rule := range activeRules {
		fmt.Fprintf(&out, "- %s\n", rule.RuleID)
	}
	out.WriteString("\n## Active Rules\n")
	for _, rule := range activeRules {
		fmt.Fprintf(&out, "\n### %s\n", rule.RuleID)
		fmt.Fprintf(&out, "- path: %s\n", rule.RulePath)
		fmt.Fprintf(&out, "- sha256: %s\n\n", rule.Sha256)
		out.Write(rule.Body)
		if len(rule.Body) == 0 || rule.Body[len(rule.Body)-1] != '\n' {
			out.WriteByte('\n')
		}
	}
	return out.Bytes(), nil
}

func runtimeRubricSHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
