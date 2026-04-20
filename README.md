# auto-improve

Self-improving harness pipeline for AI coding agents.
Observes merged PRs, replays each under the current "best" rule set, scores the
output, proposes new rules, verifies them in a second pass, and promotes wins.

> ⚠️ **Status (2026-04-20)**: Planning phase. TypeScript M1 skeleton is being
> deleted; re-implementing in **Go** from scratch. No commands are runnable yet.

## 📚 Canonical docs (read in this order)

1. **`docs/design/Go実装計画.md`** — phase breakdown, ownership matrix, rollout plan (rev11+)
2. **`docs/design/io-contracts.md`** — exact schemas, step70 staged transaction, recovery state machine, resume vocabulary, recover CLI (rev11+)
3. **`docs/design/全体設計.md`** — background / 骨格思想 only (rev6 以降未同期、Phase 4 で rewrite)

Old migration-plan memos (`docs/memos/`) are history/参考 only. The two files above are canonical.

## Planned runtime (will land post-Phase 0)

- Go 1.22 binary: `auto-improve {preflight, detect-merged, run, sunset, recover}`
- macOS launchd (hourly tick) or `workflow_dispatch` on GitHub Actions
- External CLI dependencies: `git >= 2.35`, `gh >= 2.40`, `jq >= 1.6`, `yq >= 4.0`, `claude`, `codex`
- Platform: darwin/arm64, darwin/amd64, linux/amd64

### Recover after `needs_manual_recovery`

launchd は `StartInterval: 3600` で hourly tick のため、operator が sentinel を `recover` した後 **最大 1 時間** pipeline 停止することがある。即時復旧したい場合は以下いずれか:
- `auto-improve run --pr <n>` で該当 PR を手動 trigger
- `auto-improve run --detect-loop --with-preflight` で detect ループを手動起動
- `launchctl start com.nishimoto265.auto-improve` で launchd の次 tick を前倒し

`auto-improve recover --inspect --run <id>` は read-only で state を confirm でき、副作用なしに診断可能。

Setup, config, and run-book will be filled in at Phase 3-C (release) and Phase 4 (docs finalization).
