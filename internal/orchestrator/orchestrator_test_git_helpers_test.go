package orchestrator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func overwriteFakeGitScript(t *testing.T, binDir, script string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "git"), []byte(script), 0o755))
}

func manualRecoveryGitScript() string {
	return `#!/bin/sh
set -eu

state_dir="${AUTO_IMPROVE_GIT_STATE_DIR}"
mkdir -p "$state_dir"
: >> "$state_dir/worktrees.list"

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
        echo "unsupported config args: $*" >&2
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
        echo "unsupported remote args: $*" >&2
        exit 1
        ;;
    esac
    ;;
  merge-base)
    if [ "${1:-}" = "--is-ancestor" ]; then
      exit 0
    fi
    ;;
  add)
    exit 0
    ;;
  write-tree)
    echo "0000000000000000000000000000000000000000"
    ;;
  commit-tree)
    echo "${AUTO_IMPROVE_TEST_TARGET_SHA}"
    ;;
  update-ref|reset)
    exit 0
    ;;
  worktree)
    case "${1:-}" in
      add)
        if [ "${2:-}" = "-b" ]; then
          path="$4"
        else
          path="$2"
        fi
        mkdir -p "$path"
        { grep -Fqx "$path" "$state_dir/worktrees.list" 2>/dev/null || printf '%s\n' "$path" >> "$state_dir/worktrees.list"; } || true
        ;;
      remove)
        rm -rf "$3"
        ;;
      list)
        while IFS= read -r path; do
          [ -n "$path" ] || continue
          printf 'worktree %s\n\n' "$path"
        done < "$state_dir/worktrees.list"
        ;;
    esac
    ;;
  diff)
    if [ "${1:-}" = "--cached" ] || [ "${1:-}" = "--name-only" ]; then
      exit 0
    fi
    for file in "$repo_dir"/generated-*; do
      [ -e "$file" ] || continue
      name="$(basename "$file")"
      printf 'diff --git a/%s b/%s\n' "$name" "$name"
      printf 'new file mode 100644\n'
      printf 'index 0000000..1111111\n'
      printf -- '--- /dev/null\n'
      printf '+++ b/%s\n' "$name"
      printf '@@ -0,0 +1 @@\n'
      printf '+generated change\n'
      exit 0
    done
    exit 0
    ;;
  ls-files)
    for file in "$repo_dir"/generated-*; do
      [ -e "$file" ] || continue
      printf '%s\0' "$(basename "$file")"
      exit 0
    done
    exit 0
    ;;
  status)
    exit 0
    ;;
  branch)
    case "$(basename "$repo_dir")" in
      *-pass1-a1) echo "auto-improve/$(basename "$repo_dir" | sed 's/-pass1-a1$//')/pass1/a1" ;;
      *-pass1-a2) echo "auto-improve/$(basename "$repo_dir" | sed 's/-pass1-a2$//')/pass1/a2" ;;
      *-pass1-a3) echo "auto-improve/$(basename "$repo_dir" | sed 's/-pass1-a3$//')/pass1/a3" ;;
      *-pass2-a1) echo "auto-improve/$(basename "$repo_dir" | sed 's/-pass2-a1$//')/pass2/a1" ;;
      *-pass2-a2) echo "auto-improve/$(basename "$repo_dir" | sed 's/-pass2-a2$//')/pass2/a2" ;;
      *-pass2-a3) echo "auto-improve/$(basename "$repo_dir" | sed 's/-pass2-a3$//')/pass2/a3" ;;
      *) echo "stub-branch" ;;
    esac
    ;;
  ls-remote)
    if [ -f "$state_dir/after-push" ]; then
      exit 0
    fi
    branch="${4:-best}"
    printf '%s\trefs/heads/%s\n' "${AUTO_IMPROVE_TEST_BEST_SHA}" "$branch"
    ;;
  push)
    touch "$state_dir/after-push"
    echo "non-fast-forward" >&2
    exit 1
    ;;
esac

exit 0
`
}

func postPushRollbackGitScript() string {
	return `#!/bin/sh
set -eu

state_dir="${AUTO_IMPROVE_GIT_STATE_DIR}"
mkdir -p "$state_dir"
: >> "$state_dir/worktrees.list"

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
        echo "unsupported config args: $*" >&2
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
        echo "unsupported remote args: $*" >&2
        exit 1
        ;;
    esac
    ;;
  merge-base)
    if [ "${1:-}" = "--is-ancestor" ]; then
      exit 0
    fi
    ;;
  add)
    exit 0
    ;;
  write-tree)
    echo "0000000000000000000000000000000000000000"
    ;;
  commit-tree)
    echo "${AUTO_IMPROVE_TEST_TARGET_SHA}"
    ;;
  update-ref|reset)
    exit 0
    ;;
  worktree)
    case "${1:-}" in
      add)
        if [ "${2:-}" = "-b" ]; then
          path="$4"
        else
          path="$2"
        fi
        mkdir -p "$path"
        { grep -Fqx "$path" "$state_dir/worktrees.list" 2>/dev/null || printf '%s\n' "$path" >> "$state_dir/worktrees.list"; } || true
        ;;
      remove)
        rm -rf "$3"
        ;;
      list)
        while IFS= read -r path; do
          [ -n "$path" ] || continue
          printf 'worktree %s\n\n' "$path"
        done < "$state_dir/worktrees.list"
        ;;
    esac
    ;;
  diff)
    if [ "${1:-}" = "--cached" ] || [ "${1:-}" = "--name-only" ]; then
      exit 0
    fi
    for file in "$repo_dir"/generated-*; do
      [ -e "$file" ] || continue
      name="$(basename "$file")"
      printf 'diff --git a/%s b/%s\n' "$name" "$name"
      printf 'new file mode 100644\n'
      printf 'index 0000000..1111111\n'
      printf -- '--- /dev/null\n'
      printf '+++ b/%s\n' "$name"
      printf '@@ -0,0 +1 @@\n'
      printf '+generated change\n'
      exit 0
    done
    exit 0
    ;;
  ls-files)
    for file in "$repo_dir"/generated-*; do
      [ -e "$file" ] || continue
      printf '%s\0' "$(basename "$file")"
      exit 0
    done
    exit 0
    ;;
  status)
    exit 0
    ;;
  branch)
    case "$(basename "$repo_dir")" in
      *-pass1-a1) echo "auto-improve/$(basename "$repo_dir" | sed 's/-pass1-a1$//')/pass1/a1" ;;
      *-pass1-a2) echo "auto-improve/$(basename "$repo_dir" | sed 's/-pass1-a2$//')/pass1/a2" ;;
      *-pass1-a3) echo "auto-improve/$(basename "$repo_dir" | sed 's/-pass1-a3$//')/pass1/a3" ;;
      *-pass2-a1) echo "auto-improve/$(basename "$repo_dir" | sed 's/-pass2-a1$//')/pass2/a1" ;;
      *-pass2-a2) echo "auto-improve/$(basename "$repo_dir" | sed 's/-pass2-a2$//')/pass2/a2" ;;
      *-pass2-a3) echo "auto-improve/$(basename "$repo_dir" | sed 's/-pass2-a3$//')/pass2/a3" ;;
      *) echo "stub-branch" ;;
    esac
    ;;
  ls-remote)
    branch="${4:-best}"
    if [ -f "$state_dir/pushed-target" ]; then
      printf '%s\trefs/heads/%s\n' "${AUTO_IMPROVE_TEST_TARGET_SHA}" "$branch"
    else
      printf '%s\trefs/heads/%s\n' "${AUTO_IMPROVE_TEST_BEST_SHA}" "$branch"
    fi
    ;;
  push)
    refspec="${2:-}"
    if [ "$refspec" = "${AUTO_IMPROVE_TEST_TARGET_SHA}:best" ]; then
      mkdir -p "$(dirname "$AUTO_IMPROVE_TEST_SENTINEL_PATH")"
      cat > "$AUTO_IMPROVE_TEST_SENTINEL_PATH" <<EOF
{"run_id":"2026-04-21-PR99-deadbee","pr":99,"reason":"transactional_failure","failed_step":"70","created_at":"2026-04-21T00:00:00Z"}
EOF
      touch "$state_dir/pushed-target"
      exit 0
    fi
    exit 0
    ;;
esac

exit 0
`
}
