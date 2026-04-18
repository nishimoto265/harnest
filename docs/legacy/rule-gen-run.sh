#!/bin/bash
# ルール生成方式の比較実験
# Usage: MAIN_SURFACE=surface:XX bash scripts/rule-gen-run.sh {a|b|c}
#
# 方式A: 逐次レポート蓄積型（5並列分析 → 1統合）
# 方式B: バッチ一括型（1回で全処理）
# 方式C: 逐次リファイン型（5回逐次）

APPROACH=$1

if [ -z "$APPROACH" ]; then
  echo "Usage: MAIN_SURFACE=surface:XX bash scripts/rule-gen-run.sh {a|b|c}"
  exit 1
fi

REPO=$(cd "$(dirname "$0")/.." && pwd)
CLAUDE_BIN=$(readlink ~/.local/bin/claude 2>/dev/null || echo ~/.local/bin/claude)

# --- データマッピング ---
# PR番号:エージェントID:実験ID の順
PR_DATA=(
  "74:pr74-1:2026-04-15-about"
  "71:pr71-1:2026-04-15-top"
  "60:pr60-1:2026-04-15-recipe-comp"
  "84:pr84-1:2026-04-15-terms-r3"
  "59:pr59-1:2026-04-15-api-jsonld-r2"
)

# --- 共通ファイル ---
RULES_CHECKLIST="${REPO}/docs/harness-eval/checklists/rules-checklist.md"
REQUIREMENTS="${REPO}/docs/migration-plan/requirements.md"
ARCHITECTURE_GUIDE="${REPO}/docs/architecture-guide.md"

# --- ヘルパー関数 ---
get_pr() { echo "$1" | cut -d: -f1; }
get_agent() { echo "$1" | cut -d: -f2; }
get_exp() { echo "$1" | cut -d: -f3; }

get_log_dir() {
  local agent=$(get_agent "$1")
  local exp=$(get_exp "$1")
  echo "${REPO}/docs/harness-eval/results/${exp}/logs/${agent}"
}

get_actual_pr() {
  local pr=$(get_pr "$1")
  echo "${REPO}/docs/harness-eval/results/actual-prs/pr${pr}.patch"
}

wait_for_done() {
  local done_file=$1
  local timeout=${2:-1800}
  local elapsed=0
  while [ ! -f "$done_file" ]; do
    sleep 5
    elapsed=$((elapsed + 5))
    if [ $((elapsed % 60)) -eq 0 ]; then
      echo "    waiting... (${elapsed}s)"
    fi
    if [ "$elapsed" -ge "$timeout" ]; then
      echo "    TIMEOUT (${timeout}s)"
      return 1
    fi
  done
  return 0
}

extract_prompt() {
  local file=$1
  sed -n '/^```$/,/^```$/p' "$file" | sed '1d;$d'
}

# ================================================================
# 方式A: 逐次レポート蓄積型
# ================================================================
run_approach_a() {
  local out_dir="${REPO}/docs/harness-eval/results/rule-gen-a"
  mkdir -p "$out_dir"

  echo "=== 方式A: 逐次レポート蓄積型 ==="
  echo ""

  # --- Step 1: 5PR 並列分析 ---
  echo "[Step 1] 5PR 個別分析（並列）..."
  local step1_prompt=$(extract_prompt "${REPO}/docs/harness-eval/rule-gen-prompts/approach-a-step1.md")

  for entry in "${PR_DATA[@]}"; do
    local pr=$(get_pr "$entry")
    local agent=$(get_agent "$entry")
    local log_dir=$(get_log_dir "$entry")
    local actual_pr=$(get_actual_pr "$entry")
    local pr_out="${out_dir}/step1/pr${pr}"
    mkdir -p "$pr_out"

    local findings_file="${pr_out}/findings.md"
    local done_file="${pr_out}/done"
    local session_log="${pr_out}/session.jsonl"
    rm -f "$done_file" "$session_log"

    local prompt="$step1_prompt"
    prompt=$(echo "$prompt" | sed "s|{actual_pr_diff}|${actual_pr}|g")
    prompt=$(echo "$prompt" | sed "s|{agent_diff}|${log_dir}/diff.patch|g")
    prompt=$(echo "$prompt" | sed "s|{session_jsonl}|${log_dir}/session.jsonl|g")
    prompt=$(echo "$prompt" | sed "s|{file_access_log}|${log_dir}/file-access.log|g")
    prompt=$(echo "$prompt" | sed "s|{rules_checklist}|${RULES_CHECKLIST}|g")
    prompt=$(echo "$prompt" | sed "s|{requirements}|${REQUIREMENTS}|g")
    prompt=$(echo "$prompt" | sed "s|{architecture_guide}|${ARCHITECTURE_GUIDE}|g")
    prompt=$(echo "$prompt" | sed "s|{findings_file}|${findings_file}|g")
    prompt=$(echo "$prompt" | sed "s|{agent_id}|${agent}|g")

    (cd "$REPO" && "$CLAUDE_BIN" -p "$prompt" \
      --model sonnet \
      --effort medium \
      --output-format stream-json \
      --verbose \
      --disallowedTools Agent \
      --allowedTools "Read,Edit,Write,Grep,Glob,Bash" \
      > "$session_log" 2>&1
    echo "exit:$?" > "${pr_out}/exit_code"
    touch "$done_file") &

    echo "  pr${pr} → started"
  done

  # 完了待ち
  echo "  waiting for all 5..."
  for entry in "${PR_DATA[@]}"; do
    local pr=$(get_pr "$entry")
    wait_for_done "${out_dir}/step1/pr${pr}/done" 1800
    local exit_code=$(cat "${out_dir}/step1/pr${pr}/exit_code" 2>/dev/null || echo "unknown")
    local findings_size=$(wc -c < "${out_dir}/step1/pr${pr}/findings.md" 2>/dev/null || echo "0")
    echo "  pr${pr}: exit=${exit_code} findings=${findings_size}B"
  done

  # --- Step 2: 統合 ---
  echo ""
  echo "[Step 2] 5件統合→ルール化..."
  local step2_prompt=$(extract_prompt "${REPO}/docs/harness-eval/rule-gen-prompts/approach-a-step2.md")
  local step2_out="${out_dir}/step2"
  mkdir -p "$step2_out"
  local output_file="${step2_out}/ai-rules.md"
  local done_file="${step2_out}/done"
  local session_log="${step2_out}/session.jsonl"
  rm -f "$done_file" "$session_log"

  step2_prompt=$(echo "$step2_prompt" | sed "s|{pr74_findings}|${out_dir}/step1/pr74/findings.md|g")
  step2_prompt=$(echo "$step2_prompt" | sed "s|{pr71_findings}|${out_dir}/step1/pr71/findings.md|g")
  step2_prompt=$(echo "$step2_prompt" | sed "s|{pr60_findings}|${out_dir}/step1/pr60/findings.md|g")
  step2_prompt=$(echo "$step2_prompt" | sed "s|{pr84_findings}|${out_dir}/step1/pr84/findings.md|g")
  step2_prompt=$(echo "$step2_prompt" | sed "s|{pr59_findings}|${out_dir}/step1/pr59/findings.md|g")
  step2_prompt=$(echo "$step2_prompt" | sed "s|{output_file}|${output_file}|g")

  (cd "$REPO" && "$CLAUDE_BIN" -p "$step2_prompt" \
    --model sonnet \
    --effort medium \
    --output-format stream-json \
    --verbose \
    --disallowedTools Agent \
    --allowedTools "Read,Edit,Write,Grep,Glob,Bash" \
    > "$session_log" 2>&1
  echo "exit:$?" > "${step2_out}/exit_code"
  touch "$done_file") &

  wait_for_done "$done_file" 1800
  local exit_code=$(cat "${step2_out}/exit_code" 2>/dev/null || echo "unknown")
  local rules_size=$(wc -c < "$output_file" 2>/dev/null || echo "0")
  echo "  統合: exit=${exit_code} rules=${rules_size}B"
  touch "${out_dir}/done"
}

# ================================================================
# 方式B: バッチ一括型
# ================================================================
run_approach_b() {
  local out_dir="${REPO}/docs/harness-eval/results/rule-gen-b"
  mkdir -p "$out_dir"

  echo "=== 方式B: バッチ一括型 ==="
  echo ""

  local prompt=$(extract_prompt "${REPO}/docs/harness-eval/rule-gen-prompts/approach-b.md")
  local output_file="${out_dir}/ai-rules.md"
  local done_file="${out_dir}/done"
  local session_log="${out_dir}/session.jsonl"
  rm -f "$done_file" "$session_log"

  # 5PR分のデータを変数に展開
  for entry in "${PR_DATA[@]}"; do
    local pr=$(get_pr "$entry")
    local log_dir=$(get_log_dir "$entry")
    local actual_pr=$(get_actual_pr "$entry")
    prompt=$(echo "$prompt" | sed "s|{actual_pr${pr}}|${actual_pr}|g")
    prompt=$(echo "$prompt" | sed "s|{agent_pr${pr}}|${log_dir}/diff.patch|g")
    prompt=$(echo "$prompt" | sed "s|{session_pr${pr}}|${log_dir}/session.jsonl|g")
    prompt=$(echo "$prompt" | sed "s|{file_access_pr${pr}}|${log_dir}/file-access.log|g")
  done
  prompt=$(echo "$prompt" | sed "s|{rules_checklist}|${RULES_CHECKLIST}|g")
  prompt=$(echo "$prompt" | sed "s|{requirements}|${REQUIREMENTS}|g")
  prompt=$(echo "$prompt" | sed "s|{architecture_guide}|${ARCHITECTURE_GUIDE}|g")
  prompt=$(echo "$prompt" | sed "s|{output_file}|${output_file}|g")

  echo "[Running] 5PR一括分析→ルール化..."
  (cd "$REPO" && "$CLAUDE_BIN" -p "$prompt" \
    --model sonnet \
    --effort medium \
    --output-format stream-json \
    --verbose \
    --disallowedTools Agent \
    --allowedTools "Read,Edit,Write,Grep,Glob,Bash" \
    > "$session_log" 2>&1
  echo "exit:$?" > "${out_dir}/exit_code"
  touch "$done_file") &

  wait_for_done "$done_file" 3600
  local exit_code=$(cat "${out_dir}/exit_code" 2>/dev/null || echo "unknown")
  local rules_size=$(wc -c < "$output_file" 2>/dev/null || echo "0")
  echo "  完了: exit=${exit_code} rules=${rules_size}B"
  touch "${out_dir}/done"
}

# ================================================================
# 方式C: 逐次リファイン型
# ================================================================
run_approach_c() {
  local out_dir="${REPO}/docs/harness-eval/results/rule-gen-c"
  mkdir -p "$out_dir"

  echo "=== 方式C: 逐次リファイン型 ==="
  echo ""

  local first_prompt=$(extract_prompt "${REPO}/docs/harness-eval/rule-gen-prompts/approach-c-first.md")
  local next_prompt=$(extract_prompt "${REPO}/docs/harness-eval/rule-gen-prompts/approach-c-next.md")
  local prev_rules=""

  local step=0
  for entry in "${PR_DATA[@]}"; do
    step=$((step + 1))
    local pr=$(get_pr "$entry")
    local agent=$(get_agent "$entry")
    local log_dir=$(get_log_dir "$entry")
    local actual_pr=$(get_actual_pr "$entry")
    local step_dir="${out_dir}/step${step}-pr${pr}"
    mkdir -p "$step_dir"

    local findings_file="${step_dir}/findings.md"
    local rules_file="${step_dir}/rules.md"
    local done_file="${step_dir}/done"
    local session_log="${step_dir}/session.jsonl"
    rm -f "$done_file" "$session_log"

    echo "[Step ${step}/5] PR${pr} (${agent})..."

    local prompt
    if [ "$step" -eq 1 ]; then
      prompt="$first_prompt"
    else
      prompt="$next_prompt"
      prompt=$(echo "$prompt" | sed "s|{existing_rules}|${prev_rules}|g")
    fi

    prompt=$(echo "$prompt" | sed "s|{actual_pr_diff}|${actual_pr}|g")
    prompt=$(echo "$prompt" | sed "s|{agent_diff}|${log_dir}/diff.patch|g")
    prompt=$(echo "$prompt" | sed "s|{session_jsonl}|${log_dir}/session.jsonl|g")
    prompt=$(echo "$prompt" | sed "s|{file_access_log}|${log_dir}/file-access.log|g")
    prompt=$(echo "$prompt" | sed "s|{rules_checklist}|${RULES_CHECKLIST}|g")
    prompt=$(echo "$prompt" | sed "s|{requirements}|${REQUIREMENTS}|g")
    prompt=$(echo "$prompt" | sed "s|{architecture_guide}|${ARCHITECTURE_GUIDE}|g")
    prompt=$(echo "$prompt" | sed "s|{findings_file}|${findings_file}|g")
    prompt=$(echo "$prompt" | sed "s|{rules_file}|${rules_file}|g")
    prompt=$(echo "$prompt" | sed "s|{agent_id}|${agent}|g")

    (cd "$REPO" && "$CLAUDE_BIN" -p "$prompt" \
      --model sonnet \
      --effort medium \
      --output-format stream-json \
      --verbose \
      --disallowedTools Agent \
      --allowedTools "Read,Edit,Write,Grep,Glob,Bash" \
      > "$session_log" 2>&1
    echo "exit:$?" > "${step_dir}/exit_code"
    touch "$done_file") &

    # 逐次なので完了を待つ
    wait_for_done "$done_file" 1800
    local exit_code=$(cat "${step_dir}/exit_code" 2>/dev/null || echo "unknown")
    local findings_size=$(wc -c < "$findings_file" 2>/dev/null || echo "0")
    local rules_size=$(wc -c < "$rules_file" 2>/dev/null || echo "0")
    echo "  exit=${exit_code} findings=${findings_size}B rules=${rules_size}B"

    # 次のステップ用にルールファイルを記録
    prev_rules="$rules_file"
  done

  # 最終ルールをコピー
  if [ -f "$prev_rules" ]; then
    cp "$prev_rules" "${out_dir}/ai-rules.md"
    echo ""
    echo "最終ルール: ${out_dir}/ai-rules.md ($(wc -c < "${out_dir}/ai-rules.md")B)"
  fi
  touch "${out_dir}/done"
}

# ================================================================
# メイン
# ================================================================
echo "=== Rule Generation Experiment ==="
echo "Approach: ${APPROACH}"
echo "Time: $(date -u +%FT%TZ)"
echo ""

case "$APPROACH" in
  a) run_approach_a ;;
  b) run_approach_b ;;
  c) run_approach_c ;;
  *) echo "ERROR: approach must be a, b, or c"; exit 1 ;;
esac

echo ""
echo "[Done] $(date -u +%FT%TZ)"

# 通知
cmux notify --title "rule-gen完了" --body "方式${APPROACH}完了" 2>/dev/null || true
if [ -n "$MAIN_SURFACE" ]; then
  cmux send --surface "$MAIN_SURFACE" "ルール生成完了: 方式${APPROACH}" 2>/dev/null || true
  cmux send-key --surface "$MAIN_SURFACE" enter 2>/dev/null || true
fi
