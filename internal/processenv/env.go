package processenv

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// defaultTrustedPATH is the fixed PATH used for every sanitized subprocess env.
// Callers MUST NOT inherit the caller's $PATH since shell-init payloads can
// inject attacker-controlled binaries earlier on the path.
const defaultTrustedPATH = "/usr/bin:/bin:/usr/sbin:/sbin:/usr/local/bin:/opt/homebrew/bin"

var trustedPathState = struct {
	sync.RWMutex
	value string
}{value: defaultTrustedPATH}

func TrustedPath() string {
	trustedPathState.RLock()
	defer trustedPathState.RUnlock()
	return trustedPathState.value
}

func SetTrustedPathForTest(path string) func() {
	trustedPathState.Lock()
	previous := trustedPathState.value
	trustedPathState.value = path
	trustedPathState.Unlock()
	return func() {
		trustedPathState.Lock()
		trustedPathState.value = previous
		trustedPathState.Unlock()
	}
}

// TrustedLookPath resolves bare command names against trustedPATH instead of
// the parent process PATH. Absolute paths are allowed for explicit operator
// configuration; relative paths with separators are rejected so they cannot
// depend on the caller's current working directory.
func TrustedLookPath(file string) (string, error) {
	if file == "" {
		return "", fmt.Errorf("processenv: executable name is required")
	}
	if filepath.IsAbs(file) {
		if err := executableFile(file); err != nil {
			return "", fmt.Errorf("processenv: executable %q is not usable: %w", file, err)
		}
		return file, nil
	}
	if strings.ContainsRune(file, os.PathSeparator) {
		return "", fmt.Errorf("processenv: relative executable path %q is not allowed; use an absolute path or a bare name in trusted PATH", file)
	}
	trustedPath := TrustedPath()
	for _, dir := range filepath.SplitList(trustedPath) {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, file)
		if err := executableFile(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("processenv: executable %q not found in trusted PATH %q: %w", file, trustedPath, exec.ErrNotFound)
}

// TrustedCommand returns an exec.Cmd whose path has already been resolved by
// TrustedLookPath, avoiding exec.Command's implicit parent-PATH lookup.
func TrustedCommand(name string, args ...string) (*exec.Cmd, error) {
	resolved, err := TrustedLookPath(name)
	if err != nil {
		return nil, err
	}
	return exec.Command(resolved, args...), nil
}

// TrustedCommandContext is the context-aware variant of TrustedCommand.
func TrustedCommandContext(ctx context.Context, name string, args ...string) (*exec.Cmd, error) {
	resolved, err := TrustedLookPath(name)
	if err != nil {
		return nil, err
	}
	return exec.CommandContext(ctx, resolved, args...), nil
}

func executableFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("is a directory")
	}
	if info.Mode()&0o111 == 0 {
		return fmt.Errorf("permission denied")
	}
	return nil
}

// Sanitize returns a strict allowlist env for purely local subprocess (e.g. the
// `claude` agent binary and git operations scoped to a carved worktree).
//
// This is the most restrictive profile: it drops $PATH, SSH agent credentials,
// gh auth tokens, and any git-plumbing override. Extra k=v entries supplied by
// the caller are still filtered through the same allowlist; they can override a
// matching baseline key but cannot add arbitrary variables.
//
// Use SanitizeForNetworkExec when invoking a binary that needs to hit the
// network (gh, git push/fetch against origin). Those callers must cross a
// trust boundary and require auth variables the local profile strips.
//
// Sanitize is retained as a synonym for SanitizeForLocalExec to preserve
// existing callers (step20/50 agent runner, local git plumbing).
func Sanitize(extra ...string) []string {
	return SanitizeForLocalExec(extra...)
}

// SanitizeForLocalExec is the strict allowlist profile; see Sanitize.
func SanitizeForLocalExec(extra ...string) []string {
	return sanitize(extra, localAllowlist)
}

// SanitizeForNetworkExec allows the additional auth variables required by
// network-crossing subprocesses:
//   - SSH_AUTH_SOCK: ssh-agent auth for git+ssh clones/pushes
//   - GH_TOKEN / GITHUB_TOKEN: gh CLI PAT auth
//   - GH_HOST: gh CLI enterprise host override
//
// Shell-init-injection vectors (BASH_ENV, LD_PRELOAD, GIT_CONFIG_* overrides,
// etc.) are still blocked. Callers that only need local filesystem + git
// plumbing on a pre-carved worktree must keep using SanitizeForLocalExec.
func SanitizeForNetworkExec(extra ...string) []string {
	return sanitize(extra, networkAllowlist)
}

// SanitizeForAgentExec is the local subprocess profile plus an explicit Git
// safety profile. Agents may invoke git themselves, so do not let them inherit
// operator global/system git config or git extension-point env.
func SanitizeForAgentExec(extra ...string) []string {
	return appendSafeGitProfile(SanitizeForLocalExec(extra...))
}

// GitLocalEnv returns the hardened env for local harness git plumbing.
func GitLocalEnv(extra ...string) []string {
	return appendSafeGitProfile(SanitizeForLocalExec(extra...))
}

// GitNetworkEnv returns the hardened env for network-crossing harness git.
// Network auth env allowed by SanitizeForNetworkExec is preserved.
func GitNetworkEnv(extra ...string) []string {
	return appendSafeGitProfile(SanitizeForNetworkExec(extra...))
}

func sanitize(extra []string, allow func(string) bool) []string {
	env := os.Environ()
	out := make([]string, 0, len(env)+len(extra))
	indexByKey := make(map[string]int, len(env)+len(extra))
	appendAllowed := func(item string, replace bool) {
		key, _, ok := strings.Cut(item, "=")
		if !ok || key == "PATH" || !allow(key) {
			return
		}
		if idx, exists := indexByKey[key]; exists {
			if replace {
				out[idx] = item
			}
			return
		}
		indexByKey[key] = len(out)
		out = append(out, item)
	}
	for _, item := range env {
		appendAllowed(item, false)
	}
	for _, item := range extra {
		appendAllowed(item, true)
	}
	out = append(out, "PATH="+TrustedPath())
	return out
}

// localAllowlist is the strict baseline used for local subprocesses: no auth
// tokens, no git plumbing overrides, no shell-init hooks.
func localAllowlist(key string) bool {
	switch {
	case key == "HOME",
		key == "USER",
		key == "LANG",
		key == "LC_ALL",
		key == "TZ",
		key == "TMPDIR":
		return true
	case strings.HasPrefix(key, "AUTO_IMPROVE_"),
		strings.HasPrefix(key, "FAKE_"),
		strings.HasPrefix(key, "PROMPT_"):
		return true
	case key == "REAL_GIT":
		return true
	default:
		return false
	}
}

// networkAllowlist extends localAllowlist with the minimum auth env required
// by gh/git to reach remote hosts (README requires `gh >= 2.40` for PR
// fetching; Go実装計画 L334 calls out `gh auth status` as a preflight check).
func networkAllowlist(key string) bool {
	if localAllowlist(key) {
		return true
	}
	switch key {
	case "SSH_AUTH_SOCK",
		"GH_TOKEN",
		"GITHUB_TOKEN",
		"GH_HOST",
		"KRB5CCNAME":
		return true
	}
	return false
}

func appendSafeGitProfile(env []string) []string {
	filtered := make([]string, 0, len(env)+20)
	for _, item := range env {
		key, _, ok := strings.Cut(item, "=")
		if !ok || safeGitProfileControlsKey(key) {
			continue
		}
		filtered = append(filtered, item)
	}

	config := []struct {
		key   string
		value string
	}{
		// Empty credential.helper resets helpers inherited from lower-priority
		// config scopes without preventing explicit token/ssh-agent auth.
		{key: "credential.helper", value: ""},
		{key: "core.hooksPath", value: os.DevNull},
		{key: "core.fsmonitor", value: "false"},
		{key: "core.sshCommand", value: "ssh -F " + os.DevNull},
		{key: "diff.external", value: ""},
	}

	filtered = append(filtered,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_COUNT="+fmt.Sprintf("%d", len(config)),
	)
	for i, entry := range config {
		filtered = append(filtered,
			fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, entry.key),
			fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, entry.value),
		)
	}
	filtered = append(filtered,
		"GIT_SSH_COMMAND=ssh -F "+os.DevNull,
		"GIT_ASKPASS=/bin/false",
		"SSH_ASKPASS=/bin/false",
		"GIT_TERMINAL_PROMPT=0",
	)
	return filtered
}

func safeGitProfileControlsKey(key string) bool {
	switch key {
	case "GIT_CONFIG_NOSYSTEM",
		"GIT_CONFIG_GLOBAL",
		"GIT_CONFIG_COUNT",
		"GIT_SSH",
		"GIT_SSH_COMMAND",
		"GIT_ASKPASS",
		"SSH_ASKPASS",
		"GIT_TERMINAL_PROMPT":
		return true
	}
	return strings.HasPrefix(key, "GIT_CONFIG_KEY_") ||
		strings.HasPrefix(key, "GIT_CONFIG_VALUE_")
}
