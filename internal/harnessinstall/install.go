package harnessinstall

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	internalio "github.com/nishimoto265/auto-improve/internal/io"
)

const (
	ProviderClaude = "claude"
	ProviderCodex  = "codex"

	HookID = "auto-improve.checklist-gate"

	markdownBegin = "<!-- BEGIN AUTO-IMPROVE CHECKLIST -->"
	markdownEnd   = "<!-- END AUTO-IMPROVE CHECKLIST -->"

	commentBegin = "# BEGIN AUTO-IMPROVE CHECKLIST"
	commentEnd   = "# END AUTO-IMPROVE CHECKLIST"
)

type InstallOptions struct {
	Providers []string
	Templates Templates
}

type Templates struct {
	ClaudeGuidance    string
	CodexGuidance     string
	ProviderHooksJSON []byte
}

type PlanOptions struct {
	Check bool
}

type InstallationPlan struct {
	Root    string   `json:"root"`
	Changes []Change `json:"changes"`
	Check   bool     `json:"check"`
}

type Change struct {
	Path    string      `json:"path"`
	Action  string      `json:"action"`
	Content []byte      `json:"-"`
	Mode    os.FileMode `json:"mode,omitempty"`
}

type Result struct {
	Files []string `json:"files"`
}

func Plan(targetRoot string, install InstallOptions, opts PlanOptions) (InstallationPlan, error) {
	root, err := cleanRoot(targetRoot)
	if err != nil {
		return InstallationPlan{}, err
	}
	providers := NormalizeProviders(install.Providers)
	for _, provider := range providers {
		if provider != ProviderClaude && provider != ProviderCodex {
			return InstallationPlan{}, fmt.Errorf("harnessinstall: unsupported provider %q", provider)
		}
	}

	builder := planBuilder{root: root}
	if containsProvider(providers, ProviderClaude) {
		if err := builder.addTextMerge(filepath.Join(root, "CLAUDE.md"), guidanceTemplate(install, ProviderClaude), 0); err != nil {
			return InstallationPlan{}, err
		}
	}
	if containsProvider(providers, ProviderCodex) {
		if err := builder.addTextMerge(filepath.Join(root, "AGENTS.md"), guidanceTemplate(install, ProviderCodex), 0); err != nil {
			return InstallationPlan{}, err
		}
	}

	if err := builder.addExact(filepath.Join(root, ".auto-improve", "hooks", "verify-checklist-result.sh"), []byte(RenderHookScript()), 0o755); err != nil {
		return InstallationPlan{}, err
	}
	if containsProvider(providers, ProviderClaude) {
		if err := builder.addJSONHookMerge(filepath.Join(root, ".claude", "settings.json"), install.Templates.ProviderHooksJSON); err != nil {
			return InstallationPlan{}, err
		}
	}
	if containsProvider(providers, ProviderCodex) {
		if err := builder.addJSONHookMerge(filepath.Join(root, ".codex", "hooks.json"), install.Templates.ProviderHooksJSON); err != nil {
			return InstallationPlan{}, err
		}
		if err := builder.addCodexConfigMerge(filepath.Join(root, ".codex", "config.toml")); err != nil {
			return InstallationPlan{}, err
		}
	}
	if err := builder.addTextMerge(filepath.Join(root, ".gitignore"), RenderGitignoreBlock(), 0); err != nil {
		return InstallationPlan{}, err
	}

	sort.Slice(builder.changes, func(i, j int) bool {
		return builder.changes[i].Path < builder.changes[j].Path
	})
	return InstallationPlan{Root: root, Changes: builder.changes, Check: opts.Check}, nil
}

func Apply(plan InstallationPlan) (Result, error) {
	if plan.Check && len(plan.Changes) > 0 {
		return Result{}, fmt.Errorf("harnessinstall: install guidance is stale")
	}
	result := Result{}
	for _, change := range plan.Changes {
		if err := internalio.WriteAtomic(change.Path, change.Content); err != nil {
			return Result{}, err
		}
		if change.Mode != 0 {
			if err := os.Chmod(change.Path, change.Mode); err != nil {
				return Result{}, err
			}
		}
		result.Files = append(result.Files, change.Path)
	}
	sort.Strings(result.Files)
	return result, nil
}

func NormalizeProviders(providers []string) []string {
	if len(providers) == 0 {
		return []string{ProviderClaude, ProviderCodex}
	}
	out := make([]string, 0, len(providers))
	seen := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		for _, item := range strings.Split(provider, ",") {
			item = strings.ToLower(strings.TrimSpace(item))
			if item == "" {
				continue
			}
			if _, ok := seen[item]; ok {
				continue
			}
			seen[item] = struct{}{}
			out = append(out, item)
		}
	}
	if len(out) == 0 {
		return []string{ProviderClaude, ProviderCodex}
	}
	return out
}

func RenderGuidance(_ string) string {
	return strings.TrimSpace(fmt.Sprintf(`%s
## Auto Improve Checklist

Before implementation, read @.auto-improve/checklist.md.

At the start of work, run:

%s

Before creating a PR, update .auto-improve/work/checklist-result.md:

- [x] means compliant
- [-] means not applicable
- [!] means valid exception and requires an indented reason:

Then run:

%s
%s`, markdownBegin, `AUTO_IMPROVE_BIN="${AUTO_IMPROVE_BIN:-auto-improve}"
"$AUTO_IMPROVE_BIN" lessons prepare-checklist-result --force`, `AUTO_IMPROVE_BIN="${AUTO_IMPROVE_BIN:-auto-improve}"
"$AUTO_IMPROVE_BIN" lessons verify-checklist-result`, markdownEnd)) + "\n"
}

func guidanceTemplate(install InstallOptions, provider string) string {
	switch provider {
	case ProviderClaude:
		if strings.TrimSpace(install.Templates.ClaudeGuidance) != "" {
			return ensureTrailingNewline(strings.TrimSpace(install.Templates.ClaudeGuidance))
		}
	case ProviderCodex:
		if strings.TrimSpace(install.Templates.CodexGuidance) != "" {
			return ensureTrailingNewline(strings.TrimSpace(install.Templates.CodexGuidance))
		}
	}
	return RenderGuidance(provider)
}

func RenderHookScript() string {
	return `#!/bin/sh
set -eu

ROOT="${AUTO_IMPROVE_REPO_ROOT:-}"
if [ -z "$ROOT" ]; then
  ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
fi

AUTO_IMPROVE_BIN="${AUTO_IMPROVE_BIN:-auto-improve}"

if ! "$AUTO_IMPROVE_BIN" lessons verify-checklist-result --root "$ROOT" >/dev/null; then
  cat >&2 <<'EOF'
auto-improve checklist is incomplete.
Run:
  AUTO_IMPROVE_BIN="${AUTO_IMPROVE_BIN:-auto-improve}"
  "$AUTO_IMPROVE_BIN" lessons prepare-checklist-result --force
Then mark every item in .auto-improve/work/checklist-result.md:
  [x] compliant
  [-] not applicable
  [!] valid exception with an indented reason:
Then run:
  "$AUTO_IMPROVE_BIN" lessons verify-checklist-result
EOF
  exit 2
fi
`
}

func RenderProviderHooksJSON() string {
	return string(RenderProviderHooksJSONWithStopHook(nil))
}

func RenderGitignoreBlock() string {
	return strings.TrimSpace(fmt.Sprintf(`%s
.auto-improve/work/
%s`, commentBegin, commentEnd)) + "\n"
}

type planBuilder struct {
	root    string
	changes []Change
}

func (b *planBuilder) addTextMerge(path, block string, mode os.FileMode) error {
	existing, err := readOptional(path)
	if err != nil {
		return err
	}
	merged, err := upsertTextBlock(string(existing), blockBegin(block), blockEnd(block), block)
	if err != nil {
		return fmt.Errorf("harnessinstall: managed block in %s is malformed", path)
	}
	b.addIfChanged(path, existing, []byte(merged), mode)
	return nil
}

func (b *planBuilder) addExact(path string, content []byte, mode os.FileMode) error {
	existing, err := readOptional(path)
	if err != nil {
		return err
	}
	b.addIfChanged(path, existing, content, mode)
	return nil
}

func (b *planBuilder) addJSONHookMerge(path string, template []byte) error {
	existing, err := readOptional(path)
	if err != nil {
		return err
	}
	merged, err := MergeProviderHooksJSONWithTemplate(existing, template)
	if err != nil {
		return fmt.Errorf("harnessinstall: parse provider hook config %s: %w", path, err)
	}
	b.addIfChanged(path, existing, merged, 0)
	return nil
}

func (b *planBuilder) addCodexConfigMerge(path string) error {
	existing, err := readOptional(path)
	if err != nil {
		return err
	}
	merged := []byte(mergeCodexHooksFeature(string(existing)))
	b.addIfChanged(path, existing, merged, 0)
	return nil
}

func (b *planBuilder) addIfChanged(path string, existing, next []byte, mode os.FileMode) {
	if bytes.Equal(existing, next) && !modeNeedsUpdate(path, mode) {
		return
	}
	action := "update"
	if existing == nil {
		action = "create"
	}
	b.changes = append(b.changes, Change{Path: path, Action: action, Content: next, Mode: mode})
}

func modeNeedsUpdate(path string, mode os.FileMode) bool {
	if mode == 0 {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Mode().Perm() != mode
}

func cleanRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	return filepath.Abs(root)
}

func containsProvider(providers []string, provider string) bool {
	for _, item := range providers {
		if item == provider {
			return true
		}
	}
	return false
}

func readOptional(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return data, nil
}
