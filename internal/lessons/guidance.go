package lessons

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	internalio "github.com/nishimoto265/auto-improve/internal/io"
)

const (
	managedBegin = "<!-- BEGIN AUTO-IMPROVE CHECKLIST -->"
	managedEnd   = "<!-- END AUTO-IMPROVE CHECKLIST -->"

	commentManagedBegin = "# BEGIN AUTO-IMPROVE CHECKLIST"
	commentManagedEnd   = "# END AUTO-IMPROVE CHECKLIST"
)

type InstallGuidanceOptions struct {
	Root      string
	Providers []string
}

type InstallGuidanceResult struct {
	Files []string `json:"files"`
}

func InstallGuidance(opts InstallGuidanceOptions) (InstallGuidanceResult, error) {
	root, err := cleanRoot(opts.Root)
	if err != nil {
		return InstallGuidanceResult{}, err
	}
	providers := normalizeProviders(opts.Providers)
	for _, provider := range providers {
		if provider != "claude" && provider != "codex" {
			return InstallGuidanceResult{}, fmt.Errorf("lessons: unsupported provider %q", provider)
		}
	}
	result := InstallGuidanceResult{}
	addFile := func(path string) {
		result.Files = append(result.Files, path)
	}

	if containsProvider(providers, "claude") {
		path, err := upsertMarkdownGuidance(root, "CLAUDE.md")
		if err != nil {
			return InstallGuidanceResult{}, err
		}
		addFile(path)
	}
	if containsProvider(providers, "codex") {
		path, err := upsertMarkdownGuidance(root, "AGENTS.md")
		if err != nil {
			return InstallGuidanceResult{}, err
		}
		addFile(path)
	}

	hookPath, err := writeChecklistHookScript(root)
	if err != nil {
		return InstallGuidanceResult{}, err
	}
	addFile(hookPath)

	if containsProvider(providers, "claude") {
		path, err := upsertProviderHookJSON(filepath.Join(root, ".claude", "settings.json"))
		if err != nil {
			return InstallGuidanceResult{}, err
		}
		addFile(path)
	}
	if containsProvider(providers, "codex") {
		hooksPath, err := upsertProviderHookJSON(filepath.Join(root, ".codex", "hooks.json"))
		if err != nil {
			return InstallGuidanceResult{}, err
		}
		addFile(hooksPath)
		configPath, err := upsertCodexHooksFeature(filepath.Join(root, ".codex", "config.toml"))
		if err != nil {
			return InstallGuidanceResult{}, err
		}
		addFile(configPath)
	}

	gitignorePath, err := upsertGitignore(root)
	if err != nil {
		return InstallGuidanceResult{}, err
	}
	addFile(gitignorePath)

	sort.Strings(result.Files)
	return result, nil
}

func normalizeProviders(providers []string) []string {
	if len(providers) == 0 {
		return []string{"claude", "codex"}
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
		return []string{"claude", "codex"}
	}
	return out
}

func containsProvider(providers []string, provider string) bool {
	for _, item := range providers {
		if item == provider {
			return true
		}
	}
	return false
}

func upsertMarkdownGuidance(root, name string) (string, error) {
	path := filepath.Join(root, name)
	block := strings.TrimSpace(fmt.Sprintf(`%s
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
%s`, managedBegin, "auto-improve lessons prepare-checklist-result --force", "auto-improve lessons verify-checklist-result", managedEnd)) + "\n"
	if err := upsertTextBlock(path, managedBegin, managedEnd, block); err != nil {
		return "", err
	}
	return path, nil
}

func writeChecklistHookScript(root string) (string, error) {
	path := filepath.Join(root, RepoDirName, "hooks", "verify-checklist-result.sh")
	body := `#!/bin/sh
set -eu

ROOT="${AUTO_IMPROVE_REPO_ROOT:-}"
if [ -z "$ROOT" ]; then
  ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
fi

AUTO_IMPROVE_BIN="${AUTO_IMPROVE_BIN:-auto-improve}"

if ! "$AUTO_IMPROVE_BIN" lessons verify-checklist-result --root "$ROOT" >/dev/null; then
  cat >&2 <<'EOF'
auto-improve checklist is incomplete.
Run: auto-improve lessons prepare-checklist-result --force
Then mark every item in .auto-improve/work/checklist-result.md:
  [x] compliant
  [-] not applicable
  [!] valid exception with an indented reason:
Then run: auto-improve lessons verify-checklist-result
EOF
  exit 2
fi
`
	if err := internalio.WriteAtomic(path, []byte(body)); err != nil {
		return "", err
	}
	if err := os.Chmod(path, 0o755); err != nil {
		return "", err
	}
	return path, nil
}

func upsertProviderHookJSON(path string) (string, error) {
	var config map[string]any
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", err
		}
		config = map[string]any{}
	} else if len(strings.TrimSpace(string(data))) == 0 {
		config = map[string]any{}
	} else if err := json.Unmarshal(data, &config); err != nil {
		return "", fmt.Errorf("lessons: parse provider hook config %s: %w", path, err)
	}
	hooksValue, ok := config["hooks"]
	if !ok {
		hooksValue = map[string]any{}
		config["hooks"] = hooksValue
	}
	hooks, ok := hooksValue.(map[string]any)
	if !ok {
		return "", fmt.Errorf("lessons: provider hook config %s has non-object hooks", path)
	}
	stopHooks, _ := hooks["Stop"].([]any)
	if !hasAutoImproveStopHook(stopHooks) {
		stopHooks = append(stopHooks, map[string]any{
			"hooks": []any{
				map[string]any{
					"type":    "command",
					"command": "sh .auto-improve/hooks/verify-checklist-result.sh",
					"timeout": float64(30),
				},
			},
		})
	}
	hooks["Stop"] = stopHooks
	out, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return "", err
	}
	out = append(out, '\n')
	if err := internalio.WriteAtomic(path, out); err != nil {
		return "", err
	}
	return path, nil
}

func hasAutoImproveStopHook(items []any) bool {
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		hookItems, ok := m["hooks"].([]any)
		if !ok {
			continue
		}
		for _, hook := range hookItems {
			hookMap, ok := hook.(map[string]any)
			if !ok {
				continue
			}
			command, _ := hookMap["command"].(string)
			if strings.Contains(command, ".auto-improve/hooks/verify-checklist-result.sh") {
				return true
			}
		}
	}
	return false
}

func upsertCodexHooksFeature(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", err
		}
		body := "# BEGIN AUTO-IMPROVE CODEX HOOKS\n[features]\ncodex_hooks = true\n# END AUTO-IMPROVE CODEX HOOKS\n"
		return path, internalio.WriteAtomic(path, []byte(body))
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if strings.Contains(text, "codex_hooks") {
		return path, nil
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == "[features]" {
			lines = append(lines[:i+1], append([]string{"codex_hooks = true"}, lines[i+1:]...)...)
			out := strings.Join(lines, "\n")
			if !strings.HasSuffix(out, "\n") {
				out += "\n"
			}
			return path, internalio.WriteAtomic(path, []byte(out))
		}
	}
	block := "\n# BEGIN AUTO-IMPROVE CODEX HOOKS\n[features]\ncodex_hooks = true\n# END AUTO-IMPROVE CODEX HOOKS\n"
	if strings.TrimSpace(text) == "" {
		block = strings.TrimPrefix(block, "\n")
	}
	return path, internalio.WriteAtomic(path, []byte(strings.TrimRight(text, "\n")+block))
}

func upsertGitignore(root string) (string, error) {
	path := filepath.Join(root, ".gitignore")
	block := strings.TrimSpace(fmt.Sprintf(`%s
.auto-improve/work/
%s`, commentManagedBegin, commentManagedEnd)) + "\n"
	return path, upsertTextBlock(path, commentManagedBegin, commentManagedEnd, block)
}

func upsertTextBlock(path, begin, end, block string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		return internalio.WriteAtomic(path, []byte(block))
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	beginIndex := strings.Index(text, begin)
	endIndex := strings.Index(text, end)
	switch {
	case beginIndex >= 0 && endIndex >= beginIndex:
		endIndex += len(end)
		next := text[endIndex:]
		if strings.HasPrefix(next, "\n") {
			next = strings.TrimPrefix(next, "\n")
		}
		text = strings.TrimRight(text[:beginIndex], "\n") + "\n\n" + strings.TrimRight(block, "\n") + "\n"
		if strings.TrimSpace(next) != "" {
			text += "\n" + next
		}
	case beginIndex >= 0 || endIndex >= 0:
		return fmt.Errorf("lessons: managed block in %s is malformed", path)
	default:
		if strings.TrimSpace(text) == "" {
			text = block
		} else {
			text = strings.TrimRight(text, "\n") + "\n\n" + block
		}
	}
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return internalio.WriteAtomic(path, []byte(text))
}
