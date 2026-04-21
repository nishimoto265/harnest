package step50_implement

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/prompt"
)

// PromptData is the render input for prompts/step50-implement-pass2.tmpl.
type PromptData struct {
	TaskPackage      contracts.TaskPackage
	Agent            contracts.AgentID
	CandidateRuleIDs []string
	RulePayloads     []RulePayload
	WorktreePath     string
	Pass             int
}

// RenderPrompt renders the step50 pass2 prompt template with sanitized text.
func RenderPrompt(data PromptData) (string, error) {
	tmplPath, err := step50TemplatePath()
	if err != nil {
		return "", err
	}

	templateBytes, err := os.ReadFile(tmplPath)
	if err != nil {
		return "", fmt.Errorf("read step50 template: %w", err)
	}

	tmpl, err := template.New(filepath.Base(tmplPath)).Option("missingkey=error").Parse(string(templateBytes))
	if err != nil {
		return "", fmt.Errorf("parse step50 template: %w", err)
	}

	var rendered strings.Builder
	if err := tmpl.Execute(&rendered, sanitizePromptData(data)); err != nil {
		return "", fmt.Errorf("execute step50 template: %w", err)
	}
	return rendered.String(), nil
}

func sanitizePromptData(data PromptData) PromptData {
	safe := data
	safe.TaskPackage = sanitizeTaskPackage(data.TaskPackage)
	safe.WorktreePath = internalio.SanitizeForPromptEmbedding(data.WorktreePath)
	safe.CandidateRuleIDs = sanitizeStrings(data.CandidateRuleIDs)
	safe.RulePayloads = sanitizeRulePayloads(data.RulePayloads)
	return safe
}

func sanitizeTaskPackage(pkg contracts.TaskPackage) contracts.TaskPackage {
	safe := pkg
	safe.Title = internalio.SanitizeForPromptEmbedding(pkg.Title)
	safe.BestBranch = internalio.SanitizeForPromptEmbedding(pkg.BestBranch)
	safe.ReconstructedTaskPrompt = internalio.SanitizeForPromptEmbedding(pkg.ReconstructedTaskPrompt)

	safe.Worktrees = make([]contracts.WorktreeAllocation, len(pkg.Worktrees))
	for i, worktree := range pkg.Worktrees {
		safe.Worktrees[i] = worktree
		safe.Worktrees[i].Path = internalio.SanitizeForPromptEmbedding(worktree.Path)
		safe.Worktrees[i].Branch = internalio.SanitizeForPromptEmbedding(worktree.Branch)
	}
	return safe
}

func sanitizeRulePayloads(rulePayloads []RulePayload) []RulePayload {
	if len(rulePayloads) == 0 {
		return nil
	}
	safe := make([]RulePayload, len(rulePayloads))
	for i, rule := range rulePayloads {
		safe[i] = RulePayload{
			ID:   internalio.SanitizeForPromptEmbedding(rule.ID),
			Text: internalio.SanitizeForPromptEmbedding(rule.Text),
		}
	}
	return safe
}

func sanitizeStrings(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	safe := make([]string, len(items))
	for i, item := range items {
		safe[i] = internalio.SanitizeForPromptEmbedding(item)
	}
	return safe
}

func step50TemplatePath() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("resolve caller for step50 template")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
	templatePath := filepath.Join(repoRoot, prompt.TemplateStep50Implement.RelativePath())
	if err := contracts.EnsureCleanAbsolutePath(templatePath); err != nil {
		return "", err
	}
	return templatePath, nil
}
