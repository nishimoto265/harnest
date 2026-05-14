package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func writeIntegrationAgentsConfig(t *testing.T, root, claudePath string) {
	t.Helper()
	content := "profiles:\n" +
		"  fake-claude:\n" +
		"    provider: claude\n" +
		"    binary: " + yamlDoubleQuote(claudePath) + "\n" +
		"    args: [\"-p\"]\n" +
		"  judge-primary:\n" +
		"    provider: stub\n" +
		"roles:\n" +
		"  implementer: fake-claude\n" +
		"  judge_primary: judge-primary\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "agents.yaml"), []byte(content), 0o644))

	configPath := filepath.Join(root, "config.yaml")
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	if strings.Contains(string(data), "agent_config_path:") {
		return
	}
	require.NoError(t, os.WriteFile(configPath, append(data, []byte("agent_config_path: \"./agents.yaml\"\n")...), 0o644))
}

func writeIntegrationAdoptAgentsConfig(t *testing.T, root, implementerPath, judgePath string) {
	t.Helper()
	content := "profiles:\n" +
		"  fake-implementer:\n" +
		"    provider: claude\n" +
		"    binary: " + yamlDoubleQuote(implementerPath) + "\n" +
		"    args: [\"-p\"]\n" +
		"  fake-judge-primary:\n" +
		"    provider: claude\n" +
		"    binary: " + yamlDoubleQuote(judgePath) + "\n" +
		"roles:\n" +
		"  implementer: fake-implementer\n" +
		"  judge_primary: fake-judge-primary\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "agents.yaml"), []byte(content), 0o644))

	configPath := filepath.Join(root, "config.yaml")
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	if strings.Contains(string(data), "agent_config_path:") {
		return
	}
	require.NoError(t, os.WriteFile(configPath, append(data, []byte("agent_config_path: \"./agents.yaml\"\n")...), 0o644))
}

func fakeClaudeScript(delay time.Duration) string {
	return "#!/bin/sh\n" +
		"set -eu\n" +
		"if [ \"${1:-}\" = \"--version\" ]; then printf 'claude 1.0.0\\n'; exit 0; fi\n" +
		"sleep " + formatSleep(delay) + "\n" +
		"cat > checklist-result.json <<EOF\n" +
		"{\"schema_version\":\"1\",\"run_id\":\"${HARNEST_RUN_ID}\",\"pass\":${HARNEST_PASS},\"agent\":\"${HARNEST_AGENT}\",\"items\":[]}\n" +
		"EOF\n" +
		"printf 'generated change\\n' > \"generated-${HARNEST_PASS}-${HARNEST_AGENT}.txt\"\n" +
		"printf '{\"event\":\"ok\"}\\n'\n"
}

func fakeAdoptImplementerScript() string {
	return `#!/bin/sh
set -eu

if [ "${1:-}" = "--version" ]; then
  printf 'claude 1.0.0\n'
  exit 0
fi

cat > checklist-result.json <<EOF
{"schema_version":"1","run_id":"${HARNEST_RUN_ID}","pass":${HARNEST_PASS},"agent":"${HARNEST_AGENT}","items":[]}
EOF
mkdir -p app
printf 'pass=%s agent=%s\n' "${HARNEST_PASS}" "${HARNEST_AGENT}" > "app/generated-${HARNEST_PASS}-${HARNEST_AGENT}.txt"
printf '{"event":"ok"}\n'
`
}

func fakeAdoptJudgeScript() string {
	return `#!/bin/sh
set -eu

if [ "${1:-}" = "--version" ]; then
  printf 'claude 1.0.0\n'
  exit 0
fi

prompt="$(cat)"
if printf '%s' "$prompt" | grep -q 'final decision judge for step60 pairwise'; then
  printf '{"decision":"adopt","agent_decisions":['
  printf '{"agent":"a1","winner":"B","margin":"clear","justification":"fake pass2 win"},'
  printf '{"agent":"a2","winner":"B","margin":"clear","justification":"fake pass2 win"},'
  printf '{"agent":"a3","winner":"B","margin":"clear","justification":"fake pass2 win"}'
  printf '],"justification":"fake pairwise final decision"}\n'
  exit 0
fi
if printf '%s' "$prompt" | grep -q 'step60 true pairwise judge'; then
  printf '{"winner":"B","margin":"clear","dimension_votes":['
  printf '{"dimension":"fidelity","winner":"B","reason":"fake pairwise vote"},'
  printf '{"dimension":"correctness","winner":"B","reason":"fake pairwise vote"},'
  printf '{"dimension":"maintainability","winner":"B","reason":"fake pairwise vote"},'
  printf '{"dimension":"discipline","winner":"B","reason":"fake pairwise vote"},'
  printf '{"dimension":"communication","winner":"B","reason":"fake pairwise vote"}'
  printf '],"fatal_issues":[],"justification":"fake pairwise comparison"}\n'
  exit 0
fi
if printf '%s' "$prompt" | grep -q 'step30 pass1'; then
  score=55
  verdict=violated
  rationale='pass1 intentionally violates fake adoption rule'
else
  score=95
  verdict=compliant
  rationale='pass2 satisfies fake adoption rule'
fi

rules="$(printf '%s\n' "$prompt" | awk '
  /^Required compliance rule_ids:/ { collecting=1; next }
  collecting && /^- / { sub(/^- /, ""); print; next }
  collecting && NF == 0 { exit }
  collecting && !/^- / { exit }
')"

printf '{"scores":['
printf '{"dimension":"fidelity","score":%s,"reason":"fake judge score"},' "$score"
printf '{"dimension":"correctness","score":%s,"reason":"fake judge score"},' "$score"
printf '{"dimension":"maintainability","score":%s,"reason":"fake judge score"},' "$score"
printf '{"dimension":"discipline","score":%s,"reason":"fake judge score"},' "$score"
printf '{"dimension":"communication","score":%s,"reason":"fake judge score"}' "$score"
printf '],"compliance":['
first=1
for rule in $rules; do
  if [ "$first" -eq 0 ]; then
    printf ','
  fi
  first=0
  printf '{"rule_id":"%s","verdict":"%s","rationale":"%s"}' "$rule" "$verdict" "$rationale"
done
printf ']}\n'
`
}

func formatSleep(delay time.Duration) string {
	if delay <= 0 {
		return "0"
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.3f", delay.Seconds()), "0"), ".")
}
