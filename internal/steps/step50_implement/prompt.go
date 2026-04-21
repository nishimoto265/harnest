package step50_implement

import (
	"embed"
	"fmt"
	"strings"
	"text/template"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
)

//go:embed step50-implement-pass2.tmpl
var step50TemplateFS embed.FS

const step50TemplateName = "step50-implement-pass2.tmpl"

// PromptData is the render input for prompts/step50-implement-pass2.tmpl.
type PromptData struct {
	TaskPackage  contracts.TaskPackage
	Agent        contracts.AgentID
	RulePayloads []RulePayload
	WorktreePath string
	Pass         int
}

// RenderPrompt renders the step50 pass2 prompt template with sanitized text.
func RenderPrompt(data PromptData) (string, error) {
	templateBytes, err := step50TemplateFS.ReadFile(step50TemplateName)
	if err != nil {
		return "", fmt.Errorf("read step50 template: %w", err)
	}

	tmpl, err := template.New(step50TemplateName).Option("missingkey=error").Parse(string(templateBytes))
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
			ID:           internalio.SanitizeForPromptEmbedding(rule.ID),
			Kind:         rule.Kind,
			TargetRuleID: internalio.SanitizeForPromptEmbedding(rule.TargetRuleID),
			Title:        internalio.SanitizeForPromptEmbedding(rule.Title),
			ProposedBody: internalio.SanitizeForPromptEmbedding(rule.ProposedBody),
		}
	}
	return safe
}
