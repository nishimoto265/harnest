# auto-improve

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

- Go 1.22 binary: `auto-improve {preflight, detect-merged, run, sunset, recover}`
- macOS launchd (hourly tick) or `workflow_dispatch` on GitHub Actions
- External CLI dependencies: `git >= 2.35`, `gh >= 2.40`, `jq >= 1.6`, `yq >= 4.0`, `claude`, `codex`
- Platform: darwin/arm64, darwin/amd64, linux/amd64

## Runtime dependencies

`auto-improve preflight` validates the local runtime before `run --with-preflight`
or the installer arms launchd. Make sure these are installed and working first:

- `git >= 2.35`
- `gh >= 2.40` with `gh auth status` succeeding
- `curl`
- `jq >= 1.6`
- `yq >= 4.0`
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
`repo.policy_branch` and create that remote branch ahead of time.
`task_prompt.source` controls how step10 synthesizes the shared task brief for
pass1/pass2. The default `auto` mode prefers linked issues when present but
still keeps changed tests/files and a diff excerpt as supporting context.

`agents.yaml` controls which runtime provider each role uses. Today the
implementer role is provider-aware (`claude` or `codex`), while judge roles are
reserved for future non-stub judge wiring and can safely stay `stub`.

## Commands

- `auto-improve preflight`
  Local runtime, writable state path, repo settings, and `best_branch` reachability gate.
- `auto-improve detect-merged`
  `repo.default_branch` 向けの merged PR を列挙する。
- `auto-improve run --pr <n> --with-preflight`
  1 PR 分の pipeline を実行する。
- `auto-improve run --detect-loop --with-preflight`
  未処理 merged PR を順に実行する。
- `auto-improve sunset`
  archived/deprecated lifecycle の sunset flow を手動実行する。
- `auto-improve recover ...`
  step70 / sentinel / cleanup の recover flow を実行する。

## Install

Install the released binary into `/usr/local/bin` and configure launchd on
macOS:

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

The installer downloads from GitHub Releases `latest`. Until the first release
exists, `make install` needs either a published release or explicit overrides
such as `AUTO_IMPROVE_RELEASE_URL` and `AUTO_IMPROVE_EXPECTED_SHA256`.

`make release` は local publish を既定で拒否します。通常は GitHub release
workflow を使ってください。手元から明示的に publish する場合だけ
`ALLOW_LOCAL_RELEASE=1 make release` を使います。

## GitHub Actions Prerequisites

`workflow_dispatch` assumes a dedicated self-hosted runner labeled
`auto-improve`. That runner must already have `git`, `gh`, `jq`, `yq`,
`claude`, and `codex` installed and authenticated, and it must keep a durable
runner-local state directory so `paths.runs` and `worktree.base` persist across
invocations.

### Recover after `needs_manual_recovery`

launchd は `StartInterval: 3600` で hourly tick のため、operator が sentinel を `recover` した後 **最大 1 時間** pipeline 停止することがある。即時復旧したい場合は以下いずれか:
- `auto-improve run --pr <n>` で該当 PR を手動 trigger
- `auto-improve run --detect-loop --with-preflight` で detect ループを手動起動
- `launchctl start com.nishimoto265.auto-improve` で launchd の次 tick を前倒し

`auto-improve recover --inspect --run <id>` は read-only で state を confirm でき、副作用なしに診断可能。

Manual CLI commands load `config.yaml` from the current working directory, so
run them from the repository root unless you have arranged the same config file
layout elsewhere.

## Git Hooks

Enable the repository hooks with `git config core.hooksPath .githooks`.
