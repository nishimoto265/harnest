#!/bin/sh
set -eu

prompt="$(cat)"
if [ -n "${PROMPT_CAPTURE_FILE:-}" ]; then
  printf '%s' "$prompt" > "$PROMPT_CAPTURE_FILE"
fi

printf '{"type":"session","event":"start"}\n'

printf 'implemented by fake claude\n' >> implemented.txt

if [ "${FAKE_SKIP_CHECKLIST:-0}" != "1" ]; then
  cat > checklist-result.json <<EOF
{"schema_version":"1","run_id":"${FAKE_RUN_ID:-2026-04-21-PR42-abcdef0}","pass":2,"agent":"${FAKE_AGENT:-a1}","items":[]}
EOF
fi

git add -A
git commit -m "fake implementation"

printf '{"type":"session","event":"done"}\n'
