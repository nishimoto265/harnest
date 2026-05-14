package judges

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nishimoto265/harnest/internal/agents"
	"github.com/nishimoto265/harnest/internal/steps/agentrunner"
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
],"issues":[
  {"severity":"high","category":"routing","title":"Proxy helper mixing","evidence":"proxy.ts mixes 404 matching with request mutation.","proposed_lesson":"Separate proxy matching helpers from mutation logic.","checklist_item":"Keep proxy matching helpers separate from mutation logic."}
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
	require.Len(t, output.Issues, 1)
	assert.Equal(t, "routing", output.Issues[0].Category)
	assert.Equal(t, "Proxy helper mixing", output.Issues[0].Title)
}

func TestCLIJudgeCodexScoreOutputRecordsArgvAndUsesManagedOutputPath(t *testing.T) {
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "output.patch")
	rubricPath := filepath.Join(dir, "rubric.md")
	argvPath := filepath.Join(dir, "argv.txt")
	promptPath := filepath.Join(dir, "prompt.txt")
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
cat > "${FAKE_CODEX_PROMPT_OUT}"
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
	t.Setenv("FAKE_CODEX_PROMPT_OUT", promptPath)
	judge := NewCLIJudge(agents.Profile{
		Provider: agents.ProviderCodex,
		Binary:   codexPath,
		Args:     []string{"--model", "gpt-5"},
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

	argvBytes, err := os.ReadFile(argvPath)
	require.NoError(t, err)
	argv := strings.Split(strings.TrimSpace(string(argvBytes)), "\n")
	assert.Equal(t, "exec", argv[0])
	workdir := argValue(t, argv, "-C")
	assert.NotEqual(t, dir, workdir)
	assert.Contains(t, filepath.Base(workdir), "auto-improve-judge-workdir-")
	assertArgValue(t, argv, "-c", `web_search="disabled"`)
	assertArgLastValueHasPrefix(t, argv, "-o", filepath.Join(os.TempDir(), "auto-improve-output-"))
	assert.Equal(t, "-", argv[len(argv)-1])

	promptBytes, err := os.ReadFile(promptPath)
	require.NoError(t, err)
	prompt := string(promptBytes)
	assert.Contains(t, prompt, filepath.Join(workdir, "output.patch"))
	assert.Contains(t, prompt, filepath.Join(workdir, "rubric.md"))
	assert.NotContains(t, prompt, outputPath)
	assert.NotContains(t, prompt, rubricPath)
}

func TestCLIJudgeFiltersUnexpectedComplianceRowsForExpectedSet(t *testing.T) {
	judge := cliJudge{
		role:    RoleArbiter,
		profile: agents.Profile{Provider: agents.ProviderClaude, Binary: "claude"},
		now:     func() time.Time { return time.Unix(0, 0).UTC() },
	}
	input := JudgeInput{
		RunID:                     "2026-04-23-PR1-abcdef0",
		Pass:                      1,
		Agent:                     "a1",
		OutputPath:                "/tmp/output.patch",
		RubricPath:                "/tmp/rubric.md",
		ExpectedComplianceRuleIDs: []string{"rule-b"},
		EnforceExpectedCompliance: true,
	}

	output, err := judge.toJudgeOutput(input, modelJudgeResponse{
		Scores: []modelJudgeScore{
			{Dimension: "fidelity", Score: 80, Reason: "r1"},
			{Dimension: "correctness", Score: 81, Reason: "r2"},
			{Dimension: "maintainability", Score: 82, Reason: "r3"},
			{Dimension: "discipline", Score: 83, Reason: "r4"},
			{Dimension: "communication", Score: 84, Reason: "r5"},
		},
		Compliance: []modelJudgeCompliance{
			{RuleID: "rule-a", Verdict: "compliant", Rationale: "extra"},
			{RuleID: "rule-b", Verdict: "violated", Rationale: "expected"},
		},
	})

	require.NoError(t, err)
	require.Len(t, output.Compliance, 1)
	assert.Equal(t, "rule-b", output.Compliance[0].RuleID)
	assert.Equal(t, "expected", output.Compliance[0].Rationale)
}

func TestCLIJudgeMarksMissingExpectedComplianceRowsAsMissed(t *testing.T) {
	judge := cliJudge{
		role:    RolePrimary,
		profile: agents.Profile{Provider: agents.ProviderCodex, Binary: "codex"},
		now:     func() time.Time { return time.Unix(0, 0).UTC() },
	}
	input := JudgeInput{
		RunID:                     "2026-04-23-PR1-abcdef0",
		Pass:                      1,
		Agent:                     "a1",
		OutputPath:                "/tmp/output.patch",
		RubricPath:                "/tmp/rubric.md",
		ExpectedComplianceRuleIDs: []string{"rule-a", "rule-b"},
		EnforceExpectedCompliance: true,
	}

	output, err := judge.toJudgeOutput(input, modelJudgeResponse{
		Scores: []modelJudgeScore{
			{Dimension: "fidelity", Score: 80, Reason: "r1"},
			{Dimension: "correctness", Score: 81, Reason: "r2"},
			{Dimension: "maintainability", Score: 82, Reason: "r3"},
			{Dimension: "discipline", Score: 83, Reason: "r4"},
			{Dimension: "communication", Score: 84, Reason: "r5"},
		},
		Compliance: []modelJudgeCompliance{
			{RuleID: "rule-a", Verdict: "compliant", Rationale: "expected"},
		},
	})

	require.NoError(t, err)
	require.Len(t, output.Compliance, 2)
	assert.Equal(t, "rule-a", output.Compliance[0].RuleID)
	assert.Equal(t, "rule-b", output.Compliance[1].RuleID)
	assert.Equal(t, "missed", string(output.Compliance[1].Verdict))
	assert.Contains(t, output.Compliance[1].Rationale, "omitted")
}

func TestCLIJudgeTruncatesLongFreeTextFields(t *testing.T) {
	judge := cliJudge{
		role:    RolePrimary,
		profile: agents.Profile{Provider: agents.ProviderClaude, Binary: "claude"},
		now:     func() time.Time { return time.Unix(0, 0).UTC() },
	}
	input := JudgeInput{
		RunID:                     "2026-04-23-PR1-abcdef0",
		Pass:                      1,
		Agent:                     "a1",
		OutputPath:                "/tmp/output.patch",
		RubricPath:                "/tmp/rubric.md",
		ExpectedComplianceRuleIDs: []string{"rule-a"},
		EnforceExpectedCompliance: true,
	}

	output, err := judge.toJudgeOutput(input, modelJudgeResponse{
		Scores: []modelJudgeScore{
			{Dimension: "fidelity", Score: 80, Reason: strings.Repeat("a", 1200)},
			{Dimension: "correctness", Score: 81, Reason: "r2"},
			{Dimension: "maintainability", Score: 82, Reason: "r3"},
			{Dimension: "discipline", Score: 83, Reason: "r4"},
			{Dimension: "communication", Score: 84, Reason: "r5"},
		},
		Compliance: []modelJudgeCompliance{
			{RuleID: "rule-a", Verdict: "violated", Rationale: strings.Repeat("b", 700)},
		},
	})

	require.NoError(t, err)
	assert.Len(t, []rune(output.Scores[0].Reasons), 1000)
	require.Len(t, output.Compliance, 1)
	assert.Len(t, []rune(output.Compliance[0].Rationale), 500)
}

func TestCLIJudgeCodexFailsClosedOnNonZeroExitEvenWithOutput(t *testing.T) {
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "output.patch")
	rubricPath := filepath.Join(dir, "rubric.md")
	require.NoError(t, os.WriteFile(outputPath, []byte("diff --git a/app/message.txt b/app/message.txt\n"), 0o644))
	require.NoError(t, os.WriteFile(rubricPath, []byte("# rubric\n"), 0o644))

	codexPath := filepath.Join(dir, "codex")
	require.NoError(t, os.WriteFile(codexPath, []byte(`#!/bin/sh
set -eu
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
  {"dimension":"fidelity","score":100,"reason":"stale"},
  {"dimension":"correctness","score":100,"reason":"stale"},
  {"dimension":"maintainability","score":100,"reason":"stale"},
  {"dimension":"discipline","score":100,"reason":"stale"},
  {"dimension":"communication","score":100,"reason":"stale"}
],"compliance":[
  {"rule_id":"stub-rubric-rule","verdict":"compliant","rationale":"stale"}
]}
EOF
echo "judge failed" >&2
exit 42
`), 0o755))

	judge := NewCLIJudge(agents.Profile{Provider: agents.ProviderCodex, Binary: codexPath}, RolePrimary)
	_, err := judge.ScoreOutput(context.Background(), JudgeInput{
		RunID:      "2026-04-23-PR1-abcdef0",
		Pass:       1,
		Agent:      "a1",
		OutputPath: outputPath,
		RubricPath: rubricPath,
	})
	require.Error(t, err)
	assert.ErrorContains(t, err, "exited with code 42")
	assert.ErrorContains(t, err, "judge failed")
}

func TestValidateCLIJudgeCommandResultFailsClosed(t *testing.T) {
	cleanupErr := errors.New("cleanup failed")
	tests := []struct {
		name    string
		result  agentrunner.CommandResult
		wantErr string
	}{
		{
			name: "timeout",
			result: agentrunner.CommandResult{
				TimedOut:      true,
				StdoutSnippet: []byte("partial stdout\n"),
				StderrSnippet: []byte("partial stderr\n"),
			},
			wantErr: "timed out",
		},
		{
			name: "nonzero",
			result: agentrunner.CommandResult{
				ExitCode:      7,
				StdoutSnippet: []byte("valid-looking output\n"),
				StderrSnippet: []byte("failed\n"),
			},
			wantErr: "exited with code 7",
		},
		{
			name: "cleanup",
			result: agentrunner.CommandResult{
				CleanupErr: cleanupErr,
			},
			wantErr: "cleanup failed",
		},
		{
			name:   "success",
			result: agentrunner.CommandResult{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCLIJudgeCommandResult(tt.result)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestCodexJudgeExecArgsAreReadOnlyAndKeepSafeProfileArgs(t *testing.T) {
	dir := t.TempDir()
	codexPath := filepath.Join(dir, "codex")
	require.NoError(t, os.WriteFile(codexPath, []byte("#!/bin/sh\nexit 0\n"), 0o755))

	command, err := agentrunner.PrepareReadOnlyCommand(agents.Profile{
		Provider: agents.ProviderCodex,
		Binary:   codexPath,
		Args:     []string{"--model", "gpt-5", "--model=gpt-5-mini", "-m", "gpt-5"},
	}, "/tmp/worktree")
	require.NoError(t, err)
	defer command.Cleanup()
	defer os.Remove(command.ResponsePath)

	assert.Equal(t, []string{
		"exec",
		"--sandbox", "read-only",
		"--skip-git-repo-check",
		"--ephemeral",
		"-c", `web_search="disabled"`,
		"-C", "/tmp/worktree",
		"--model", "gpt-5",
		"--model=gpt-5-mini",
		"-m", "gpt-5",
		"-o", command.ResponsePath,
		"-",
	}, command.Args)
	assert.Equal(t, codexPath, command.Binary)
	assert.Equal(t, "/tmp/worktree", command.Workdir)
	assert.NotContains(t, command.Args, "--full-auto")
	assert.NotContains(t, command.Args, "--dangerously-bypass-approvals-and-sandbox")
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
		{name: "model config bypass", args: []string{"-c", `model="gpt-5"`}},
		{name: "approval config", args: []string{"--config=approval_policy=\"never\""}},
		{name: "shell env config", args: []string{"-c", "shell_environment_policy.inherit=all"}},
		{name: "profile config", args: []string{"-c", "profile=\"judge-ci\""}},
		{name: "mcp config override", args: []string{"-c", "mcp_servers.local.command=\"writer\""}},
		{name: "profile", args: []string{"--profile", "judge-ci"}},
		{name: "profile equals", args: []string{"--profile=judge-ci"}},
		{name: "cwd", args: []string{"-C", "/tmp/other"}},
		{name: "cd", args: []string{"--cd", "/tmp/other"}},
		{name: "cd equals", args: []string{"--cd=/tmp/other"}},
		{name: "add dir", args: []string{"--add-dir", "/tmp/other"}},
		{name: "output", args: []string{"-o", "/tmp/other.json"}},
		{name: "output equals", args: []string{"-o=/tmp/other.json"}},
		{name: "last message output", args: []string{"--output-last-message", "/tmp/other.json"}},
		{name: "last message output equals", args: []string{"--output-last-message=/tmp/other.json"}},
		{name: "ignore rules", args: []string{"--ignore-rules"}},
		{name: "ignore user config", args: []string{"--ignore-user-config"}},
		{name: "model consumes flag-like value", args: []string{"--model", "--ignore-rules"}},
		{name: "model equals empty", args: []string{"--model="}},
		{name: "unknown passthrough", args: []string{"--json"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			codexPath := filepath.Join(dir, "codex")
			require.NoError(t, os.WriteFile(codexPath, []byte("#!/bin/sh\nexit 0\n"), 0o755))

			command, err := agentrunner.PrepareReadOnlyCommand(agents.Profile{
				Provider: agents.ProviderCodex,
				Binary:   codexPath,
				Args:     tt.args,
			}, "/tmp/worktree")
			require.Error(t, err)
			assert.Empty(t, command.Args)
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

func TestClaudeJudgeExecArgsUseReadOnlyToolsAndKeepSafeProfileArgs(t *testing.T) {
	dir := t.TempDir()
	claudePath := filepath.Join(dir, "claude")
	require.NoError(t, os.WriteFile(claudePath, []byte("#!/bin/sh\nexit 0\n"), 0o755))

	command, err := agentrunner.PrepareReadOnlyCommand(agents.Profile{
		Provider: agents.ProviderClaude,
		Binary:   claudePath,
		Args:     []string{"--model", "claude-3-5-sonnet"},
	}, "/tmp/worktree")
	require.NoError(t, err)
	defer command.Cleanup()
	defer os.Remove(command.ResponsePath)

	assert.Equal(t, []string{
		"-p",
		"--output-format", "json",
		"--allowedTools", "Read",
		"--model", "claude-3-5-sonnet",
	}, command.Args)
	assert.Equal(t, claudePath, command.Binary)
	assert.Equal(t, "/tmp/worktree", command.Workdir)
}

func TestClaudeJudgeExecArgsRejectUnsafeProfileArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "allowed tools", args: []string{"--allowedTools", "Read,Bash"}},
		{name: "allowed tools equals", args: []string{"--allowedTools=Read,Bash"}},
		{name: "kebab allowed tools", args: []string{"--allowed-tools", "Bash"}},
		{name: "disallowed tools override", args: []string{"--disallowedTools", ""}},
		{name: "output format", args: []string{"--output-format", "text"}},
		{name: "output format equals", args: []string{"--output-format=text"}},
		{name: "cwd", args: []string{"--cwd", "/tmp/other"}},
		{name: "cwd equals", args: []string{"--cwd=/tmp/other"}},
		{name: "add dir", args: []string{"--add-dir", "/tmp/other"}},
		{name: "permission mode", args: []string{"--permission-mode", "bypassPermissions"}},
		{name: "permission mode equals", args: []string{"--permission-mode=bypassPermissions"}},
		{name: "danger bypass", args: []string{"--dangerously-skip-permissions"}},
		{name: "permission prompt tool", args: []string{"--permission-prompt-tool", "mcp__grant"}},
		{name: "mcp config", args: []string{"--mcp-config", "/tmp/mcp.json"}},
		{name: "settings", args: []string{"--settings", "/tmp/settings.json"}},
		{name: "profile", args: []string{"--profile", "judge-ci"}},
		{name: "profile equals", args: []string{"--profile=judge-ci"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			claudePath := filepath.Join(dir, "claude")
			require.NoError(t, os.WriteFile(claudePath, []byte("#!/bin/sh\nexit 0\n"), 0o755))

			command, err := agentrunner.PrepareReadOnlyCommand(agents.Profile{
				Provider: agents.ProviderClaude,
				Binary:   claudePath,
				Args:     tt.args,
			}, "/tmp/worktree")
			require.Error(t, err)
			assert.Empty(t, command.Args)
		})
	}
}

func TestClaudeJudgeExecArgsRejectSafeArgMissingValue(t *testing.T) {
	dir := t.TempDir()
	claudePath := filepath.Join(dir, "claude")
	require.NoError(t, os.WriteFile(claudePath, []byte("#!/bin/sh\nexit 0\n"), 0o755))

	command, err := agentrunner.PrepareReadOnlyCommand(agents.Profile{
		Provider: agents.ProviderClaude,
		Binary:   claudePath,
		Args:     []string{"--model"},
	}, "/tmp/worktree")

	require.Error(t, err)
	assert.Empty(t, command.Args)
}

func assertArgValue(t *testing.T, argv []string, flag, want string) {
	t.Helper()
	assert.Equal(t, want, argValue(t, argv, flag))
}

func argValue(t *testing.T, argv []string, flag string) string {
	t.Helper()
	for i := 0; i < len(argv)-1; i++ {
		if argv[i] == flag {
			return argv[i+1]
		}
	}
	t.Fatalf("missing %s in argv: %v", flag, argv)
	return ""
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
