#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
target="${1:-cmd/harnest}"

case "${target}" in
  /*) target_path="${target}" ;;
  *) target_path="${repo_root}/${target}" ;;
esac

if [[ ! -d "${target_path}" ]]; then
  echo "size-report: directory not found: ${target}" >&2
  exit 1
fi

find "${target_path}" -maxdepth 1 -type f -name '*.go' ! -name '*_test.go' -print0 |
  while IFS= read -r -d '' file; do
    lines="$(wc -l <"${file}" | tr -d ' ')"
    rel="${file#${repo_root}/}"
    printf '%6d %s\n' "${lines}" "${rel}"
  done |
  sort -nr
