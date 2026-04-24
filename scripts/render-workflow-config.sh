#!/usr/bin/env bash
set -euo pipefail

base_dir="${HOME}/.auto-improve"
runs_base="${INPUT_RUNS_BASE:-${base_dir}/runs}"
worktree_base="${INPUT_WORKTREE_BASE:-${base_dir}/worktrees}"
repo_github="${INPUT_REPO_GITHUB:-${GITHUB_REPOSITORY}}"
default_branch="${INPUT_DEFAULT_BRANCH:-${REPOSITORY_DEFAULT_BRANCH}}"
output_dir="${AUTO_IMPROVE_WORKFLOW_OUTPUT_DIR:-.}"

case "${runs_base}" in
  /*) ;;
  *) echo "runs_base must be an absolute path: ${runs_base}" >&2; exit 1 ;;
esac
case "${worktree_base}" in
  /*) ;;
  *) echo "worktree_base must be an absolute path: ${worktree_base}" >&2; exit 1 ;;
esac

mkdir -p "${runs_base}" "${worktree_base}" "${output_dir}"

yaml_quote() {
  local value="${1}"
  value="${value//\\/\\\\}"
  value="${value//\"/\\\"}"
  printf '"%s"' "${value}"
}

profile_for_role() {
  local role="${1}"
  local provider="${2}"
  case "${role}" in
    implementer) printf '%s-implementer' "${provider}" ;;
    judge_primary|judge_secondary|judge_arbiter) printf '%s-judge' "${provider}" ;;
    *) echo "unsupported role: ${role}" >&2; exit 1 ;;
  esac
}

{
  printf 'profiles:\n'
  printf '  claude-implementer:\n'
  printf '    provider: claude\n'
  printf '    binary: %s\n' "$(yaml_quote "${INPUT_CLAUDE_CLI_PATH}")"
  printf '    args: ["-p"]\n'
  printf '  claude-judge:\n'
  printf '    provider: claude\n'
  printf '    binary: %s\n' "$(yaml_quote "${INPUT_CLAUDE_CLI_PATH}")"
  printf '  codex-implementer:\n'
  printf '    provider: codex\n'
  printf '    binary: %s\n' "$(yaml_quote "${INPUT_CODEX_CLI_PATH}")"
  printf '  codex-judge:\n'
  printf '    provider: codex\n'
  printf '    binary: %s\n' "$(yaml_quote "${INPUT_CODEX_CLI_PATH}")"
  printf 'roles:\n'
  printf '  implementer: %s\n' "$(yaml_quote "$(profile_for_role implementer "${INPUT_IMPLEMENTER_PROVIDER}")")"
  printf '  judge_primary: %s\n' "$(yaml_quote "$(profile_for_role judge_primary "${INPUT_JUDGE_PRIMARY_PROVIDER}")")"
  printf '  judge_secondary: %s\n' "$(yaml_quote "$(profile_for_role judge_secondary "${INPUT_JUDGE_SECONDARY_PROVIDER}")")"
  printf '  judge_arbiter: %s\n' "$(yaml_quote "$(profile_for_role judge_arbiter "${INPUT_JUDGE_ARBITER_PROVIDER}")")"
} > "${output_dir}/agents.yaml"

{
  printf 'repo:\n'
  printf '  github: %s\n' "$(yaml_quote "${repo_github}")"
  printf '  root: %s\n' "$(yaml_quote "${GITHUB_WORKSPACE}")"
  printf '  default_branch: %s\n' "$(yaml_quote "${default_branch}")"
  printf '  best_branch: %s\n' "$(yaml_quote "${INPUT_BEST_BRANCH}")"
  printf 'paths:\n'
  printf '  runs: %s\n' "$(yaml_quote "${runs_base}")"
  printf 'worktree:\n'
  printf '  base: %s\n' "$(yaml_quote "${worktree_base}")"
  printf 'agent_config_path: "./agents.yaml"\n'
  printf 'claude_cli_path: %s\n' "$(yaml_quote "${INPUT_CLAUDE_CLI_PATH}")"
  printf 'codex_cli_path: %s\n' "$(yaml_quote "${INPUT_CODEX_CLI_PATH}")"
} > "${output_dir}/config.yaml"
