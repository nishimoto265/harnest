package judges

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nishimoto265/auto-improve/internal/agents"
	"github.com/nishimoto265/auto-improve/internal/steps/agentrunner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCLIJudgeCodexScoreOutput(t *testing.T) {
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "output.patch")
	rubricPath := filepath.Join(dir, "rubric.md")
	require.NoError(t, os.WriteFile(outputPath, []byte("diff --git a/app/message.txt b/app/message.txt\n"), 0o644))
	require.NoError(t, os.WriteFile(rubricPath, []byte("# rubric\n"), 0o644))
	codexPath := filepath.Join(dir, "codex")
	nodePath := filepath.Join(dir, "node")
	require.NoError(t, os.WriteFile(nodePath, []byte(`#!/bin/sh
set -eu
shift
out=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-o" ]; then
    out="$2"
    shift 2
    continue
  fi
  shift
done
cat > /dev/null
cat > "$out" <<'EOF'
{"scores":[
  {"dimension":"fidelity","score":80,"reason":"r1"},
  {"dimension":"correctness","score":81,"reason":"r2"},
  {"dimension":"maintainability","score":82,"reason":"r3"},
  {"dimension":"discipline","score":83,"reason":"r4"},
  {"dimension":"communication","score":84,"reason":"r5"}
],"compliance":[
  {"rule_id":"stub-rubric-rule","verdict":"compliant","rationale":"ok"}
]}
EOF
`), 0o755))
	require.NoError(t, os.WriteFile(codexPath, []byte("#!/usr/bin/env node\n"), 0o755))

	judge := NewCLIJudge(agents.Profile{Provider: agents.ProviderCodex, Binary: codexPath}, RolePrimary)
	output, err := judge.ScoreOutput(context.Background(), JudgeInput{
		RunID:      "2026-04-23-PR1-abcdef0",
		Pass:       1,
		Agent:      "a1",
		OutputPath: outputPath,
		RubricPath: rubricPath,
	})
	require.NoError(t, err)
	require.NoError(t, output.ValidateFor(JudgeInput{
		RunID:      "2026-04-23-PR1-abcdef0",
		Pass:       1,
		Agent:      "a1",
		OutputPath: outputPath,
		RubricPath: rubricPath,
	}))
	assert.Len(t, output.Scores, 5)
	assert.Len(t, output.Compliance, 1)
}

func TestCLIJudgeClaudeScoreOutput(t *testing.T) {
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "output.patch")
	rubricPath := filepath.Join(dir, "rubric.md")
	require.NoError(t, os.WriteFile(outputPath, []byte("diff --git a/app/message.txt b/app/message.txt\n"), 0o644))
	require.NoError(t, os.WriteFile(rubricPath, []byte("# rubric\n"), 0o644))
	claudePath := filepath.Join(dir, "claude")
	nodePath := filepath.Join(dir, "node")
	require.NoError(t, os.WriteFile(nodePath, []byte(`#!/bin/sh
cat > /dev/null
cat <<'EOF'
{"type":"result","subtype":"success","result":"{\"scores\":[{\"dimension\":\"fidelity\",\"score\":80,\"reason\":\"r1\"},{\"dimension\":\"correctness\",\"score\":81,\"reason\":\"r2\"},{\"dimension\":\"maintainability\",\"score\":82,\"reason\":\"r3\"},{\"dimension\":\"discipline\",\"score\":83,\"reason\":\"r4\"},{\"dimension\":\"communication\",\"score\":84,\"reason\":\"r5\"}],\"compliance\":[{\"rule_id\":\"stub-rubric-rule\",\"verdict\":\"compliant\",\"rationale\":\"ok\"}]}"}
EOF
`), 0o755))
	require.NoError(t, os.WriteFile(claudePath, []byte("#!/usr/bin/env node\n"), 0o755))

	judge := NewCLIJudge(agents.Profile{Provider: agents.ProviderClaude, Binary: claudePath}, RolePrimary)
	output, err := judge.ScoreOutput(context.Background(), JudgeInput{
		RunID:      "2026-04-23-PR1-abcdef0",
		Pass:       1,
		Agent:      "a1",
		OutputPath: outputPath,
		RubricPath: rubricPath,
	})
	require.NoError(t, err)
	assert.Len(t, output.Scores, 5)
	assert.Len(t, output.Compliance, 1)
	assert.Equal(t, nodePath, mustNodePath(t, claudePath))
}

func mustNodePath(t *testing.T, binary string) string {
	t.Helper()
	path, _, err := agentrunner.PrepareProviderBinary(agents.ProviderClaude, binary)
	require.NoError(t, err)
	return path
}
