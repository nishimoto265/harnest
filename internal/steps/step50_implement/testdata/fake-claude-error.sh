#!/bin/sh
set -eu

prompt="$(cat)"
if [ -n "${PROMPT_CAPTURE_FILE:-}" ]; then
  printf '%s' "$prompt" > "$PROMPT_CAPTURE_FILE"
fi

printf 'rate_limit\n' >&2
exit 1
