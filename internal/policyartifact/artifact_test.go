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
		{path: ".auto-improve", want: true},
		{path: ".auto-improve/lessons/r.md", want: true},
		{path: "auto-improve", want: false},
		{path: "auto-improve/app.go", want: false},
		{path: "auto-improve/guidance", want: true},
		{path: "auto-improve/guidance/AGENTS.md.template", want: true},
		{path: "auto-improve/rules-registry.jsonl", want: true},
		{path: "auto-improve/rules", want: true},
		{path: "auto-improve/rules/r.md", want: true},
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
		".auto-improve/checklist.md",
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
