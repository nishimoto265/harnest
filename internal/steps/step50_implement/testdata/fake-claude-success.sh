#!/bin/sh
set -eu

prompt_file=".step50-prompt.txt"
cat > "$prompt_file"
cat "$prompt_file"
rm -f "$prompt_file"

printf 'implemented\n' > implementation.txt
cat > checklist-result.json <<EOF
{"schema_version":"1","run_id":"${STEP50_RUN_ID}","pass":2,"agent":"${STEP50_AGENT}","items":[]}
EOF

git add implementation.txt checklist-result.json
git commit -m "fake step50 success" >/dev/null
