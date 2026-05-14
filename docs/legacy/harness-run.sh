#!/usr/bin/env bash
set -euo pipefail

cat >&2 <<'EOF'
docs/legacy/harness-run.sh is archived and intentionally not runnable.

The active runtime is the Go CLI documented in README.md:
  harnest run --pr <n> --with-preflight
  harnest run --detect-loop --with-preflight
EOF
exit 1
