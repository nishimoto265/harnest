package policyartifact

import (
	"path/filepath"
	"strings"
)

const (
	ChecklistResultFile = "checklist-result.json"
	OverlayDir          = ".auto-improve"
	RepoPolicyDir       = "auto-improve"
	RepoRegistryFile    = RepoPolicyDir + "/rules-registry.jsonl"
	RepoRulesDir        = RepoPolicyDir + "/rules"
)

// Is reports whether a repository-relative path is owned by auto-improve
// policy/overlay state rather than task implementation code.
func Is(path string) bool {
	path = filepath.ToSlash(filepath.Clean(strings.TrimSpace(path)))
	path = strings.TrimPrefix(path, "./")
	switch path {
	case "", ".":
		return false
	case ChecklistResultFile, OverlayDir, RepoRegistryFile, RepoRulesDir:
		return true
	default:
		return strings.HasPrefix(path, OverlayDir+"/") ||
			strings.HasPrefix(path, RepoRulesDir+"/")
	}
}

func GitExcludePathspecs() []string {
	return []string{
		":(exclude)" + ChecklistResultFile,
		":(exclude)" + OverlayDir,
		":(exclude)" + OverlayDir + "/**",
		":(exclude)" + RepoRegistryFile,
		":(exclude)" + RepoRulesDir,
		":(exclude)" + RepoRulesDir + "/**",
	}
}
