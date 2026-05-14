package policyartifact

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsPolicyArtifactPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{path: "checklist-result.json", want: true},
		{path: ".harnest", want: true},
		{path: ".harnest/lessons/r.md", want: true},
		{path: "harnest", want: false},
		{path: "harnest/app.go", want: false},
		{path: "harnest/guidance", want: true},
		{path: "harnest/guidance/AGENTS.md.template", want: true},
		{path: "harnest/rules-registry.jsonl", want: true},
		{path: "harnest/rules", want: true},
		{path: "harnest/rules/r.md", want: true},
		{path: "AGENTS.md", want: false},
		{path: "CLAUDE.md", want: false},
		{path: ".gitignore", want: false},
		{path: ".claude/settings.json", want: false},
		{path: ".codex/hooks.json", want: false},
		{path: ".codex/config.toml", want: false},
		{path: ".codex/other.json", want: false},
		{path: "docs/frontend-rules/rule.md", want: false},
		{path: "scripts/generate-checklist.sh", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			assert.Equal(t, tt.want, Is(tt.path))
		})
	}
}

func TestIsPolicyBasePathIncludesManagedMixedFiles(t *testing.T) {
	for _, path := range []string{
		".harnest/checklist.md",
		"AGENTS.md",
		"CLAUDE.md",
		".gitignore",
		".claude/settings.json",
		".codex/hooks.json",
		".codex/config.toml",
	} {
		t.Run(path, func(t *testing.T) {
			assert.True(t, IsPolicyBasePath(path))
		})
	}
	assert.False(t, IsPolicyBasePath("docs/frontend-rules/rule.md"))
}
