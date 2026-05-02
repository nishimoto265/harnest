package policyartifact

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	ChecklistResultFile = "checklist-result.json"
	OverlayDir          = ".auto-improve"
	AgentGuidanceFile   = "AGENTS.md"
	ClaudeGuidanceFile  = "CLAUDE.md"
	GitignoreFile       = ".gitignore"
	ClaudeSettingsFile  = ".claude/settings.json"
	CodexHooksFile      = ".codex/hooks.json"
	CodexConfigFile     = ".codex/config.toml"
	RepoPolicyDir       = "auto-improve"
	RepoGuidanceDir     = RepoPolicyDir + "/guidance"
	RepoRegistryFile    = RepoPolicyDir + "/rules-registry.jsonl"
	RepoRulesDir        = RepoPolicyDir + "/rules"
)

// Is reports whether a repository-relative path is fully owned by
// auto-improve policy/overlay state and must be excluded from task
// implementation output.
//
// Mixed user files such as AGENTS.md, CLAUDE.md, .gitignore, and provider
// config files are intentionally not included here. auto-improve may install
// managed blocks or hook entries in those files when preparing the policy base,
// but implementation changes outside that managed state must remain visible in
// agent diffs.
func Is(path string) bool {
	path = filepath.ToSlash(filepath.Clean(strings.TrimSpace(path)))
	path = strings.TrimPrefix(path, "./")
	switch path {
	case "", ".":
		return false
	case ChecklistResultFile,
		OverlayDir,
		RepoGuidanceDir,
		RepoRegistryFile,
		RepoRulesDir:
		return true
	default:
		return strings.HasPrefix(path, OverlayDir+"/") ||
			strings.HasPrefix(path, RepoGuidanceDir+"/") ||
			strings.HasPrefix(path, RepoRulesDir+"/")
	}
}

// IsPolicyBasePath reports whether a repository-relative path may be changed
// while preparing the auto-improve policy base. This is broader than Is because
// it includes mixed user files that receive managed blocks or hook entries.
func IsPolicyBasePath(path string) bool {
	path = filepath.ToSlash(filepath.Clean(strings.TrimSpace(path)))
	path = strings.TrimPrefix(path, "./")
	if Is(path) {
		return true
	}
	switch path {
	case AgentGuidanceFile,
		ClaudeGuidanceFile,
		GitignoreFile,
		ClaudeSettingsFile,
		CodexHooksFile,
		CodexConfigFile:
		return true
	default:
		return false
	}
}

func GitPolicyBasePathspecs() []string {
	return []string{
		OverlayDir,
		AgentGuidanceFile,
		ClaudeGuidanceFile,
		GitignoreFile,
		ClaudeSettingsFile,
		CodexHooksFile,
		CodexConfigFile,
	}
}

func ExistingPolicyBasePathspecs(root string) []string {
	out := make([]string, 0, len(GitPolicyBasePathspecs()))
	for _, rel := range GitPolicyBasePathspecs() {
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel))); err == nil {
			out = append(out, rel)
		}
	}
	return out
}

func GitResetPathspecs() []string {
	return []string{
		ChecklistResultFile,
		OverlayDir,
		RepoGuidanceDir,
		RepoRegistryFile,
		RepoRulesDir,
	}
}

func GitExcludePathspecs() []string {
	base := GitResetPathspecs()
	out := make([]string, 0, len(base)+3)
	for _, path := range base {
		out = append(out, ":(exclude)"+path)
		if path == OverlayDir || path == RepoGuidanceDir || path == RepoRulesDir {
			out = append(out, ":(exclude)"+path+"/**")
		}
	}
	return out
}
