#!/bin/sh
set -eu

prompt="$(cat)"
if [ -n "${PROMPT_CAPTURE_FILE:-}" ]; then
  printf '%s' "$prompt" > "$PROMPT_CAPTURE_FILE"
fi

printf '{"type":"session","event":"start"}\n'

printf 'implemented by fake claude\n' >> implemented.txt

if [ "${FAKE_MKFIFO_CHECKLIST:-0}" = "1" ]; then
  rm -f checklist-result.json
  mkfifo checklist-result.json
elif [ "${FAKE_SKIP_CHECKLIST:-0}" != "1" ]; then
  cat > checklist-result.json <<EOF
{"schema_version":"1","run_id":"${FAKE_RUN_ID:-2026-04-21-PR42-abcdef0}","pass":2,"agent":"${FAKE_AGENT:-a1}","items":[]}
EOF
fi

if [ -n "${FAKE_CHECKOUT_REF_BEFORE_COMMIT:-}" ]; then
  git checkout "${FAKE_CHECKOUT_REF_BEFORE_COMMIT}"
fi

if [ -n "${FAKE_DETACH_HELPER:-}" ]; then
  "${FAKE_DETACH_HELPER}" \
    "${FAKE_DETACHED_PID_PATH}" \
    "${FAKE_DETACH_DELAY:-200ms}"
fi

git add -A
git commit -m "fake implementation"

printf '{"type":"session","event":"done"}\n'
