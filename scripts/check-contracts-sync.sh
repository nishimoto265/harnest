#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
docs_file="$repo_root/docs/design/io-contracts.md"
registry_go="$repo_root/internal/contracts/registry.go"
intention_go="$repo_root/internal/contracts/intention.go"

declare -a mismatches=()

while IFS= read -r variant; do
  [[ -z "$variant" ]] && continue
  if ! rg -q "type ${variant} struct" "$registry_go"; then
    mismatches+=("registry variant missing in code: ${variant}")
  fi
done < <(rg -o 'RuleRegistry[A-Za-z]+' "$docs_file" | sort -u)

go_stage_values="$(sed -n 's/^[[:space:]]*IntentionStage[A-Za-z0-9_]*[[:space:]]*IntentionStage = \"\\([^\"]*\\)\"/\\1/p' "$intention_go" | sort -u)"
recovery_section="$(awk '
  /^\*\*Recovery state machine\*\*/ { in_section=1; next }
  /^\*\*planning recovery decision tree\*\*/ { in_section=0 }
  in_section { print }
' "$docs_file")"

while IFS= read -r stage; do
  [[ -z "$stage" ]] && continue
  if ! grep -Fq "\`${stage}\`" <<<"$recovery_section"; then
    mismatches+=("intention stage missing in recovery state machine table: ${stage}")
  fi
done <<<"$go_stage_values"

while IFS= read -r stage; do
  [[ -z "$stage" ]] && continue
  if ! grep -Fq "\"${stage}\"" "$intention_go"; then
    mismatches+=("recovery state machine table mentions unknown Go intention stage: ${stage}")
  fi
done < <(grep -oE '`(planning|branch_pushed|registry_appended|decision_written|rolling_back_branch_reverted|rolling_back_registry_appended|rolling_back_decision_written|needs_manual_recovery)`' <<<"$recovery_section" | tr -d '`' | sort -u)

if ((${#mismatches[@]} > 0)); then
  printf 'contracts/docs drift detected:\n' >&2
  for mismatch in "${mismatches[@]}"; do
    printf ' - %s\n' "$mismatch" >&2
  done
  exit 1
fi

printf 'contracts/docs sync OK\n'
