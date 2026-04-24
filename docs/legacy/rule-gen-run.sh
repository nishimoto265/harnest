#!/usr/bin/env bash
set -euo pipefail

cat >&2 <<'EOF'
docs/legacy/rule-gen-run.sh is archived and intentionally not runnable.

The active runtime is the Go CLI documented in README.md:
  auto-improve run --pr <n> --with-preflight
  auto-improve run --detect-loop --with-preflight
EOF
exit 1
