package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type realGitIntegrationSHAs struct {
	base  string
	head  string
	merge string
}

func seedRealGitIntegrationRepo(t *testing.T, repoRoot string) realGitIntegrationSHAs {
	t.Helper()
	runIntegrationGit(t, repoRoot, "init", "-b", "main")
	runIntegrationGit(t, repoRoot, "config", "user.name", "Test User")
	runIntegrationGit(t, repoRoot, "config", "user.email", "test@example.com")
	daemonRoot := filepath.Join(filepath.Dir(repoRoot), "git-daemon")
	bareRemote := filepath.Join(daemonRoot, "owner", "repo.git")
	require.NoError(t, os.MkdirAll(filepath.Dir(bareRemote), 0o755))
	runIntegrationGit(t, repoRoot, "init", "--bare", bareRemote)
	runIntegrationGit(t, repoRoot, "remote", "add", "origin", bareRemote)

	require.NoError(t, os.MkdirAll(filepath.Join(repoRoot, "app"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repoRoot, "app", "message.txt"), []byte("base\n"), 0o644))
	runIntegrationGit(t, repoRoot, "add", "app/message.txt")
	runIntegrationGit(t, repoRoot, "commit", "-m", "base")
	baseSHA := strings.TrimSpace(runIntegrationGit(t, repoRoot, "rev-parse", "HEAD"))
	runIntegrationGit(t, repoRoot, "push", "origin", "HEAD:refs/heads/main")
	runIntegrationGit(t, repoRoot, "push", "origin", baseSHA+":refs/heads/auto-improve/best")

	runIntegrationGit(t, repoRoot, "checkout", "-b", "feature/pr-77")
	require.NoError(t, os.WriteFile(filepath.Join(repoRoot, "app", "message.txt"), []byte("merged change\n"), 0o644))
	runIntegrationGit(t, repoRoot, "commit", "-am", "change message")
	headSHA := strings.TrimSpace(runIntegrationGit(t, repoRoot, "rev-parse", "HEAD"))

	runIntegrationGit(t, repoRoot, "checkout", "main")
	runIntegrationGit(t, repoRoot, "merge", "--no-ff", "feature/pr-77", "-m", "merge pr 77")
	mergeSHA := strings.TrimSpace(runIntegrationGit(t, repoRoot, "rev-parse", "HEAD"))
	runIntegrationGit(t, repoRoot, "push", "origin", "HEAD:refs/heads/main")

	githubRemoteURL := "https://github.com/owner/repo.git"
	bareRemoteURL := "file://" + filepath.ToSlash(bareRemote)
	runIntegrationGit(t, repoRoot, "config", "url."+bareRemoteURL+".insteadOf", githubRemoteURL)
	runIntegrationGit(t, repoRoot, "remote", "set-url", "origin", githubRemoteURL)
	return realGitIntegrationSHAs{base: baseSHA, head: headSHA, merge: mergeSHA}
}

func seedIntegrationPolicyBranch(t *testing.T, repoRoot, branch string) {
	t.Helper()
	current := strings.TrimSpace(runIntegrationGit(t, repoRoot, "rev-parse", "--abbrev-ref", "HEAD"))
	runIntegrationGit(t, repoRoot, "checkout", "--orphan", "auto-improve-policy-seed")
	runIntegrationGit(t, repoRoot, "rm", "-r", "-f", "--ignore-unmatch", ".")
	require.NoError(t, os.MkdirAll(filepath.Join(repoRoot, "auto-improve"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repoRoot, "auto-improve", "rules-registry.jsonl"), []byte(""), 0o644))
	runIntegrationGit(t, repoRoot, "add", "auto-improve/rules-registry.jsonl")
	runIntegrationGit(t, repoRoot, "commit", "-m", "seed policy")
	runIntegrationGit(t, repoRoot, "push", "origin", "HEAD:refs/heads/"+branch)
	runIntegrationGit(t, repoRoot, "checkout", current)
}

func runIntegrationGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return string(out)
}

func recoverAdoptAnywayGitScript() string {
	return `#!/bin/sh
set -eu

state_dir="${AUTO_IMPROVE_GIT_STATE_DIR}"
mkdir -p "$state_dir"

if [ "${1:-}" = "-C" ]; then
  repo_dir="$2"
  shift 2
else
  repo_dir="$(pwd)"
fi

subcmd="$1"
shift

case "$subcmd" in
  config)
    case "${1:-} ${2:-}" in
      "--get remote.origin.url")
        printf '%s\n' "git@github.com:owner/repo.git"
        ;;
      *)
        exit 1
        ;;
    esac
    ;;
  ls-remote)
    branch="${4:-best}"
    printf '%s\trefs/heads/%s\n' "${AUTO_IMPROVE_TEST_REMOTE_SHA}" "$branch"
    ;;
  worktree)
    case "${1:-}" in
      list)
        if [ -f "$state_dir/worktrees.list" ]; then
          while IFS= read -r path; do
            [ -n "$path" ] || continue
            printf 'worktree %s\n\n' "$path"
          done < "$state_dir/worktrees.list"
        fi
        ;;
      remove)
        rm -rf "${3:-}"
        ;;
    esac
    ;;
  remote)
    case "${1:-} ${2:-}" in
      "get-url origin")
        printf '%s\n' "git@github.com:owner/repo.git"
        ;;
      *)
        exit 1
        ;;
    esac
    ;;
esac

exit 0
`
}

func brokenPolicyGitScript() string {
	return `#!/bin/sh
set -eu

if [ "${1:-}" = "-C" ]; then
  repo_dir="$2"
  shift 2
else
  repo_dir="$(pwd)"
fi

subcmd="$1"
shift

case "$subcmd" in
  config)
    case "${1:-} ${2:-}" in
      "--get remote.origin.url")
        printf '%s\n' "git@github.com:owner/repo.git"
        ;;
      *)
        exit 1
        ;;
    esac
    ;;
  rev-parse)
    if [ "${1:-}" = "--verify" ]; then
      echo "${AUTO_IMPROVE_TEST_BEST_SHA}"
      exit 0
    fi
    case "${1:-}" in
      *^1) echo "${AUTO_IMPROVE_TEST_BASE_SHA}" ;;
      HEAD) echo "${AUTO_IMPROVE_TEST_TARGET_SHA}" ;;
      refs/heads/*) echo "${AUTO_IMPROVE_TEST_TARGET_SHA}" ;;
      *) echo "${AUTO_IMPROVE_TEST_BEST_SHA}" ;;
    esac
    ;;
  fetch)
    exit 0
    ;;
  remote)
    case "${1:-} ${2:-}" in
      "get-url origin")
        printf '%s\n' "git@github.com:owner/repo.git"
        ;;
      *)
        exit 1
        ;;
    esac
    ;;
  ls-tree)
    printf '%s\n' "auto-improve/rules-registry.jsonl"
    printf '%s\n' "auto-improve/rules/r-bad.md"
    ;;
  show)
    case "${1:-}" in
      origin/auto-improve/policy:auto-improve/rules-registry.jsonl)
        printf '%s\n' '{"kind":"added","schema_version":"1","rule_id":"r-bad","rule_path":"rules/r-bad.md","sha256":"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff","idempotency_key":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","version_seq":1,"prev_hash":"","by_run_id":"2026-04-23-PR1-feedbee","at":"2026-04-23T08:00:00Z"}'
        ;;
      origin/auto-improve/policy:auto-improve/rules/r-bad.md)
        printf '%s\n' '# broken policy'
        ;;
      *)
        exit 1
        ;;
    esac
    ;;
  merge-base)
    if [ "${1:-}" = "--is-ancestor" ]; then
      exit 0
    fi
    ;;
  worktree)
    case "${1:-}" in
      add)
        if [ "${2:-}" = "-b" ]; then
          mkdir -p "$4"
        else
          mkdir -p "$2"
        fi
        ;;
      remove)
        rm -rf "${3:-}"
        ;;
      list)
        exit 0
        ;;
    esac
    ;;
  diff|ls-files|status|branch|ls-remote|push)
    exit 0
    ;;
  *)
    exit 1
    ;;
esac

exit 0
`
}
