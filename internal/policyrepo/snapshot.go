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
	"github.com/nishimoto265/auto-improve/internal/harnessinstall"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/lessons"
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
	remote, err = snapshotWithGeneratedChecklist(remote)
	if err != nil {
		return false, err
	}
	local, err = snapshotWithGeneratedChecklist(local)
	if err != nil {
		return false, err
	}
	return snapshotsEqual(remote, local), nil
}

func snapshotsEqual(a, b snapshot) bool {
	if a.registryPresent != b.registryPresent || !bytes.Equal(a.registry, b.registry) || len(a.rules) != len(b.rules) || len(a.files) != len(b.files) {
		return false
	}
	for path, aBody := range a.rules {
		if !bytes.Equal(aBody, b.rules[path]) {
			return false
		}
	}
	for path, aBody := range a.files {
		if !bytes.Equal(aBody, b.files[path]) {
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
	normalized, err := snapshotWithGeneratedChecklist(snap)
	if err != nil {
		return err
	}
	snap = normalized
	repoRootDir := filepath.Join(worktreeDir, RepoDirName)
	for _, rel := range []string{RegistryRepoRelPath, RulesRepoDirRelPath, GuidanceRepoDirPath, OverlayRepoDirPath} {
		if err := os.RemoveAll(filepath.Join(worktreeDir, rel)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.MkdirAll(repoRootDir, 0o755); err != nil {
		return err
	}
	if snap.registryPresent {
		if err := internalio.WriteAtomic(filepath.Join(worktreeDir, RegistryRepoRelPath), snap.registry); err != nil {
			return err
		}
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
	for rel, data := range snap.files {
		if err := validateManagedFilePath(rel); err != nil {
			return err
		}
		if err := internalio.WriteAtomic(filepath.Join(worktreeDir, filepath.FromSlash(rel)), data); err != nil {
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
		return defaultBootstrapSnapshot(), nil
	}
	if !slices.Contains(files, RegistryRepoRelPath) {
		return snapshot{}, fmt.Errorf("policyrepo: %s is missing %s", branch, RegistryRepoRelPath)
	}
	snap := snapshot{rules: make(map[string][]byte, len(files)), files: make(map[string][]byte, len(files))}
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
		default:
			snap.files[rel] = data
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
		files:           make(map[string][]byte),
	}
	rulesSrc := filepath.Join(runsBase, rulesLocalDirName)
	info, err := os.Lstat(rulesSrc)
	if err != nil {
		if os.IsNotExist(err) {
			if err := loadLocalManagedFiles(runsBase, snap.files); err != nil {
				return snapshot{}, err
			}
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
	if err := loadLocalManagedFiles(runsBase, snap.files); err != nil {
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
	return snapshot{rules: map[string][]byte{}, files: map[string][]byte{}}, nil
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
		files:           map[string][]byte{},
	}
	rulesSrc := filepath.Join(repoRoot, RulesRepoDirRelPath)
	info, err := os.Lstat(rulesSrc)
	if err != nil {
		if os.IsNotExist(err) {
			if err := loadRepoLocalManagedFiles(repoRoot, snap.files); err != nil {
				return snapshot{}, err
			}
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
	if err := loadRepoLocalManagedFiles(repoRoot, snap.files); err != nil {
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
	out, err := gitText(ctx, repoRoot, "ls-tree", "-r", "origin/"+branch, "--", RepoDirName, OverlayRepoDirPath)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(out)) == "" {
		return nil, nil
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	files := make([]string, 0, len(lines))
	for _, line := range lines {
		mode, rel, ok := parseTreeLine(line)
		if !ok {
			return nil, fmt.Errorf("policyrepo: parse policy tree entry: %s", line)
		}
		if mode != "100644" && mode != "100755" {
			return nil, fmt.Errorf("policyrepo: managed policy file must be a regular file: %s", rel)
		}
		line = strings.TrimSpace(rel)
		if line == "" {
			continue
		}
		if isManagedFilePath(line) {
			files = append(files, line)
		}
	}
	slices.Sort(files)
	return files, nil
}

func parseTreeLine(line string) (string, string, bool) {
	meta, rel, ok := strings.Cut(line, "\t")
	if !ok {
		return "", "", false
	}
	fields := strings.Fields(meta)
	if len(fields) != 3 || fields[1] != "blob" {
		return "", "", false
	}
	return fields[0], rel, true
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
	for rel := range snap.files {
		if err := validateManagedFilePath(rel); err != nil {
			return err
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
	normalized, err := snapshotWithGeneratedChecklist(snap)
	if err != nil {
		return err
	}
	snap = normalized
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
	for rel, data := range snap.files {
		if err := validateManagedFilePath(rel); err != nil {
			return err
		}
		if err := internalio.WriteAtomic(filepath.Join(stageDir, filepath.FromSlash(rel)), data); err != nil {
			return err
		}
	}
	return nil
}

func snapshotWithGeneratedChecklist(snap snapshot) (snapshot, error) {
	items, err := checklistLessonsForSnapshot(snap)
	if err != nil {
		return snapshot{}, err
	}
	files := make(map[string][]byte, len(snap.files)+1)
	for rel, data := range snap.files {
		files[rel] = data
	}
	files[OverlayRepoDirPath+"/checklist.md"] = []byte(lessons.RenderChecklist(items))
	snap.files = files
	return snap, nil
}

func checklistLessonsForSnapshot(snap snapshot) ([]lessons.Lesson, error) {
	if !snap.registryPresent || len(strings.TrimSpace(string(snap.registry))) == 0 {
		return nil, nil
	}
	entries, err := decodeRegistryEntries(snap.registry)
	if err != nil {
		return nil, err
	}
	states, err := registryview.Build(entries)
	if err != nil {
		return nil, err
	}
	active := registryview.Active(states)
	items := make([]lessons.Lesson, 0, len(active))
	for ruleID, state := range active {
		body, ok := snap.rules[state.RulePath]
		if !ok {
			return nil, fmt.Errorf("policyrepo: active rule body missing for checklist: rule_id=%s rule_path=%s", ruleID, state.RulePath)
		}
		item, err := lessons.ParseLessonMarkdown(state.RulePath, ruleID, body)
		if err != nil {
			item = fallbackChecklistLesson(ruleID, state.RulePath, body)
		}
		items = append(items, item)
	}
	return items, nil
}

func fallbackChecklistLesson(ruleID, rulePath string, body []byte) lessons.Lesson {
	item := firstMarkdownHeading(body)
	if item == "" {
		item = "Review " + ruleID
	}
	return lessons.Lesson{
		ID:   ruleID,
		Path: rulePath,
		Metadata: lessons.Metadata{
			Status:     lessons.StatusActive,
			Severity:   lessons.SeverityMedium,
			Confidence: lessons.ConfidenceMedium,
			Category:   "general",
		},
		ChecklistItem: item,
	}
}

func firstMarkdownHeading(body []byte) string {
	lines := strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, "# "))
		}
	}
	return ""
}

func moveCurrentPolicyToBackup(runsBase, backupDir string) (func() error, error) {
	type moved struct {
		src string
		dst string
	}
	movedPaths := make([]moved, 0, 5)
	for _, name := range []string{registryLocalName, idempotencyLocalName, rulesLocalDirName, OverlayRepoDirPath, RepoDirName} {
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
	for _, name := range []string{OverlayRepoDirPath, RepoDirName} {
		if _, err := os.Stat(filepath.Join(stageDir, name)); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if err := os.Rename(filepath.Join(stageDir, name), filepath.Join(runsBase, name)); err != nil {
			return err
		}
	}
	return nil
}

func loadLocalManagedFiles(root string, out map[string][]byte) error {
	return loadManagedFiles(root, out)
}

func loadRepoLocalManagedFiles(root string, out map[string][]byte) error {
	return loadManagedFiles(root, out)
}

func loadManagedFiles(root string, out map[string][]byte) error {
	for _, base := range []string{OverlayRepoDirPath, GuidanceRepoDirPath} {
		dir := filepath.Join(root, filepath.FromSlash(base))
		info, err := os.Lstat(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("policyrepo: managed policy path must be a real directory: %s", dir)
		}
		if err := internalio.EnsureNoSymlinkPathComponents(dir); err != nil {
			return fmt.Errorf("policyrepo: managed policy path rejected: %w", err)
		}
		err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if d.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("policyrepo: managed policy file must not be a symlink: %s", path)
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("policyrepo: managed policy file must be regular: %s", path)
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if !isManagedFilePath(rel) || rel == RegistryRepoRelPath || strings.HasPrefix(rel, RulesRepoDirRelPath+"/") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			out[rel] = data
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func validateManagedFilePath(rel string) error {
	if !isManagedFilePath(rel) || rel == RegistryRepoRelPath || strings.HasPrefix(rel, RulesRepoDirRelPath+"/") {
		return fmt.Errorf("policyrepo: unsupported managed policy path: %s", rel)
	}
	return nil
}

func isManagedFilePath(rel string) bool {
	rel = filepath.ToSlash(filepath.Clean(strings.TrimSpace(rel)))
	rel = strings.TrimPrefix(rel, "./")
	switch rel {
	case RegistryRepoRelPath:
		return true
	case "", ".", RepoDirName, OverlayRepoDirPath, RulesRepoDirRelPath, GuidanceRepoDirPath:
		return false
	}
	if strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) || strings.Contains(rel, "\x00") {
		return false
	}
	return strings.HasPrefix(rel, RulesRepoDirRelPath+"/") ||
		rel == OverlayRepoDirPath+"/checklist.md" ||
		strings.HasPrefix(rel, OverlayRepoDirPath+"/hooks/") ||
		strings.HasPrefix(rel, GuidanceRepoDirPath+"/")
}

func defaultBootstrapSnapshot() snapshot {
	return snapshot{
		registry:        nil,
		registryPresent: true,
		rules:           map[string][]byte{},
		files: map[string][]byte{
			".auto-improve/checklist.md":                         []byte("# Checklist\n\nNo active lessons.\n"),
			".auto-improve/hooks/verify-checklist-result.sh":     []byte(harnessinstall.RenderHookScript()),
			"auto-improve/guidance/AGENTS.md.template":           []byte(harnessinstall.RenderGuidance(harnessinstall.ProviderCodex)),
			"auto-improve/guidance/CLAUDE.md.template":           []byte(harnessinstall.RenderGuidance(harnessinstall.ProviderClaude)),
			"auto-improve/guidance/provider-hooks.json.template": []byte(harnessinstall.RenderProviderHooksJSON()),
		},
	}
}
