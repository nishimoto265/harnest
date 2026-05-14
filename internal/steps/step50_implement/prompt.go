package step50_implement

import (
	"embed"
	"fmt"
	"strings"
	"text/template"

	"github.com/nishimoto265/harnest/internal/candidaterules"
	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/nishimoto265/harnest/internal/policyrepo"
)

//go:embed prompts/step50-implement-pass2.tmpl
var step50PromptFS embed.FS

const step50TemplateFile = "prompts/step50-implement-pass2.tmpl"

// PromptData is the render input for prompts/step50-implement-pass2.tmpl.
type PromptData struct {
	TaskPackage      contracts.TaskPackage
	Agent            contracts.AgentID
	CandidateRuleIDs []string
	RulePayloads     []candidaterules.RulePayload
	ActiveRules      []policyrepo.ActiveRule
	WorktreePath     string
	Pass             int
}

// RenderPrompt renders the step50 pass2 prompt template with sanitized text.
func RenderPrompt(data PromptData) (string, error) {
	tmpl, err := template.New("step50-implement-pass2.tmpl").Option("missingkey=error").ParseFS(step50PromptFS, step50TemplateFile)
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
	safe.ActiveRules = sanitizeActiveRules(data.ActiveRules)
	return safe
}

func sanitizeTaskPackage(pkg contracts.TaskPackage) contracts.TaskPackage {
	safe := pkg
	safe.Title = internalio.SanitizeForPromptEmbedding(pkg.Title)
	safe.BestBranch = internalio.SanitizeForPromptEmbedding(pkg.BestBranch)
	safe.ReconstructedTaskPrompt = internalio.SanitizeForPromptEmbedding(pkg.ReconstructedTaskPrompt, internalio.SafeTextOptions{
		Label: "task_brief",
		Fence: true,
	})

	safe.Worktrees = make([]contracts.WorktreeAllocation, len(pkg.Worktrees))
	for i, worktree := range pkg.Worktrees {
		safe.Worktrees[i] = worktree
		safe.Worktrees[i].Path = internalio.SanitizeForPromptEmbedding(worktree.Path)
		safe.Worktrees[i].Branch = internalio.SanitizeForPromptEmbedding(worktree.Branch)
	}
	return safe
}

func sanitizeRulePayloads(rulePayloads []candidaterules.RulePayload) []candidaterules.RulePayload {
	if len(rulePayloads) == 0 {
		return nil
	}
	safe := make([]candidaterules.RulePayload, len(rulePayloads))
	for i, rule := range rulePayloads {
		safe[i] = candidaterules.RulePayload{
			ID:           internalio.SanitizeForPromptEmbedding(rule.ID),
			Kind:         internalio.SanitizeForPromptEmbedding(rule.Kind),
			TargetRuleID: internalio.SanitizeForPromptEmbedding(rule.TargetRuleID),
			Title:        internalio.SanitizeForPromptEmbedding(rule.Title),
			ProposedBody: internalio.SanitizeForPromptEmbedding(rule.ProposedBody, internalio.SafeTextOptions{
				Label: "experiment_lesson",
				Fence: true,
			}),
		}
	}
	return safe
}

func rulePayloadIDs(payloads []candidaterules.RulePayload) []string {
	if len(payloads) == 0 {
		return nil
	}
	ids := make([]string, 0, len(payloads))
	for _, payload := range payloads {
		ids = append(ids, payload.ID)
	}
	return ids
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
