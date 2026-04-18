#!/bin/bash
# ハーネス実験の自動実行スクリプト
# Usage: MAIN_SURFACE=surface:XX [FROM_BRANCH=branch] bash scripts/harness-run.sh <PR番号> <実験ID> <タスク指示文> [実行数=3] [モデル=sonnet] [effort=medium]
#
# 例: MAIN_SURFACE=surface:46 bash scripts/harness-run.sh 74 "2026-04-15-about" "P8: /about ページを実装してください。..." 3 sonnet medium
# FROM_BRANCH: worktreeの設定元ブランチ（省略時はbest設定を適用）
# MAIN_SURFACE: 完了時にcmux sendで通知するsurface ID（省略可）

PR=$1
EXPERIMENT_ID=$2
TASK=$3
COUNT=${4:-3}
MODEL=${5:-sonnet}
EFFORT=${6:-medium}

if [ -z "$PR" ] || [ -z "$EXPERIMENT_ID" ] || [ -z "$TASK" ]; then
  echo "Usage: MAIN_SURFACE=surface:XX [FROM_BRANCH=branch] bash scripts/harness-run.sh <PR番号> <実験ID> <タスク指示文|@ファイルパス> [実行数] [モデル] [effort]"
  exit 1
fi

# タスク指示文: @で始まる場合はファイルから読む
if [[ "$TASK" == @* ]]; then
  TASK_FILE="${TASK#@}"
  # 相対パスならREPO基準
  if [[ "$TASK_FILE" != /* ]]; then
    TASK_FILE="$(cd "$(dirname "$0")/.." && pwd)/${TASK_FILE}"
  fi
  if [ ! -f "$TASK_FILE" ]; then
    echo "ERROR: タスクファイルが見つかりません: $TASK_FILE"
    exit 1
  fi
  TASK=$(cat "$TASK_FILE")
fi

REPO=$(cd "$(dirname "$0")/.." && pwd)
CLAUDE_BIN=$(readlink ~/.local/bin/claude 2>/dev/null || echo ~/.local/bin/claude)
LOCK_FILE="/tmp/harness-run-pr${PR}.lock"

# --- ロック（同じPRの二重起動防止） ---
if [ -f "$LOCK_FILE" ]; then
  OLD_PID=$(cat "$LOCK_FILE")
  if kill -0 "$OLD_PID" 2>/dev/null; then
    echo "ERROR: PR#${PR} は既に実行中 (PID: $OLD_PID)"
    echo "強制実行するには: rm $LOCK_FILE"
    exit 1
  else
    echo "WARN: 古いロックファイルを削除 (PID: $OLD_PID は存在しない)"
    rm -f "$LOCK_FILE"
  fi
fi
echo $$ > "$LOCK_FILE"

# 終了時にロック解除
cleanup() {
  rm -f "$LOCK_FILE"
}
trap cleanup EXIT

# --- base commit取得 ---
BASE_COMMIT=$(gh pr view $PR --repo everytv/delish-web-global --json baseRefOid --jq .baseRefOid 2>/dev/null)
if [ -z "$BASE_COMMIT" ]; then
  echo "ERROR: PR#${PR} のbase commitを取得できません"
  exit 1
fi

echo "=== Harness Run ==="
echo "PR: #${PR}"
echo "Experiment: ${EXPERIMENT_ID}"
echo "Base: ${BASE_COMMIT}"
echo "Model: ${MODEL}, Effort: ${EFFORT}, Count: ${COUNT}"
echo ""

# --- Step 1: Worktree作成 ---
echo "[Step 1] Creating ${COUNT} worktrees..."
for i in $(seq 1 $COUNT); do
  BRANCH="eval-pr${PR}-${i}"
  WORKTREE="/tmp/eval-pr${PR}-${i}"
  LOG_DIR="/tmp/eval-pr${PR}-${i}-logs"

  # ログディレクトリ（先に作成。worktree失敗時もdoneを書けるように）
  mkdir -p "$LOG_DIR"
  # 前回のdone/exit_codeを削除（再実行時にwait loopが即完了と誤判定するのを防ぐ）
  rm -f "$LOG_DIR/done" "$LOG_DIR/exit_code" "$LOG_DIR/session.jsonl"

  # クリーンアップ（既存があれば）
  git -C "$REPO" worktree remove "$WORKTREE" --force 2>/dev/null || true
  rm -rf "$WORKTREE"
  git -C "$REPO" worktree prune 2>/dev/null || true
  git -C "$REPO" branch -D "$BRANCH" 2>/dev/null || true

  # 作成（エラーを表示）
  WT_ERR=$(git -C "$REPO" worktree add "$WORKTREE" -b "$BRANCH" "$BASE_COMMIT" 2>&1)
  if [ $? -ne 0 ]; then
    echo "  ERROR: pr${PR}-${i} worktree作成失敗: $WT_ERR"
    echo "exit:worktree-failed" > "$LOG_DIR/exit_code"
    touch "$LOG_DIR/done"
    continue
  fi

  # 設定適用
  if [ -n "$FROM_BRANCH" ]; then
    # FROM_BRANCH指定時: そのブランチから全設定を適用
    git -C "$WORKTREE" checkout "$FROM_BRANCH" -- CLAUDE.md .claude/ docs/architecture-guide.md docs/harness-eval/checklists/ scripts/generate-checklist.sh docs/ai-rules/ docs/frontend-rules/ eslint.config.mjs 2>/dev/null || true
    # チェックリスト再生成
    (cd "$WORKTREE" && bash scripts/generate-checklist.sh > docs/harness-eval/checklists/rules-checklist.md 2>/dev/null) || true
  else
    # デフォルト: best設定を適用
    git -C "$WORKTREE" checkout feature/nishimoto-best -- CLAUDE.md .claude/ docs/architecture-guide.md docs/harness-eval/checklists/ scripts/generate-checklist.sh 2>/dev/null || true
  fi

  # node_modules symlink
  ln -s "$REPO/node_modules" "$WORKTREE/node_modules" 2>/dev/null || true

  echo "  pr${PR}-${i}: ready"
done

# --- Step 2: エージェント起動 ---
echo ""
echo "[Step 2] Launching ${COUNT} agents..."
for i in $(seq 1 $COUNT); do
  WORKTREE="/tmp/eval-pr${PR}-${i}"
  LOG_DIR="/tmp/eval-pr${PR}-${i}-logs"

  if [ ! -d "$WORKTREE" ]; then
    echo "  SKIP: pr${PR}-${i} (worktreeなし)"
    touch "$LOG_DIR/done"  # スキップもdone扱い
    continue
  fi

  # claude -p 実行。成功でも失敗でもdoneファイルを作成
  (cd "$WORKTREE" && EVAL_LOG_DIR="$LOG_DIR" "$CLAUDE_BIN" -p "$TASK" \
    --model "$MODEL" \
    --effort "$EFFORT" \
    --output-format stream-json \
    --verbose \
    --disallowedTools Agent \
    --append-system-prompt "実装完了後は必ず git add -A && git commit を実行すること。" \
    > "$LOG_DIR/session.jsonl" 2>&1
  echo "exit:$?" > "$LOG_DIR/exit_code"
  # 未コミットの変更があればスクリプト側で自動コミット
  cd "$WORKTREE" && (git diff --quiet && git diff --cached --quiet) 2>/dev/null || \
    (git add -A && git commit -m "auto-commit: harness-eval" 2>/dev/null)
  touch "$LOG_DIR/done") &

  echo "  pr${PR}-${i} → PID $!"
done

# --- Step 3: 完了待ち（タイムアウト付き） ---
echo ""
echo "[Step 3] Waiting for completion..."
TIMEOUT=1800  # 30分
ELAPSED=0
while true; do
  DONE_COUNT=0
  for i in $(seq 1 $COUNT); do
    [ -f "/tmp/eval-pr${PR}-${i}-logs/done" ] && DONE_COUNT=$((DONE_COUNT+1))
  done

  if [ "$DONE_COUNT" -ge "$COUNT" ]; then
    echo "  All ${COUNT} agents completed!"
    break
  fi

  if [ "$ELAPSED" -ge "$TIMEOUT" ]; then
    echo "  TIMEOUT: ${ELAPSED}s elapsed. ${DONE_COUNT}/${COUNT} completed."
    # 残りのclaude -pを殺す
    for i in $(seq 1 $COUNT); do
      if [ ! -f "/tmp/eval-pr${PR}-${i}-logs/done" ]; then
        echo "  killing pr${PR}-${i}..."
        touch "/tmp/eval-pr${PR}-${i}-logs/done"
        echo "exit:timeout" > "/tmp/eval-pr${PR}-${i}-logs/exit_code"
      fi
    done
    pkill -f "eval-pr${PR}.*claude" 2>/dev/null || true
    break
  fi

  echo "  done: ${DONE_COUNT}/${COUNT} (${ELAPSED}s)"
  sleep 10
  ELAPSED=$((ELAPSED+10))
done

# --- Step 4: 結果収集 ---
echo ""
echo "[Step 4] Collecting results..."
RESULT_DIR="${REPO}/docs/harness-eval/results/${EXPERIMENT_ID}/logs"

for i in $(seq 1 $COUNT); do
  WORKTREE="/tmp/eval-pr${PR}-${i}"
  LOG_DIR="/tmp/eval-pr${PR}-${i}-logs"
  DST="${RESULT_DIR}/pr${PR}-${i}"
  mkdir -p "$DST"

  # ログコピー
  cp "$LOG_DIR"/session.jsonl "$DST"/ 2>/dev/null || true
  cp "$LOG_DIR"/file-access.log "$DST"/ 2>/dev/null || true
  cp "$LOG_DIR"/hook-blocks.log "$DST"/ 2>/dev/null || true
  cp "$LOG_DIR"/timestamps.json "$DST"/ 2>/dev/null || true
  cp "$LOG_DIR"/exit_code "$DST"/ 2>/dev/null || true
  cp "$WORKTREE"/checklist.md "$DST"/ 2>/dev/null || true

  # git結果（base commitからの full diff）
  git -C "$WORKTREE" diff "$BASE_COMMIT" --stat > "$DST/diff-stat.txt" 2>/dev/null || true
  git -C "$WORKTREE" diff "$BASE_COMMIT" > "$DST/diff.patch" 2>/dev/null || true
  git -C "$WORKTREE" ls-files --others --exclude-standard > "$DST/new-files.txt" 2>/dev/null || true

  # サマリ
  EXIT=$(cat "$LOG_DIR/exit_code" 2>/dev/null || echo "unknown")
  COMMITTED=$(git -C "$WORKTREE" log --oneline -1 2>/dev/null | head -1)
  echo "  pr${PR}-${i}: exit:${EXIT} commit:${COMMITTED}"
done

# 設定スナップショット
CONFIG_DIR="${REPO}/docs/harness-eval/results/${EXPERIMENT_ID}/configs"
mkdir -p "$CONFIG_DIR"
cp /tmp/eval-pr${PR}-1/CLAUDE.md "$CONFIG_DIR"/CLAUDE.md 2>/dev/null || true
cp /tmp/eval-pr${PR}-1/.claude/settings.json "$CONFIG_DIR"/settings.json 2>/dev/null || true

echo ""
echo "[Done] Results saved to docs/harness-eval/results/${EXPERIMENT_ID}/"
echo ""
echo "Next: 採点 → result.json作成 → worktreeクリーンアップ"

# 通知
cmux notify --title "harness-eval完了" --body "PR#${PR} ${EXPERIMENT_ID} (${COUNT}体)" 2>/dev/null || true

# メインセッションに通知
if [ -n "$MAIN_SURFACE" ]; then
  cmux send --surface "$MAIN_SURFACE" "実験完了: PR#${PR} ${EXPERIMENT_ID} (${COUNT}体完了)" 2>/dev/null || true
  cmux send-key --surface "$MAIN_SURFACE" enter 2>/dev/null || true
fi
