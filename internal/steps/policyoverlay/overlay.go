package policyoverlay

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nishimoto265/auto-improve/internal/candidaterules"
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
	return internalio.WriteAtomic(filepath.Join(overlayDir, "checklist.md"), []byte(renderChecklist(items)))
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
