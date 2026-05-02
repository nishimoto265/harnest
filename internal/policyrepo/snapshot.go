package policyrepo

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/registryview"
)

func BranchSnapshotMatchesLocal(ctx context.Context, repoRoot, branch, runsBase string) (bool, error) {
	if err := fetchBranch(ctx, repoRoot, branch); err != nil {
		return false, err
	}
	remote, err := loadBranchSnapshot(ctx, repoRoot, branch)
	if err != nil {
		return false, err
	}
	local, err := loadLocalSnapshot(runsBase)
	if err != nil {
		return false, err
	}
	return snapshotsEqual(remote, local), nil
}

func snapshotsEqual(a, b snapshot) bool {
	if a.registryPresent != b.registryPresent || !bytes.Equal(a.registry, b.registry) || len(a.rules) != len(b.rules) {
		return false
	}
	for path, aBody := range a.rules {
		if !bytes.Equal(aBody, b.rules[path]) {
			return false
		}
	}
	return true
}

func applySnapshotToRunDir(runDir string, snap snapshot) error {
	dst := filepath.Join(runDir, "policy")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	return applySnapshot(dst, snap)
}

func syncSnapshotToWorktree(worktreeDir string, snap snapshot) error {
	repoRootDir := filepath.Join(worktreeDir, RepoDirName)
	registryDst := filepath.Join(worktreeDir, RegistryRepoRelPath)
	rulesDst := filepath.Join(worktreeDir, RulesRepoDirRelPath)
	if err := os.RemoveAll(registryDst); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.RemoveAll(rulesDst); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(repoRootDir, 0o755); err != nil {
		return err
	}
	if snap.registryPresent {
		if err := internalio.WriteAtomic(registryDst, snap.registry); err != nil {
			return err
		}
	}
	if len(snap.rules) == 0 {
		return nil
	}
	for rulePath, data := range snap.rules {
		if err := contracts.ValidateRulePath(rulePath); err != nil {
			return err
		}
		dstPath := filepath.Join(worktreeDir, RepoDirName, filepath.FromSlash(rulePath))
		if err := internalio.WriteAtomic(dstPath, data); err != nil {
			return err
		}
	}
	return nil
}

func loadBranchSnapshot(ctx context.Context, repoRoot, branch string) (snapshot, error) {
	files, err := listPolicyFiles(ctx, repoRoot, branch)
	if err != nil {
		return snapshot{}, err
	}
	if len(files) == 0 {
		return snapshot{}, fmt.Errorf("policyrepo: %s has no managed policy files", branch)
	}
	if !slices.Contains(files, RegistryRepoRelPath) {
		return snapshot{}, fmt.Errorf("policyrepo: %s is missing %s", branch, RegistryRepoRelPath)
	}
	snap := snapshot{rules: make(map[string][]byte, len(files))}
	for _, rel := range files {
		data, err := readBranchFile(ctx, repoRoot, branch, rel)
		if err != nil {
			return snapshot{}, err
		}
		switch {
		case rel == RegistryRepoRelPath:
			snap.registry = data
			snap.registryPresent = true
		case strings.HasPrefix(rel, RulesRepoDirRelPath+"/"):
			snap.rules[localRulePathForRepoRelative(rel)] = data
		}
	}
	if err := validateSnapshot(snap); err != nil {
		return snapshot{}, err
	}
	return snap, nil
}

func loadLocalSnapshot(runsBase string) (snapshot, error) {
	registryPath := filepath.Join(runsBase, registryLocalName)
	registryBytes, err := os.ReadFile(registryPath)
	if err != nil {
		if os.IsNotExist(err) {
			return snapshot{}, fmt.Errorf("policyrepo: local policy snapshot is missing %s", registryLocalName)
		}
		return snapshot{}, err
	}
	snap := snapshot{
		registry:        registryBytes,
		registryPresent: true,
		rules:           make(map[string][]byte),
	}
	rulesSrc := filepath.Join(runsBase, rulesLocalDirName)
	info, err := os.Lstat(rulesSrc)
	if err != nil {
		if os.IsNotExist(err) {
			if err := validateSnapshot(snap); err != nil {
				return snapshot{}, err
			}
			return snap, nil
		}
		return snapshot{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return snapshot{}, fmt.Errorf("policyrepo: local rules path must be a real directory: %s", rulesSrc)
	}
	err = filepath.WalkDir(rulesSrc, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(rulesSrc, path)
		if err != nil {
			return err
		}
		if rel == "." || d.IsDir() {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("policyrepo: local rule path must not be a symlink: %s", path)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("policyrepo: local rule path must be a regular file: %s", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		snap.rules[filepath.ToSlash(filepath.Join(rulesLocalDirName, rel))] = data
		return nil
	})
	if err != nil {
		return snapshot{}, err
	}
	if err := validateSnapshot(snap); err != nil {
		return snapshot{}, err
	}
	return snap, nil
}

func loadOptionalLocalSnapshot(runsBase string) (snapshot, error) {
	registryPath := filepath.Join(runsBase, registryLocalName)
	if _, err := os.Stat(registryPath); err == nil {
		return loadLocalSnapshot(runsBase)
	} else if err != nil && !os.IsNotExist(err) {
		return snapshot{}, err
	}
	rulesSrc := filepath.Join(runsBase, rulesLocalDirName)
	if _, statErr := os.Stat(rulesSrc); statErr == nil {
		return loadLocalSnapshot(runsBase)
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return snapshot{}, statErr
	}
	return snapshot{rules: map[string][]byte{}}, nil
}

func loadRepoLocalSnapshot(repoRoot string) (snapshot, error) {
	registryPath := filepath.Join(repoRoot, RegistryRepoRelPath)
	registryInfo, err := os.Lstat(registryPath)
	if err != nil {
		return snapshot{}, err
	}
	if registryInfo.Mode()&os.ModeSymlink != 0 || !registryInfo.Mode().IsRegular() {
		return snapshot{}, fmt.Errorf("policyrepo: repo-local registry path must be a regular file: %s", registryPath)
	}
	registryBytes, err := os.ReadFile(registryPath)
	if err != nil {
		return snapshot{}, err
	}
	snap := snapshot{
		registry:        registryBytes,
		registryPresent: true,
		rules:           map[string][]byte{},
	}
	rulesSrc := filepath.Join(repoRoot, RulesRepoDirRelPath)
	info, err := os.Lstat(rulesSrc)
	if err != nil {
		if os.IsNotExist(err) {
			if err := validateSnapshot(snap); err != nil {
				return snapshot{}, err
			}
			return snap, nil
		}
		return snapshot{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return snapshot{}, fmt.Errorf("policyrepo: repo-local rules path must be a real directory: %s", rulesSrc)
	}
	err = filepath.WalkDir(rulesSrc, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(rulesSrc, path)
		if err != nil {
			return err
		}
		if rel == "." || d.IsDir() {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("policyrepo: repo-local rule path must not be a symlink: %s", path)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("policyrepo: repo-local rule path must be a regular file: %s", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		snap.rules[filepath.ToSlash(filepath.Join(rulesLocalDirName, rel))] = data
		return nil
	})
	if err != nil {
		return snapshot{}, err
	}
	if err := validateSnapshot(snap); err != nil {
		return snapshot{}, err
	}
	return snap, nil
}

func writeSnapshotMetadata(runDir string, meta SnapshotMetadata) error {
	path := filepath.Join(runDir, "policy", metadataLocalName)
	return internalio.WriteJSONAtomic(path, meta)
}

func registryHead(path string) (string, error) {
	lines, err := internalio.RegistryLines(path)
	if err != nil {
		return "", err
	}
	if len(lines) == 0 {
		return "", nil
	}
	return lines[len(lines)-1].Sha256, nil
}

func listPolicyFiles(ctx context.Context, repoRoot, branch string) ([]string, error) {
	out, err := gitText(ctx, repoRoot, "ls-tree", "-r", "--name-only", "origin/"+branch, "--", RepoDirName)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	files := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if line == RegistryRepoRelPath || strings.HasPrefix(line, RulesRepoDirRelPath+"/") {
			files = append(files, line)
		}
	}
	slices.Sort(files)
	return files, nil
}

func readBranchFile(ctx context.Context, repoRoot, branch, rel string) ([]byte, error) {
	out, err := gitRaw(ctx, repoRoot, "show", "origin/"+branch+":"+rel)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func localRulePathForRepoRelative(rel string) string {
	return filepath.ToSlash(strings.TrimPrefix(rel, RepoDirName+"/"))
}

func validateSnapshot(snap snapshot) error {
	if !snap.registryPresent {
		if len(snap.rules) == 0 {
			return nil
		}
		return fmt.Errorf("policyrepo: registry is required when rules are present")
	}
	entries, err := decodeRegistryEntries(snap.registry)
	if err != nil {
		return err
	}
	states, err := registryview.Build(entries)
	if err != nil {
		return err
	}
	for ruleID, state := range registryview.Active(states) {
		body, ok := snap.rules[state.RulePath]
		if !ok {
			return fmt.Errorf("policyrepo: active rule body missing: rule_id=%s rule_path=%s", ruleID, state.RulePath)
		}
		if got := sha256Hex(body); got != state.Sha256 {
			return fmt.Errorf("policyrepo: active rule body sha mismatch: rule_id=%s got=%s want=%s", ruleID, got, state.Sha256)
		}
	}
	return nil
}

func decodeRegistryEntries(data []byte) ([]contracts.RuleRegistryEntry, error) {
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	entries := make([]contracts.RuleRegistryEntry, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry contracts.RuleRegistryEntry
		if err := contracts.DecodeStrictJSON([]byte(line), &entry); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func applySnapshot(runsBase string, snap snapshot) error {
	stageDir, err := os.MkdirTemp(runsBase, ".policy-stage-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stageDir)
	if err := stageSnapshot(stageDir, snap); err != nil {
		return err
	}
	backupDir, err := os.MkdirTemp(runsBase, ".policy-backup-")
	if err != nil {
		return err
	}
	restore, err := moveCurrentPolicyToBackup(runsBase, backupDir)
	if err != nil {
		_ = os.RemoveAll(backupDir)
		return err
	}
	if err := moveSnapshotIntoPlace(runsBase, stageDir, snap); err != nil {
		_ = restore()
		_ = os.RemoveAll(backupDir)
		return err
	}
	return os.RemoveAll(backupDir)
}

func stageSnapshot(stageDir string, snap snapshot) error {
	if snap.registryPresent {
		if err := internalio.WriteAtomic(filepath.Join(stageDir, registryLocalName), snap.registry); err != nil {
			return err
		}
	}
	for rulePath, data := range snap.rules {
		if err := contracts.ValidateRulePath(rulePath); err != nil {
			return err
		}
		if err := internalio.WriteAtomic(filepath.Join(stageDir, filepath.FromSlash(rulePath)), data); err != nil {
			return err
		}
	}
	return nil
}

func moveCurrentPolicyToBackup(runsBase, backupDir string) (func() error, error) {
	type moved struct {
		src string
		dst string
	}
	movedPaths := make([]moved, 0, 3)
	for _, name := range []string{registryLocalName, idempotencyLocalName, rulesLocalDirName} {
		src := filepath.Join(runsBase, name)
		if _, err := os.Stat(src); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		dst := filepath.Join(backupDir, name)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return nil, err
		}
		if err := os.Rename(src, dst); err != nil {
			return nil, err
		}
		movedPaths = append(movedPaths, moved{src: src, dst: dst})
	}
	return func() error {
		for i := len(movedPaths) - 1; i >= 0; i-- {
			if err := os.RemoveAll(movedPaths[i].src); err != nil && !os.IsNotExist(err) {
				return err
			}
			if err := os.Rename(movedPaths[i].dst, movedPaths[i].src); err != nil {
				return err
			}
		}
		return nil
	}, nil
}

func moveSnapshotIntoPlace(runsBase, stageDir string, snap snapshot) error {
	if snap.registryPresent {
		if err := os.Rename(filepath.Join(stageDir, registryLocalName), filepath.Join(runsBase, registryLocalName)); err != nil {
			return err
		}
	}
	if len(snap.rules) > 0 {
		if err := os.Rename(filepath.Join(stageDir, rulesLocalDirName), filepath.Join(runsBase, rulesLocalDirName)); err != nil {
			return err
		}
	}
	return nil
}
