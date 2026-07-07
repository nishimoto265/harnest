#!/bin/bash
# Claude Code フックから何が取れるかを観察するための probe スクリプト。
# 取得した情報をすべて 1 行の JSON にまとめて $PROBE_LOG_FILE (default: ~/probe-log.jsonl) に append する。
# プラグインや本番の hook の動作には一切影響を与えない（常に exit 0）。

set +e

HOOK_EVENT="${1:-unknown}"
LOG_FILE="${PROBE_LOG_FILE:-$HOME/probe-log.jsonl}"

# stdin を一度読み込む
STDIN_RAW=$(cat 2>/dev/null || true)

# jq が無くても probe 自体は壊れないようにする
if ! command -v jq >/dev/null 2>&1; then
  echo "{\"ts\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\",\"hook_event\":\"$HOOK_EVENT\",\"error\":\"jq not found\"}" >> "$LOG_FILE"
  exit 0
fi

# stdin が JSON として valid かを判定
if [ -n "$STDIN_RAW" ] && echo "$STDIN_RAW" | jq -e . >/dev/null 2>&1; then
  STDIN_VALID="true"
else
  STDIN_VALID="false"
fi

# stdin に含まれる代表的なフィールドを抽出
SESSION_ID=$(echo "$STDIN_RAW" | jq -r '.session_id // empty' 2>/dev/null)
TRANSCRIPT_PATH=$(echo "$STDIN_RAW" | jq -r '.transcript_path // empty' 2>/dev/null)
TOOL_NAME=$(echo "$STDIN_RAW" | jq -r '.tool_name // empty' 2>/dev/null)
TOOL_INPUT_FILE_PATH=$(echo "$STDIN_RAW" | jq -r '.tool_input.file_path // empty' 2>/dev/null)
TOOL_INPUT_COMMAND=$(echo "$STDIN_RAW" | jq -r '.tool_input.command // empty' 2>/dev/null)

# timeout コマンドを検出（macOS は gtimeout、Linux は timeout）
TIMEOUT_CMD=""
if command -v gtimeout >/dev/null 2>&1; then
  TIMEOUT_CMD="gtimeout 3"
elif command -v timeout >/dev/null 2>&1; then
  TIMEOUT_CMD="timeout 3"
fi

# cwd の git 情報（cwd が git 配下か）
GIT_TOPLEVEL=$(git rev-parse --show-toplevel 2>/dev/null || echo "")
GIT_REMOTE_ORIGIN=$(git remote get-url origin 2>/dev/null || echo "")
GIT_REMOTE_UPSTREAM=$(git remote get-url upstream 2>/dev/null || echo "")
GIT_BRANCH=$(git branch --show-current 2>/dev/null || echo "")
GIT_HEAD=$(git rev-parse HEAD 2>/dev/null || echo "")
GIT_REMOTES_RAW=$(git remote -v 2>/dev/null || echo "")
GIT_IS_WORKTREE=$(git rev-parse --is-inside-work-tree 2>/dev/null || echo "")
GIT_COMMON_DIR=$(git rev-parse --git-common-dir 2>/dev/null || echo "")
GIT_DIR=$(git rev-parse --git-dir 2>/dev/null || echo "")
GIT_SUPERPROJECT=$(git rev-parse --show-superproject-working-tree 2>/dev/null || echo "")

# tool_input.file_path のディレクトリの git 情報
FILE_DIR_GIT_TOPLEVEL=""
FILE_DIR_GIT_REMOTE=""
FILE_DIR_GIT_BRANCH=""
if [ -n "$TOOL_INPUT_FILE_PATH" ]; then
  if [ -e "$TOOL_INPUT_FILE_PATH" ]; then
    FILE_DIR=$(dirname "$TOOL_INPUT_FILE_PATH")
  else
    FILE_DIR=$(dirname "$TOOL_INPUT_FILE_PATH")
  fi
  if [ -d "$FILE_DIR" ]; then
    FILE_DIR_GIT_TOPLEVEL=$(git -C "$FILE_DIR" rev-parse --show-toplevel 2>/dev/null || echo "")
    FILE_DIR_GIT_REMOTE=$(git -C "$FILE_DIR" remote get-url origin 2>/dev/null || echo "")
    FILE_DIR_GIT_BRANCH=$(git -C "$FILE_DIR" branch --show-current 2>/dev/null || echo "")
  fi
fi

# PR 情報（gh CLI が使えれば、timeout 付きで）
PR_NUMBER=""
PR_URL=""
if command -v gh >/dev/null 2>&1; then
  PR_JSON=$($TIMEOUT_CMD gh pr view --json number,url 2>/dev/null || echo "")
  if [ -n "$PR_JSON" ]; then
    PR_NUMBER=$(echo "$PR_JSON" | jq -r '.number // empty' 2>/dev/null)
    PR_URL=$(echo "$PR_JSON" | jq -r '.url // empty' 2>/dev/null)
  fi
fi

# Claude Code 関連の env 変数（context のヒントになるもの）
CLAUDE_ENV_JSON=$(env | grep -E '^(CLAUDE|ANTHROPIC|WORKSPACE|PROJECT)' | jq -R -s 'split("\n") | map(select(length>0))' 2>/dev/null || echo '[]')

# stdin を JSON として埋め込む（valid なら parse、invalid なら文字列として）
if [ "$STDIN_VALID" = "true" ]; then
  STDIN_FIELD=$(echo "$STDIN_RAW" | jq -c .)
else
  STDIN_FIELD=$(jq -n --arg s "$STDIN_RAW" '{ raw: $s, parsed: false }')
fi

# 1 行 JSON で append
jq -nc \
  --arg ts "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --arg hook_event "$HOOK_EVENT" \
  --argjson stdin "$STDIN_FIELD" \
  --arg session_id "$SESSION_ID" \
  --arg transcript_path "$TRANSCRIPT_PATH" \
  --arg tool_name "$TOOL_NAME" \
  --arg tool_input_file_path "$TOOL_INPUT_FILE_PATH" \
  --arg tool_input_command "$TOOL_INPUT_COMMAND" \
  --arg cwd "$(pwd)" \
  --arg git_toplevel "$GIT_TOPLEVEL" \
  --arg git_remote_origin "$GIT_REMOTE_ORIGIN" \
  --arg git_branch "$GIT_BRANCH" \
  --arg git_head "$GIT_HEAD" \
  --arg git_remotes_raw "$GIT_REMOTES_RAW" \
  --arg git_remote_upstream "$GIT_REMOTE_UPSTREAM" \
  --arg git_is_worktree "$GIT_IS_WORKTREE" \
  --arg git_common_dir "$GIT_COMMON_DIR" \
  --arg git_dir "$GIT_DIR" \
  --arg git_superproject "$GIT_SUPERPROJECT" \
  --arg file_dir_git_toplevel "$FILE_DIR_GIT_TOPLEVEL" \
  --arg file_dir_git_remote "$FILE_DIR_GIT_REMOTE" \
  --arg file_dir_git_branch "$FILE_DIR_GIT_BRANCH" \
  --arg pr_number "$PR_NUMBER" \
  --arg pr_url "$PR_URL" \
  --argjson claude_env "$CLAUDE_ENV_JSON" \
  '{
    ts: $ts,
    hook_event: $hook_event,
    extracted: {
      session_id: $session_id,
      transcript_path: $transcript_path,
      tool_name: $tool_name,
      tool_input_file_path: $tool_input_file_path,
      tool_input_command: $tool_input_command
    },
    cwd: $cwd,
    git_from_cwd: {
      toplevel: $git_toplevel,
      remote_origin: $git_remote_origin,
      remote_upstream: $git_remote_upstream,
      branch: $git_branch,
      head: $git_head,
      remotes_raw: $git_remotes_raw,
      is_worktree: $git_is_worktree,
      git_dir: $git_dir,
      common_dir: $git_common_dir,
      superproject: $git_superproject
    },
    git_from_tool_file_path: {
      toplevel: $file_dir_git_toplevel,
      remote_origin: $file_dir_git_remote,
      branch: $file_dir_git_branch
    },
    pr: {
      number: $pr_number,
      url: $pr_url
    },
    claude_env: $claude_env,
    stdin: $stdin
  }' >> "$LOG_FILE" 2>>"$LOG_FILE.err"

exit 0
