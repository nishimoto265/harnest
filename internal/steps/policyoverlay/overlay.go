package policyoverlay

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nishimoto265/auto-improve/internal/candidaterules"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/harnessinstall"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/policyrepo"
)

type ExperimentLesson struct {
	ID   string
	Body string
}

type checklistItem struct {
	ID   string
	Text string
}

func ExperimentsFromRulePayloads(payloads []candidaterules.RulePayload) []ExperimentLesson {
	if len(payloads) == 0 {
		return nil
	}
	out := make([]ExperimentLesson, 0, len(payloads))
	for _, payload := range payloads {
		out = append(out, ExperimentLesson{
			ID:   payload.ID,
			Body: payload.ProposedBody,
		})
	}
	return out
}

func Apply(worktreePath string, activeRules []policyrepo.ActiveRule, experimentLessons []ExperimentLesson) error {
	return ApplyWithSnapshot(worktreePath, "", activeRules, experimentLessons)
}

func ApplyWithSnapshot(worktreePath, policySnapshotDir string, activeRules []policyrepo.ActiveRule, experimentLessons []ExperimentLesson) error {
	root := filepath.Clean(worktreePath)
	if !filepath.IsAbs(root) {
		return fmt.Errorf("policyoverlay: worktree path must be absolute: %s", worktreePath)
	}
	if err := internalio.EnsureNoSymlinkPathComponents(root); err != nil {
		return fmt.Errorf("policyoverlay: worktree path rejected: %w", err)
	}
	overlayDir := filepath.Join(root, ".auto-improve")
	lessonsDir := filepath.Join(overlayDir, "lessons")
	if err := ensureRealDir(overlayDir); err != nil {
		return err
	}
	if err := installHarnessGuidance(root, policySnapshotDir); err != nil {
		return err
	}
	if strings.TrimSpace(policySnapshotDir) != "" {
		if err := copySnapshotOverlay(root, policySnapshotDir); err != nil {
			return err
		}
	}
	if err := replaceRealDir(lessonsDir); err != nil {
		return err
	}

	items := make([]checklistItem, 0, len(activeRules)+len(experimentLessons))
	for _, rule := range activeRules {
		id := safeLessonID(rule.RuleID)
		if id == "" {
			continue
		}
		if err := writeLesson(lessonsDir, id, rule.Body); err != nil {
			return err
		}
		items = append(items, checklistItem{ID: id, Text: checklistItemText(rule.Body, rule.RuleID)})
	}
	for _, lesson := range experimentLessons {
		id := safeLessonID(lesson.ID)
		if id == "" {
			continue
		}
		if err := writeLesson(lessonsDir, id, lesson.Body); err != nil {
			return err
		}
		items = append(items, checklistItem{ID: id, Text: checklistItemText(lesson.Body, lesson.ID)})
	}
	if err := internalio.WriteAtomic(filepath.Join(overlayDir, "checklist.md"), []byte(renderChecklist(items))); err != nil {
		return err
	}
	return nil
}

func installHarnessGuidance(root, policySnapshotDir string) error {
	templates, err := loadHarnessTemplates(policySnapshotDir)
	if err != nil {
		return err
	}
	plan, err := harnessinstall.Plan(root, harnessinstall.InstallOptions{
		Providers: []string{harnessinstall.ProviderClaude, harnessinstall.ProviderCodex},
		Templates: templates,
	}, harnessinstall.PlanOptions{})
	if err != nil {
		return fmt.Errorf("policyoverlay: install harness guidance: %w", err)
	}
	if _, err := harnessinstall.Apply(plan); err != nil {
		return fmt.Errorf("policyoverlay: install harness guidance: %w", err)
	}
	return nil
}

func loadHarnessTemplates(policySnapshotDir string) (harnessinstall.Templates, error) {
	if strings.TrimSpace(policySnapshotDir) == "" {
		return harnessinstall.Templates{}, nil
	}
	snapshotRoot := filepath.Clean(policySnapshotDir)
	if !filepath.IsAbs(snapshotRoot) {
		return harnessinstall.Templates{}, fmt.Errorf("policyoverlay: policy snapshot path must be absolute: %s", policySnapshotDir)
	}
	if err := internalio.EnsureNoSymlinkPathComponents(snapshotRoot); err != nil {
		return harnessinstall.Templates{}, fmt.Errorf("policyoverlay: policy snapshot path rejected: %w", err)
	}
	var templates harnessinstall.Templates
	var err error
	if templates.CodexGuidance, err = readOptionalTemplate(snapshotRoot, "auto-improve/guidance/AGENTS.md.template"); err != nil {
		return harnessinstall.Templates{}, err
	}
	if templates.ClaudeGuidance, err = readOptionalTemplate(snapshotRoot, "auto-improve/guidance/CLAUDE.md.template"); err != nil {
		return harnessinstall.Templates{}, err
	}
	providerHooks, err := readOptionalTemplateBytes(snapshotRoot, "auto-improve/guidance/provider-hooks.json.template")
	if err != nil {
		return harnessinstall.Templates{}, err
	}
	templates.ProviderHooksJSON = providerHooks
	return templates, nil
}

func readOptionalTemplate(root, rel string) (string, error) {
	data, err := readOptionalTemplateBytes(root, rel)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func readOptionalTemplateBytes(root, rel string) ([]byte, error) {
	if err := contracts.EnsureCleanRelativePath(rel); err != nil {
		return nil, err
	}
	path := filepath.Join(root, filepath.FromSlash(rel))
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("policyoverlay: harness template must be a regular file: %s", path)
	}
	return os.ReadFile(path)
}

func copySnapshotOverlay(root, policySnapshotDir string) error {
	snapshotRoot := filepath.Clean(policySnapshotDir)
	if !filepath.IsAbs(snapshotRoot) {
		return fmt.Errorf("policyoverlay: policy snapshot path must be absolute: %s", policySnapshotDir)
	}
	if err := internalio.EnsureNoSymlinkPathComponents(snapshotRoot); err != nil {
		return fmt.Errorf("policyoverlay: policy snapshot path rejected: %w", err)
	}
	srcRoot := filepath.Join(snapshotRoot, ".auto-improve")
	info, err := os.Lstat(srcRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("policyoverlay: snapshot overlay path must be a real directory: %s", srcRoot)
	}
	return filepath.WalkDir(srcRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcRoot, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		dst := filepath.Join(root, ".auto-improve", rel)
		if d.IsDir() {
			return ensureRealDir(dst)
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("policyoverlay: snapshot overlay file must not be a symlink: %s", path)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("policyoverlay: snapshot overlay file must be regular: %s", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := ensureRealDir(filepath.Dir(dst)); err != nil {
			return err
		}
		if err := internalio.WriteAtomic(dst, data); err != nil {
			return err
		}
		return os.Chmod(dst, info.Mode().Perm())
	})
}

func ensureRealDir(path string) error {
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("policyoverlay: path must be a real directory: %s", path)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	return os.Mkdir(path, 0o755)
}

func replaceRealDir(path string) error {
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("policyoverlay: path must be a real directory: %s", path)
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.Mkdir(path, 0o755)
}

func writeLesson(lessonsDir, id, body string) error {
	path := filepath.Join(lessonsDir, id+".md")
	return internalio.WriteAtomic(path, []byte(body))
}

func renderChecklist(items []checklistItem) string {
	var out strings.Builder
	out.WriteString("# Checklist\n\n")
	if len(items) == 0 {
		out.WriteString("No active lessons.\n")
		return out.String()
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].ID < items[j].ID
	})
	for _, item := range items {
		fmt.Fprintf(&out, "- [ ] `%s` %s\n", item.ID, item.Text)
	}
	return out.String()
}

func checklistItemText(body, fallbackID string) string {
	if text := extractMarkdownSection(body, "Checklist Item"); text != "" {
		return singleLine(text)
	}
	return "Review learned policy " + fallbackID
}

func extractMarkdownSection(body, section string) string {
	lines := strings.Split(body, "\n")
	inSection := false
	var picked []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") {
			title := strings.TrimSpace(strings.TrimPrefix(trimmed, "## "))
			if inSection && title != section {
				break
			}
			inSection = title == section
			continue
		}
		if inSection {
			picked = append(picked, line)
		}
	}
	return strings.TrimSpace(strings.Join(picked, "\n"))
}

func singleLine(value string) string {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return ""
	}
	return strings.Join(fields, " ")
}

func safeLessonID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" || strings.Contains(id, "/") || strings.Contains(id, "\\") || strings.Contains(id, "\x00") {
		return ""
	}
	id = strings.TrimSuffix(id, ".md")
	if id == "." || id == ".." || strings.Contains(id, "..") {
		return ""
	}
	return id
}
