# HarNest

Self-improving harness pipeline for AI coding agents.
Observes merged PRs, replays each under the current "best" rule set, scores the
output, proposes new rules, verifies them in a second pass, and promotes wins.

> ⚠️ **Status (2026-04-23)**: Go 版の core 実装と Phase 3 infra までは
> 収束済みです。ローカル test / static validation は通っていますが、
> live の self-hosted Actions、実 release publish、実 macOS `launchctl`
> reload まではまだこの workspace では実行していません。

## 📚 Canonical docs (read in this order)

1. **`docs/design/io-contracts.md`** — exact schemas, step70 staged transaction, recovery state machine, resume vocabulary, recover CLI
2. **`docs/design/全体設計.md`** — Go 実装の全体像、durable artifact、step 関係
3. **`docs/design/Go実装計画.md`** — archived rollout plan への案内

Old migration-plan memos (`docs/memos/`) are history/参考 only.

## Runtime

- Go 1.23+ binary. Release and CI builds use patched Go 1.26.3: `harnest <repo-url>` or `harnest {preflight, detect-merged, run, sunset, recover}`
- macOS launchd (hourly tick) or `workflow_dispatch` on GitHub Actions
- External CLI dependencies: `git >= 2.35`, `gh >= 2.40`, `jq >= 1.6`, `yq >= 4.0`, `lsof`, `claude`, `codex`
- Platform: darwin/arm64, darwin/amd64, linux/amd64

## Runtime dependencies

`harnest preflight` validates the local runtime before `run --with-preflight`
or the installer arms launchd. Make sure these are installed and working first:

- `git >= 2.35`
- `gh >= 2.40` with `gh auth status` succeeding
- `curl`
- `jq >= 1.6`
- `yq >= 4.0`
- `lsof`
- `claude`
- `codex`

Copy `config.yaml.example` to `config.yaml`, and if you want per-role provider
selection copy `agents.yaml.example` to `agents.yaml`. Keep `paths.runs` and
`worktree.base` durable absolute paths, and run install commands from the
repository root unless you set `REPO_ROOT=/abs/path/to/repo`. The older
top-level `runs_base` / `worktree_base` keys are still accepted as aliases, but
the nested form is the canonical schema now. For operational detect/run flows,
fill in the `repo.github`, `repo.root`, `repo.default_branch`, and
`repo.best_branch` fields too. If you want adopted rules to persist in the
target repository instead of only under the local `runs` cache, also configure
`repo.policy_branch`. The branch may already exist; if it does not, the first
adopt can bootstrap it from the run's policy snapshot.
Subprocess command names are resolved against the fixed trusted runtime PATH
(`/usr/bin:/bin:/usr/sbin:/sbin:/usr/local/bin:/opt/homebrew/bin`), not the
caller shell's ambient `PATH`; use absolute paths for `claude` / `codex` if
they are installed elsewhere. If a provider binary is a Node shebang wrapper and
must run with a specific Node runtime, set `node_binary` on that profile.
`task_prompt.source` controls the shared task brief used by pass1/pass2. Use
`auto` (default) to ask the optional `task_generator` agent to reconstruct an
issue-like task description from PR title/body, linked issues, changed files,
changed tests, and diff evidence. Use `issue` to force a usable linked issue as
the task prompt; if no usable issue exists, it falls back to `auto`. Configure
`roles.task_generator` in `agents.yaml` to use a Claude/Codex profile for this
generation step. Without that role, step10 falls back to a deterministic
best-effort task brief; transient generator failures also fall back to that
deterministic path unless the run context itself was canceled. The source
boundary is kept small so future providers such as Asana can feed the same
`auto` generation path.

The repo URL entrypoint (`harnest <repo-url>`) can bootstrap a target
repository without a checked-in local `config.yaml`. It clones or reuses the
target under `~/.auto-improve/repos/<owner>/<repo>`, stores per-repository
state under `~/.auto-improve/runs/<owner>__<repo>/`, and records registrations
in `~/.auto-improve/repositories.yaml`. Set `AUTO_IMPROVE_HOME` to relocate
that user-scope state. If a local `config.yaml` exists in the current working
directory, its provider/scoring settings are used as the base and the target
repo/path fields are overlaid from the URL.

`agents.yaml` controls which runtime provider each role uses. Implementer roles
can use `claude` or `codex`; judge roles can stay `stub` or use CLI-backed
`claude` / `codex` profiles. Provider-specific `args` are appended to the
built-in invocation, but judge args reject overrides for cwd, output paths,
profiles, permission modes, MCP/settings, and unsafe sandbox/config changes.
The example Claude profile includes `-p` so runs are non-interactive. Codex
implementers default to
`codex exec --full-auto --skip-git-repo-check -C <worktree>`, while Codex judges
run with `codex exec --sandbox read-only --skip-git-repo-check --ephemeral`.
The dangerous `--dangerously-bypass-approvals-and-sandbox` mode is never
injected by default and must be an explicit implementer profile `args` opt-in if
an externally sandboxed environment requires it.
Absolute scoring in step30 and step60 uses the single `judge_primary` profile.
Extra legacy `judge_secondary` / `judge_arbiter` role keys in old `agents.yaml`
files are ignored by the normal pipeline. `scoring.pairwise_mode` controls
step60's true pairwise judge fanout. Use
`single` for one final judge over all pass1/pass2 pairs, `basic` for one
same-agent comparison per pair plus a final decision judge, or `strict` for
AB/BA order reversal per pair plus a final decision judge. Build, test, and
lint failures are judge evidence, not an automatic zero-score gate; the final
decision judge can still override comparison votes when it identifies a fatal
issue.

## Commands

- `harnest <repo-url>`
  Register/bootstrap a GitHub repository URL under `~/.auto-improve` and run
  continuously. Repository state is namespaced per `owner/repo`.
- `harnest <repo-url> --limit <n>`
  Process at most `<n>` selected merged PRs and exit.
- `harnest <repo-url> --pr <n[,m...]>`
  Process one or more comma-separated PR numbers and exit.
- `harnest <repo-url> --dry-run`
  Resolve the repository, candidate PRs, selected PRs, state paths, and skip
  reasons without running the pipeline. Docs-only PRs are skipped by default.
- `harnest preflight`
  Local runtime, writable state path, repo settings, and `best_branch` reachability gate.
  `policy_branch` is checked for config conflicts only; a missing remote policy
  branch is allowed so first-run bootstrap can create it.
- `harnest detect-merged`
  `repo.default_branch` 向けの merged PR を列挙する。
- `harnest run --pr <n> --with-preflight`
  1 PR 分の pipeline を実行する。
- `harnest run --pr <n> --from-scratch`
  既存の non-terminal run を `superseded_by_from_scratch` として閉じ、worktree を prune して新規 run で再実行する。
- `harnest run --detect-loop --with-preflight`
  未処理 merged PR を順に実行する。
- `harnest clear <repo-url>`
  指定 repo の clone / runs / worktrees / active registration を
  `~/.auto-improve/archives/cleared/<timestamp>/...` に退避し、次回実行で
  clean bootstrap できる状態にする。実リモートや policy branch は変更しない。
- `harnest clear --all`
  `AUTO_IMPROVE_HOME` 配下の generated state 全体を archive に退避する。
- `harnest sunset`
  archived/deprecated lifecycle の sunset flow を手動実行する。
- `harnest recover ...`
  step70 / sentinel / cleanup の recover flow を実行する。
- `harnest lessons new <id> --checklist-item <text>`
  `.auto-improve/lessons/<id>.md` の lesson skeleton を作成する。
- `harnest lessons generate-checklist [--check]`
  active lessons から `.auto-improve/checklist.md` を生成、または stale か確認する。
- `harnest lessons prepare-checklist-result [--force]`
  `.auto-improve/checklist.md` を `.auto-improve/work/checklist-result.md` にコピーする。
- `harnest lessons verify-checklist-result`
  作業用 checklist result が `[x]` / `[-]` / `[!]` で解決済みか確認する。
- `harnest lessons install-guidance [--provider claude,codex]`
  `CLAUDE.md` / `AGENTS.md` / provider hook 設定を managed block 方式で追加する。

## Install

Install via Homebrew after a release has been published:

```bash
brew tap nishimoto265/homebrew-tap
brew install --cask harnest
```

The release workflow publishes GitHub Release assets named
`harnest_darwin_arm64`, `harnest_darwin_amd64`, `harnest_linux_amd64`, and
`checksums.txt`, then updates the `nishimoto265/homebrew-tap` cask. The release
repository needs a `HOMEBREW_TAP_GITHUB_TOKEN` secret with write access to that
tap.

To install the released binary directly into `/usr/local/bin` and configure
launchd on macOS:

```bash
make install
```

If `/usr/local/bin` is not writable, use `sudo make install` or install without
sudo into a user-owned directory:

```bash
INSTALL_DIR="$HOME/.local/bin" make install
```

`scripts/install.sh` performs a staged install in `INSTALL_DIR`, runs
`preflight` against the staged binary before swapping it into place, and rolls
back the binary/plist if the swap or launchd reload fails. On macOS the
generated plist uses the repository root as `WorkingDirectory` so the default
`config.yaml` lookup continues to work. On Linux, the installer only installs
the binary; scheduling remains manual or via GitHub Actions.

launchd labels and plist names are per instance:
`com.nishimoto265.auto-improve.<instance>`. Set
`AUTO_IMPROVE_INSTANCE=owner/repo` when installing or uninstalling multiple
repositories on the same machine. If omitted, the scripts derive a sanitized
instance from `REPO_ROOT` or the current directory so separate repository roots
do not share the same LaunchAgent.

The installer downloads from GitHub Releases `latest`. Until the first release
exists, `make install` needs either a published release or explicit overrides
such as `HARNEST_RELEASE_URL` and `HARNEST_EXPECTED_SHA256`.

`make release` は local publish を既定で拒否します。通常は GitHub release
workflow を使ってください。手元から明示的に publish する場合だけ
`ALLOW_LOCAL_RELEASE=1 make release` を使います。

## GitHub Actions Prerequisites

`workflow_dispatch` assumes a dedicated self-hosted runner labeled
`auto-improve`. That runner must already have `git`, `gh`, `jq`, `yq`, `lsof`,
`claude`, and `codex` installed and authenticated, and it must keep a durable
runner-local state directory so `paths.runs` and `worktree.base` persist across
invocations.

### Recover after `needs_manual_recovery`

launchd は `StartInterval: 3600` で hourly tick のため、operator が sentinel を `recover` した後 **最大 1 時間** pipeline 停止することがある。即時復旧したい場合は以下いずれか:
- `harnest run --pr <n>` で該当 PR を手動 trigger
- `harnest run --detect-loop --with-preflight` で detect ループを手動起動
- `launchctl start com.nishimoto265.auto-improve.<instance>` で launchd の次 tick を前倒し

`harnest recover --inspect --run <id>` は read-only で state を confirm でき、副作用なしに診断可能。

Manual CLI commands load `config.yaml` from the current working directory, so
run them from the repository root unless you have arranged the same config file
layout elsewhere.

## Git Hooks

Enable the repository hooks with `git config core.hooksPath .githooks`.
