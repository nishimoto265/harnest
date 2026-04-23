package processenv

import (
	"os"
	"strings"
)

// trustedPATH is the fixed PATH used for every sanitized subprocess env.
// Callers MUST NOT inherit the caller's $PATH since shell-init payloads can
// inject attacker-controlled binaries earlier on the path.
const trustedPATH = "/usr/bin:/bin:/usr/sbin:/sbin:/usr/local/bin:/opt/homebrew/bin"

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
//   - GIT_ASKPASS: gh-provided credential helper hook (set by `gh auth setup-git`)
//
// Shell-init-injection vectors (BASH_ENV, LD_PRELOAD, GIT_CONFIG_* overrides,
// etc.) are still blocked. Callers that only need local filesystem + git
// plumbing on a pre-carved worktree must keep using SanitizeForLocalExec.
func SanitizeForNetworkExec(extra ...string) []string {
	return sanitize(extra, networkAllowlist)
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
	out = append(out, "PATH="+trustedPATH)
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
		"GIT_ASKPASS",
		"KRB5CCNAME":
		return true
	}
	return false
}
