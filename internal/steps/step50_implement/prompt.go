package step50_implement

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"text/template"

	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/prompt"
)

type promptData struct {
	TaskPackage      contracts.TaskPackage
	Agent            contracts.AgentID
	CandidateRuleIDs []string
	RulePayloads     []RulePayload
	WorktreePath     string
	Pass             int
}

func renderPrompt(cfg *config.Config, data promptData) (string, error) {
	if cfg == nil {
		return "", fmt.Errorf("render prompt: config is required")
	}

	repoRoot, err := cfg.RepoRoot()
	if err != nil {
		return "", fmt.Errorf("resolve repo root: %w", err)
	}
	templatePath := filepath.Join(repoRoot, prompt.TemplateStep50Implement.RelativePath())
	templateBytes, err := os.ReadFile(templatePath)
	if err != nil {
		return "", fmt.Errorf("read prompt template: %w", err)
	}

	tmpl, err := template.New(filepath.Base(templatePath)).Option("missingkey=error").Parse(string(templateBytes))
	if err != nil {
		return "", fmt.Errorf("parse prompt template: %w", err)
	}

	sanitized := sanitizePromptData(data)
	var rendered bytes.Buffer
	if err := tmpl.Execute(&rendered, sanitized); err != nil {
		return "", fmt.Errorf("execute prompt template: %w", err)
	}
	return rendered.String(), nil
}

func sanitizePromptData(data promptData) promptData {
	data.TaskPackage = sanitizeTaskPackageForPrompt(data.TaskPackage)
	data.WorktreePath = internalio.SanitizeForPromptEmbedding(data.WorktreePath)

	if len(data.CandidateRuleIDs) > 0 {
		ids := make([]string, len(data.CandidateRuleIDs))
		for i, id := range data.CandidateRuleIDs {
			ids[i] = internalio.SanitizeForPromptEmbedding(id)
		}
		data.CandidateRuleIDs = ids
	}

	if len(data.RulePayloads) > 0 {
		payloads := make([]RulePayload, len(data.RulePayloads))
		for i, payload := range data.RulePayloads {
			payloads[i] = RulePayload{
				ID:   internalio.SanitizeForPromptEmbedding(payload.ID),
				Text: internalio.SanitizeForPromptEmbedding(payload.Text),
			}
		}
		data.RulePayloads = payloads
	}

	return data
}

func sanitizeTaskPackageForPrompt(pkg contracts.TaskPackage) contracts.TaskPackage {
	sanitized := pkg
	sanitized.Title = internalio.SanitizeForPromptEmbedding(pkg.Title)
	sanitized.BestBranch = internalio.SanitizeForPromptEmbedding(pkg.BestBranch)
	sanitized.ReconstructedTaskPrompt = internalio.SanitizeForPromptEmbedding(pkg.ReconstructedTaskPrompt)

	if len(pkg.Worktrees) > 0 {
		worktrees := make([]contracts.WorktreeAllocation, len(pkg.Worktrees))
		copy(worktrees, pkg.Worktrees)
		for i := range worktrees {
			worktrees[i].Path = internalio.SanitizeForPromptEmbedding(worktrees[i].Path)
			worktrees[i].Branch = internalio.SanitizeForPromptEmbedding(worktrees[i].Branch)
		}
		sanitized.Worktrees = worktrees
	}

	return sanitized
}
