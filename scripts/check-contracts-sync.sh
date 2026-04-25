#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
docs_file="$repo_root/docs/design/io-contracts.md"
registry_go="$repo_root/internal/contracts/registry.go"
intention_go="$repo_root/internal/contracts/intention.go"

declare -a mismatches=()

contains_ere() {
  local pattern="$1"
  local file="$2"
  if command -v rg >/dev/null 2>&1; then
    rg -q "$pattern" "$file"
  else
    grep -Eq "$pattern" "$file"
  fi
}

extract_ere() {
  local pattern="$1"
  local file="$2"
  if command -v rg >/dev/null 2>&1; then
    rg -o "$pattern" "$file"
  else
    grep -Eo "$pattern" "$file"
  fi
}

while IFS= read -r variant; do
  [[ -z "$variant" ]] && continue
  if ! contains_ere "type ${variant} struct" "$registry_go"; then
    mismatches+=("registry variant missing in code: ${variant}")
  fi
done < <(extract_ere 'RuleRegistry[A-Za-z]+' "$docs_file" | sort -u)

go_registry_variants="$(awk '
  /^func[[:space:]]*\(RuleRegistry[A-Za-z0-9_]+\)[[:space:]]+ruleRegistryVariant\(\)/ {
    variant = $0
    sub(/^func[[:space:]]*\(/, "", variant)
    sub(/\).*/, "", variant)
    if (variant != "") print variant
  }
' "$registry_go" | sort -u)"

if [[ -z "$go_registry_variants" ]]; then
  mismatches+=("no registry variants extracted from code")
fi

while IFS= read -r variant; do
  [[ -z "$variant" ]] && continue
  if ! contains_ere "(^|[^[:alnum:]_])${variant}([^[:alnum:]_]|$)" "$docs_file"; then
    mismatches+=("registry variant missing in docs: ${variant}")
  fi
done <<<"$go_registry_variants"

go_registry_kinds="$(awk '
  /^[[:space:]]*RegistryKind[A-Za-z0-9_]+[[:space:]]+RegistryKind[[:space:]]*=/ {
    kind = $0
    sub(/.*=[[:space:]]*"/, "", kind)
    sub(/".*/, "", kind)
    if (kind != "") print kind
  }
' "$registry_go" | sort -u)"

if [[ -z "$go_registry_kinds" ]]; then
  mismatches+=("no registry kinds extracted from code")
fi

while IFS= read -r kind; do
  [[ -z "$kind" ]] && continue
  if ! contains_ere "(^|[^[:alnum:]_])${kind}([^[:alnum:]_]|$)" "$docs_file"; then
    mismatches+=("registry kind missing in docs: ${kind}")
  fi
done <<<"$go_registry_kinds"

recovery_section="$(awk '
  /^\*\*Recovery state machine\*\*/ { in_section=1; next }
  /^\*\*planning recovery decision tree\*\*/ { in_section=0 }
  in_section { print }
' "$docs_file")"

go_stage_values="$(awk '
  /^[[:space:]]*IntentionStage[A-Za-z0-9_]+[[:space:]]+IntentionStage[[:space:]]*=/ {
    stage = $0
    sub(/.*=[[:space:]]*"/, "", stage)
    sub(/".*/, "", stage)
    if (stage != "") print stage
  }
' "$intention_go" | sort -u)"
docs_stage_values="$(awk '
  /^\| stage \| entry condition \| allowed next transitions \| required fields \| startup \/ recovery 動作 \|/ { in_table=1; next }
  in_table && /^\|---/ { next }
  in_table && !/^\|/ { exit }
  in_table {
    split($0, cols, "|")
    stage = cols[2]
    gsub(/^[[:space:]]+|[[:space:]]+$/, "", stage)
    if (stage ~ /^`[^`]+`$/) {
      gsub(/`/, "", stage)
      if (stage != "finalized") print stage
    }
  }
' <<<"$recovery_section" | sort -u)"

if [[ -z "$go_stage_values" ]]; then
  mismatches+=("no intention stages extracted from code")
fi
if [[ -z "$docs_stage_values" ]]; then
  mismatches+=("no intention stages extracted from recovery state machine table")
fi

while IFS= read -r stage; do
  [[ -z "$stage" ]] && continue
  mismatches+=("intention stage missing in recovery state machine table: ${stage}")
done < <(comm -23 <(printf '%s\n' "$go_stage_values") <(printf '%s\n' "$docs_stage_values"))

while IFS= read -r stage; do
  [[ -z "$stage" ]] && continue
  mismatches+=("recovery state machine table mentions unknown Go intention stage: ${stage}")
done < <(comm -13 <(printf '%s\n' "$go_stage_values") <(printf '%s\n' "$docs_stage_values"))

if ((${#mismatches[@]} > 0)); then
  printf 'contracts/docs drift detected:\n' >&2
  for mismatch in "${mismatches[@]}"; do
    printf ' - %s\n' "$mismatch" >&2
  done
  exit 1
fi

printf 'contracts/docs sync OK\n'
