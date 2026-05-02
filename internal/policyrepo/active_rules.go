package policyrepo

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/registryview"
)

func LoadSnapshotMetadata(runCtx internalio.RunContext) (SnapshotMetadata, bool, error) {
	path := filepath.Join(runCtx.RunDir(), "policy", metadataLocalName)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return SnapshotMetadata{}, false, nil
		}
		return SnapshotMetadata{}, false, err
	}
	meta, err := internalio.ReadJSON[SnapshotMetadata](path)
	if err != nil {
		return SnapshotMetadata{}, false, err
	}
	return meta, true, nil
}

func RegistryPathForRun(runCtx internalio.RunContext) (string, error) {
	policyDir := filepath.Join(runCtx.RunDir(), "policy")
	snapshotPath := runCtx.PolicySnapshotRegistryPath()
	if _, err := os.Stat(snapshotPath); err == nil {
		return snapshotPath, nil
	} else if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if _, err := os.Stat(policyDir); err == nil {
		return "", fmt.Errorf("policyrepo: run policy snapshot is missing %s", registryLocalName)
	} else if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	return runCtx.RulesRegistryPath(), nil
}

func LoadActiveRulesForRun(runCtx internalio.RunContext) ([]ActiveRule, error) {
	registryPath, err := RegistryPathForRun(runCtx)
	if err != nil {
		return nil, err
	}
	return LoadActiveRules(registryPath)
}

func LoadActiveRules(registryPath string) ([]ActiveRule, error) {
	if _, err := os.Stat(registryPath); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	lines, err := internalio.RegistryLines(registryPath)
	if err != nil {
		return nil, err
	}
	entries := make([]contracts.RuleRegistryEntry, 0, len(lines))
	for _, line := range lines {
		entries = append(entries, line.Entry)
	}
	states, err := registryview.Build(entries)
	if err != nil {
		return nil, err
	}
	active := registryview.Active(states)
	if len(active) == 0 {
		return nil, nil
	}
	ruleIDs := make([]string, 0, len(active))
	for ruleID := range active {
		ruleIDs = append(ruleIDs, ruleID)
	}
	sort.Strings(ruleIDs)
	base := filepath.Dir(registryPath)
	rules := make([]ActiveRule, 0, len(ruleIDs))
	for _, ruleID := range ruleIDs {
		state := active[ruleID]
		if err := contracts.ValidateRulePath(state.RulePath); err != nil {
			return nil, err
		}
		bodyPath := filepath.Join(base, filepath.FromSlash(state.RulePath))
		body, err := internalio.OpenValidatedRegularFile(bodyPath, base)
		if err != nil {
			return nil, err
		}
		if got := sha256Hex(body); got != state.Sha256 {
			return nil, fmt.Errorf("policyrepo: active rule body sha mismatch: rule_id=%s got=%s want=%s", ruleID, got, state.Sha256)
		}
		rules = append(rules, ActiveRule{
			RuleID:   ruleID,
			RulePath: state.RulePath,
			Body:     string(body),
		})
	}
	return rules, nil
}
