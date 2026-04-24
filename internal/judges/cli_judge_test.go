package judges

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

func TestCLIJudgeCodexScoreOutputRecordsArgvAndPreservesOutputPath(t *testing.T) {
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "output.patch")
	rubricPath := filepath.Join(dir, "rubric.md")
	argvPath := filepath.Join(dir, "argv.txt")
	badOutputPath := filepath.Join(dir, "profile-output.json")
	require.NoError(t, os.WriteFile(outputPath, []byte("diff --git a/app/message.txt b/app/message.txt\n"), 0o644))
	require.NoError(t, os.WriteFile(rubricPath, []byte("# rubric\n"), 0o644))

	codexPath := filepath.Join(dir, "codex")
	require.NoError(t, os.WriteFile(codexPath, []byte(`#!/bin/sh
set -eu
argv_out="${FAKE_CODEX_ARGV_OUT}"
: > "$argv_out"
out=""
while [ "$#" -gt 0 ]; do
  printf '%s\n' "$1" >> "$argv_out"
  case "$1" in
    -o)
      out="$2"
      shift
      printf '%s\n' "$1" >> "$argv_out"
      ;;
  esac
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

	t.Setenv("FAKE_CODEX_ARGV_OUT", argvPath)
	judge := NewCLIJudge(agents.Profile{
		Provider: agents.ProviderCodex,
		Binary:   codexPath,
		Args:     []string{"--profile", "judge-ci", "--model", "gpt-5", "-o", badOutputPath},
	}, RolePrimary)
	output, err := judge.ScoreOutput(context.Background(), JudgeInput{
		RunID:      "2026-04-23-PR1-abcdef0",
		Pass:       1,
		Agent:      "a1",
		OutputPath: outputPath,
		RubricPath: rubricPath,
	})
	require.NoError(t, err)
	assert.Len(t, output.Scores, 5)
	assert.NoFileExists(t, badOutputPath)

	argvBytes, err := os.ReadFile(argvPath)
	require.NoError(t, err)
	argv := strings.Split(strings.TrimSpace(string(argvBytes)), "\n")
	assert.Equal(t, "exec", argv[0])
	assertArgValue(t, argv, "-C", dir)
	assertArgLastValueHasPrefix(t, argv, "-o", filepath.Join(os.TempDir(), "auto-improve-output-"))
	assert.Equal(t, "-", argv[len(argv)-1])
}

func TestCodexJudgeExecArgsAreReadOnlyAndKeepProfileArgs(t *testing.T) {
	args, err := codexJudgeExecArgs([]string{"--profile", "judge-ci", "--model", "gpt-5"}, "/tmp/worktree", "/tmp/output.json")
	require.NoError(t, err)

	assert.Equal(t, []string{
		"exec",
		"--sandbox", "read-only",
		"--skip-git-repo-check",
		"--ephemeral",
		"-C", "/tmp/worktree",
		"--profile", "judge-ci",
		"--model", "gpt-5",
		"-o", "/tmp/output.json",
		"-",
	}, args)
	assert.NotContains(t, args, "--full-auto")
	assert.NotContains(t, args, "--dangerously-bypass-approvals-and-sandbox")
}

func TestCodexJudgeExecArgsRejectUnsafeProfileArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "full auto", args: []string{"--full-auto"}},
		{name: "danger bypass", args: []string{"--dangerously-bypass-approvals-and-sandbox"}},
		{name: "workspace write sandbox", args: []string{"--sandbox", "workspace-write"}},
		{name: "danger sandbox equals", args: []string{"--sandbox=danger-full-access"}},
		{name: "short sandbox", args: []string{"-s", "workspace-write"}},
		{name: "sandbox config", args: []string{"-c", `sandbox_mode="danger-full-access"`}},
		{name: "approval config", args: []string{"--config=approval_policy=\"never\""}},
		{name: "shell env config", args: []string{"-c", "shell_environment_policy.inherit=all"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args, err := codexJudgeExecArgs(tt.args, "/tmp/worktree", "/tmp/output.json")
			require.Error(t, err)
			assert.Nil(t, args)
		})
	}
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

func assertArgValue(t *testing.T, argv []string, flag, want string) {
	t.Helper()
	for i := 0; i < len(argv)-1; i++ {
		if argv[i] == flag {
			assert.Equal(t, want, argv[i+1])
			return
		}
	}
	t.Fatalf("missing %s in argv: %v", flag, argv)
}

func assertArgLastValueHasPrefix(t *testing.T, argv []string, flag, prefix string) {
	t.Helper()
	got := ""
	for i := 0; i < len(argv)-1; i++ {
		if argv[i] == flag {
			got = argv[i+1]
		}
	}
	require.NotEmpty(t, got, "missing %s in argv: %v", flag, argv)
	assert.True(t, strings.HasPrefix(got, prefix), "last %s value %q does not have prefix %q", flag, got, prefix)
}

func TestRenderCLIJudgePromptPass2IncludesCandidateRuleBodies(t *testing.T) {
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "output.patch")
	rubricPath := filepath.Join(dir, "rubric.md")
	require.NoError(t, os.WriteFile(outputPath, []byte("diff --git a/app/message.txt b/app/message.txt\n"), 0o644))
	require.NoError(t, os.WriteFile(rubricPath, []byte("# rubric\n"), 0o644))

	prompt, err := renderCLIJudgePrompt(RolePrimary, JudgeInput{
		RunID:                     "2026-04-23-PR1-abcdef0",
		Pass:                      2,
		Agent:                     "a1",
		OutputPath:                outputPath,
		RubricPath:                rubricPath,
		ExpectedComplianceRuleIDs: []string{"active-rule", "cand-1"},
		CandidateRules: []CandidateRule{{
			ID:    "cand-1",
			Kind:  "new",
			Title: "Message details",
			Body:  "When app/message.txt changes, app/details.txt must also change.",
		}},
	})
	require.NoError(t, err)
	assert.Contains(t, prompt, "- active-rule")
	assert.Contains(t, prompt, "- cand-1")
	assert.Contains(t, prompt, "### cand-1")
	assert.Contains(t, prompt, "When app/message.txt changes")
}

func TestRenderCLIJudgePromptPass2SupportsStrictEmptyComplianceSet(t *testing.T) {
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "output.patch")
	rubricPath := filepath.Join(dir, "rubric.md")
	require.NoError(t, os.WriteFile(outputPath, []byte("diff --git a/app/message.txt b/app/message.txt\n"), 0o644))
	require.NoError(t, os.WriteFile(rubricPath, []byte("# rubric\n"), 0o644))

	prompt, err := renderCLIJudgePrompt(RoleArbiter, JudgeInput{
		RunID:                     "2026-04-23-PR1-abcdef0",
		Pass:                      2,
		Agent:                     "a1",
		OutputPath:                outputPath,
		RubricPath:                rubricPath,
		EnforceExpectedCompliance: true,
	})
	require.NoError(t, err)
	assert.Contains(t, prompt, "No compliance rows are expected")
	assert.NotContains(t, prompt, "No fixed compliance rule list was supplied")
	assert.NotContains(t, prompt, "stub-rubric-rule")
}

func mustNodePath(t *testing.T, binary string) string {
	t.Helper()
	path, _, err := agentrunner.PrepareProviderBinary(agents.ProviderClaude, binary)
	require.NoError(t, err)
	return path
}
