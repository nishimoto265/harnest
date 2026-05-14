package lessons

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	internalio "github.com/nishimoto265/harnest/internal/io"
	"gopkg.in/yaml.v3"
)

const (
	RepoDirName             = ".harnest"
	LessonsDirName          = "lessons"
	WorkDirName             = "work"
	ChecklistFileName       = "checklist.md"
	ChecklistResultFileName = "checklist-result.md"
)

const frontMatterFence = "---"

var lessonIDPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
var checklistItemPattern = regexp.MustCompile(`^\s*-\s+\[([^\]]*)\]\s+` + "`" + `([^` + "`" + `]+)` + "`" + `(?:\s+.*)?$`)

type Status string

const (
	StatusActive     Status = "active"
	StatusDeprecated Status = "deprecated"
	StatusArchived   Status = "archived"
)

type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityHigh     Severity = "high"
	SeverityMedium   Severity = "medium"
	SeverityLow      Severity = "low"
)

type Confidence string

const (
	ConfidenceHigh   Confidence = "high"
	ConfidenceMedium Confidence = "medium"
	ConfidenceLow    Confidence = "low"
)

type Metadata struct {
	Status     Status     `yaml:"status"`
	Severity   Severity   `yaml:"severity"`
	Confidence Confidence `yaml:"confidence"`
	Category   string     `yaml:"category"`
}

type Lesson struct {
	ID            string
	Path          string
	Metadata      Metadata
	ChecklistItem string
}

type ChecklistResultSummary struct {
	Total         int `json:"total"`
	Compliant     int `json:"compliant"`
	NotApplicable int `json:"n_a"`
	Exception     int `json:"exception"`
}

type NewLessonRequest struct {
	Root          string
	ID            string
	ChecklistItem string
	Severity      Severity
	Confidence    Confidence
	Category      string
	Now           func() time.Time
}

func CreateLesson(req NewLessonRequest) (string, error) {
	root, err := cleanRoot(req.Root)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(req.ID)
	if err := ValidateID(id); err != nil {
		return "", err
	}
	checklistItem := strings.TrimSpace(req.ChecklistItem)
	if checklistItem == "" {
		return "", errors.New("lessons: checklist item is required")
	}
	severity := req.Severity
	if severity == "" {
		severity = SeverityMedium
	}
	if err := validateSeverity(severity); err != nil {
		return "", err
	}
	confidence := req.Confidence
	if confidence == "" {
		confidence = ConfidenceMedium
	}
	if err := validateConfidence(confidence); err != nil {
		return "", err
	}
	category := strings.TrimSpace(req.Category)
	if category == "" {
		category = "general"
	}
	now := req.Now
	if now == nil {
		now = time.Now
	}

	path := LessonPath(root, id)
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("lessons: lesson already exists: %s", path)
	} else if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	body := renderLessonSkeleton(id, checklistItem, severity, confidence, category, now().UTC())
	if err := internalio.WriteAtomic(path, []byte(body)); err != nil {
		return "", err
	}
	return path, nil
}

func GenerateChecklist(root string) (string, error) {
	lessons, err := Load(root)
	if err != nil {
		return "", err
	}
	return RenderChecklist(lessons), nil
}

func WriteChecklist(root string) (string, error) {
	clean, err := cleanRoot(root)
	if err != nil {
		return "", err
	}
	content, err := GenerateChecklist(clean)
	if err != nil {
		return "", err
	}
	path := ChecklistPath(clean)
	if err := internalio.WriteAtomic(path, []byte(content)); err != nil {
		return "", err
	}
	return path, nil
}

func PrepareChecklistResult(root string, force bool) (string, error) {
	clean, err := cleanRoot(root)
	if err != nil {
		return "", err
	}
	sourcePath := ChecklistPath(clean)
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("lessons: checklist is missing: %s", sourcePath)
		}
		return "", err
	}
	resultPath := ChecklistResultPath(clean)
	if !force {
		if _, err := os.Stat(resultPath); err == nil {
			return "", fmt.Errorf("lessons: checklist result already exists: %s", resultPath)
		} else if err != nil && !os.IsNotExist(err) {
			return "", err
		}
	}
	if err := internalio.WriteAtomic(resultPath, data); err != nil {
		return "", err
	}
	return resultPath, nil
}

func VerifyChecklistResult(root string) (ChecklistResultSummary, error) {
	clean, err := cleanRoot(root)
	if err != nil {
		return ChecklistResultSummary{}, err
	}
	sourceData, err := os.ReadFile(ChecklistPath(clean))
	if err != nil {
		return ChecklistResultSummary{}, err
	}
	resultData, err := os.ReadFile(ChecklistResultPath(clean))
	if err != nil {
		return ChecklistResultSummary{}, err
	}
	sourceItems, err := parseChecklistMarkdown(sourceData)
	if err != nil {
		return ChecklistResultSummary{}, fmt.Errorf("lessons: source checklist: %w", err)
	}
	resultItems, err := parseChecklistMarkdown(resultData)
	if err != nil {
		return ChecklistResultSummary{}, fmt.Errorf("lessons: checklist result: %w", err)
	}
	if err := verifyChecklistResultItems(sourceItems, resultItems); err != nil {
		return ChecklistResultSummary{}, err
	}
	return summarizeChecklistResult(resultItems), nil
}

func Load(root string) ([]Lesson, error) {
	clean, err := cleanRoot(root)
	if err != nil {
		return nil, err
	}
	lessonsDir := LessonsDir(clean)
	entries, err := os.ReadDir(lessonsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]Lesson, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".md")
		if err := ValidateID(id); err != nil {
			return nil, fmt.Errorf("lessons: invalid lesson filename %q: %w", entry.Name(), err)
		}
		path := filepath.Join(lessonsDir, entry.Name())
		lesson, err := parseLesson(path, id)
		if err != nil {
			return nil, err
		}
		out = append(out, lesson)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func ParseLessonMarkdown(path, id string, data []byte) (Lesson, error) {
	return parseLessonMarkdown(path, id, data)
}

func RenderChecklist(items []Lesson) string {
	active := make([]Lesson, 0, len(items))
	for _, item := range items {
		if item.Metadata.Status == StatusActive {
			active = append(active, item)
		}
	}
	sort.SliceStable(active, func(i, j int) bool {
		left := severityRank(active[i].Metadata.Severity)
		right := severityRank(active[j].Metadata.Severity)
		if left != right {
			return left < right
		}
		return active[i].ID < active[j].ID
	})

	var out strings.Builder
	out.WriteString("# Checklist\n\n")
	if len(active) == 0 {
		out.WriteString("No active lessons.\n")
		return out.String()
	}
	for _, item := range active {
		out.WriteString("- [ ] `")
		out.WriteString(item.ID)
		out.WriteString("` ")
		out.WriteString(singleLine(item.ChecklistItem))
		out.WriteString("\n")
	}
	return out.String()
}

func ValidateID(id string) error {
	if !lessonIDPattern.MatchString(id) {
		return fmt.Errorf("id must match %s", lessonIDPattern.String())
	}
	return nil
}

func RepoDir(root string) string {
	return filepath.Join(root, RepoDirName)
}

func LessonsDir(root string) string {
	return filepath.Join(RepoDir(root), LessonsDirName)
}

func WorkDir(root string) string {
	return filepath.Join(RepoDir(root), WorkDirName)
}

func LessonPath(root, id string) string {
	return filepath.Join(LessonsDir(root), id+".md")
}

func ChecklistPath(root string) string {
	return filepath.Join(RepoDir(root), ChecklistFileName)
}

func ChecklistResultPath(root string) string {
	return filepath.Join(WorkDir(root), ChecklistResultFileName)
}

type checklistMarkdownItem struct {
	ID        string
	Marker    string
	Line      int
	HasReason bool
}

func parseChecklistMarkdown(data []byte) ([]checklistMarkdownItem, error) {
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	items := make([]checklistMarkdownItem, 0)
	for i, line := range lines {
		match := checklistItemPattern.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		id := strings.TrimSpace(match[2])
		if err := ValidateID(id); err != nil {
			return nil, fmt.Errorf("line %d invalid checklist id %q: %w", i+1, id, err)
		}
		items = append(items, checklistMarkdownItem{
			ID:        id,
			Marker:    strings.TrimSpace(match[1]),
			Line:      i + 1,
			HasReason: hasReasonBeforeNextItem(lines[i+1:]),
		})
	}
	return items, nil
}

func verifyChecklistResultItems(sourceItems, resultItems []checklistMarkdownItem) error {
	sourceByID := make(map[string]int, len(sourceItems))
	for _, item := range sourceItems {
		if _, exists := sourceByID[item.ID]; exists {
			return fmt.Errorf("lessons: duplicate source checklist item: %s", item.ID)
		}
		sourceByID[item.ID] = item.Line
		if item.Marker != "" {
			return fmt.Errorf("lessons: source checklist item %q must use [ ]", item.ID)
		}
	}
	resultByID := make(map[string]checklistMarkdownItem, len(resultItems))
	for _, item := range resultItems {
		if _, exists := resultByID[item.ID]; exists {
			return fmt.Errorf("lessons: duplicate checklist result item: %s", item.ID)
		}
		resultByID[item.ID] = item
		if _, ok := sourceByID[item.ID]; !ok {
			return fmt.Errorf("lessons: checklist result contains item not in source checklist: %s", item.ID)
		}
		switch item.Marker {
		case "x", "-", "!":
		case "":
			return fmt.Errorf("lessons: checklist result item %q is unresolved; use [x], [-], or [!]", item.ID)
		default:
			return fmt.Errorf("lessons: checklist result item %q has invalid marker %q; use [x], [-], or [!]", item.ID, item.Marker)
		}
		if item.Marker == "!" && !item.HasReason {
			return fmt.Errorf("lessons: checklist result item %q uses [!] and requires an indented reason:", item.ID)
		}
	}
	for _, item := range sourceItems {
		if _, ok := resultByID[item.ID]; !ok {
			return fmt.Errorf("lessons: checklist result is missing item from source checklist: %s", item.ID)
		}
	}
	return nil
}

func summarizeChecklistResult(items []checklistMarkdownItem) ChecklistResultSummary {
	summary := ChecklistResultSummary{Total: len(items)}
	for _, item := range items {
		switch item.Marker {
		case "x":
			summary.Compliant++
		case "-":
			summary.NotApplicable++
		case "!":
			summary.Exception++
		}
	}
	return summary
}

func hasReasonBeforeNextItem(lines []string) bool {
	for _, line := range lines {
		if checklistItemPattern.MatchString(line) {
			return false
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "reason:") && strings.TrimSpace(strings.TrimPrefix(trimmed, "reason:")) != "" {
			return true
		}
	}
	return false
}

func parseLesson(path, id string) (Lesson, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Lesson{}, err
	}
	return parseLessonMarkdown(path, id, data)
}

func parseLessonMarkdown(path, id string, data []byte) (Lesson, error) {
	meta, body, err := splitFrontMatter(data)
	if err != nil {
		return Lesson{}, fmt.Errorf("lessons: parse %s: %w", path, err)
	}
	if err := validateMetadata(meta); err != nil {
		return Lesson{}, fmt.Errorf("lessons: %s metadata: %w", path, err)
	}
	checklistItem, err := extractSection(body, "Checklist Item")
	if err != nil {
		return Lesson{}, fmt.Errorf("lessons: %s: %w", path, err)
	}
	title := firstHeading(body)
	if title != "" && title != id {
		return Lesson{}, fmt.Errorf("lesson heading %q must match filename id %q", title, id)
	}
	return Lesson{
		ID:            id,
		Path:          path,
		Metadata:      meta,
		ChecklistItem: checklistItem,
	}, nil
}

func splitFrontMatter(data []byte) (Metadata, string, error) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.HasPrefix(text, frontMatterFence+"\n") {
		return Metadata{}, "", errors.New("missing YAML front matter")
	}
	rest := strings.TrimPrefix(text, frontMatterFence+"\n")
	idx := strings.Index(rest, "\n"+frontMatterFence+"\n")
	if idx < 0 {
		return Metadata{}, "", errors.New("unterminated YAML front matter")
	}
	rawMeta := rest[:idx]
	body := rest[idx+len("\n"+frontMatterFence+"\n"):]
	var meta Metadata
	dec := yaml.NewDecoder(strings.NewReader(rawMeta))
	dec.KnownFields(true)
	if err := dec.Decode(&meta); err != nil {
		return Metadata{}, "", err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Metadata{}, "", errors.New("front matter must contain exactly one YAML document")
		}
		return Metadata{}, "", err
	}
	return meta, body, nil
}

func validateMetadata(meta Metadata) error {
	switch meta.Status {
	case StatusActive, StatusDeprecated, StatusArchived:
	default:
		return fmt.Errorf("status must be active, deprecated, or archived")
	}
	if err := validateSeverity(meta.Severity); err != nil {
		return err
	}
	if err := validateConfidence(meta.Confidence); err != nil {
		return err
	}
	if strings.TrimSpace(meta.Category) == "" {
		return errors.New("category is required")
	}
	return nil
}

func validateSeverity(severity Severity) error {
	switch severity {
	case SeverityCritical, SeverityHigh, SeverityMedium, SeverityLow:
		return nil
	default:
		return fmt.Errorf("severity must be critical, high, medium, or low")
	}
}

func validateConfidence(confidence Confidence) error {
	switch confidence {
	case ConfidenceHigh, ConfidenceMedium, ConfidenceLow:
		return nil
	default:
		return fmt.Errorf("confidence must be high, medium, or low")
	}
}

func extractSection(body, heading string) (string, error) {
	lines := strings.Split(body, "\n")
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == "## "+heading {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return "", fmt.Errorf("missing section %q", heading)
	}
	var buf bytes.Buffer
	for _, line := range lines[start:] {
		if strings.HasPrefix(line, "## ") {
			break
		}
		buf.WriteString(line)
		buf.WriteByte('\n')
	}
	content := strings.TrimSpace(buf.String())
	if content == "" {
		return "", fmt.Errorf("section %q is empty", heading)
	}
	return content, nil
}

func firstHeading(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	return ""
}

func renderLessonSkeleton(id, checklistItem string, severity Severity, confidence Confidence, category string, now time.Time) string {
	return fmt.Sprintf(`---
status: active
severity: %s
confidence: %s
category: %s
---

# %s

## Checklist Item

%s

## Problem

TODO: Describe the failure or recurring problem this lesson prevents.

## Evidence

- created_at: %s
- TODO: Add PR, run, diff, or review evidence.

## Guidance

TODO: Add the detailed guidance to use when the checklist item is ambiguous.

## Exceptions

TODO: List legitimate exceptions, or write "None known."

## Merge Notes

TODO: Explain when a future lesson should update this file instead of creating a new one.
`, severity, confidence, fmt.Sprintf("%q", category), id, checklistItem, now.Format(time.RFC3339))
}

func singleLine(value string) string {
	fields := strings.Fields(value)
	return strings.Join(fields, " ")
}

func severityRank(severity Severity) int {
	switch severity {
	case SeverityCritical:
		return 0
	case SeverityHigh:
		return 1
	case SeverityMedium:
		return 2
	case SeverityLow:
		return 3
	default:
		return 4
	}
}

func cleanRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	return abs, nil
}
