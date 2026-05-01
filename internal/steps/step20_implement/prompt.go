package step20_implement

import (
	"embed"
	"strings"
	"text/template"

	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/policyrepo"
	"github.com/nishimoto265/auto-improve/internal/prompt"
)

//go:embed prompts/step20-implement.tmpl
var step20PromptFS embed.FS

type promptData struct {
	TaskPackage *contracts.TaskPackage
	Agent       contracts.AgentID
	OutputDir   string
	TaskPrompt  string
	ActiveRules []policyrepo.ActiveRule
}

func renderPrompt(cfg *config.Config, data promptData) (string, error) {
	tmpl, err := template.New(string(prompt.TemplateStep20Implement)).Option("missingkey=error").ParseFS(step20PromptFS, "prompts/step20-implement.tmpl")
	if err != nil {
		return "", err
	}
	var out strings.Builder
	if err := tmpl.Execute(&out, sanitizePromptData(data)); err != nil {
		return "", err
	}
	return out.String(), nil
}

func sanitizePromptData(data promptData) promptData {
	safe := data
	if data.TaskPackage != nil {
		pkg := *data.TaskPackage
		pkg.Title = internalio.SanitizeForPromptEmbedding(pkg.Title)
		pkg.BestBranch = internalio.SanitizeForPromptEmbedding(pkg.BestBranch)
		pkg.ReconstructedTaskPrompt = internalio.SanitizeForPromptEmbedding(pkg.ReconstructedTaskPrompt, internalio.SafeTextOptions{
			Label: "task_brief",
			Fence: true,
		})
		pkg.Worktrees = make([]contracts.WorktreeAllocation, len(data.TaskPackage.Worktrees))
		for i, worktree := range data.TaskPackage.Worktrees {
			pkg.Worktrees[i] = worktree
			pkg.Worktrees[i].Path = internalio.SanitizeForPromptEmbedding(worktree.Path)
			pkg.Worktrees[i].Branch = internalio.SanitizeForPromptEmbedding(worktree.Branch)
		}
		safe.TaskPackage = &pkg
	}
	safe.OutputDir = internalio.SanitizeForPromptEmbedding(data.OutputDir)
	safe.TaskPrompt = internalio.SanitizeForPromptEmbedding(data.TaskPrompt, internalio.SafeTextOptions{
		Label: "task_brief",
		Fence: true,
	})
	safe.ActiveRules = sanitizeActiveRules(data.ActiveRules)
	return safe
}

func sanitizeActiveRules(rules []policyrepo.ActiveRule) []policyrepo.ActiveRule {
	if len(rules) == 0 {
		return nil
	}
	safe := make([]policyrepo.ActiveRule, len(rules))
	for i, rule := range rules {
		safe[i] = policyrepo.ActiveRule{
			RuleID:   internalio.SanitizeForPromptEmbedding(rule.RuleID),
			RulePath: internalio.SanitizeForPromptEmbedding(rule.RulePath),
			Body: internalio.SanitizeForPromptEmbedding(rule.Body, internalio.SafeTextOptions{
				Label: "active_rule",
				Fence: true,
			}),
		}
	}
	return safe
}
