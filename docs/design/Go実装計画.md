# Go実装計画

このファイルは、Go 再実装の rollout 中に使っていた **作業用の Phase 計画書** の案内ページです。
rev34 時点の実体は archive に退避しました。

- archive: `docs/design/archive/Go実装計画.rev34.md`
- 現在の構造説明: `docs/design/全体設計.md`
- field-level contract / state machine: `docs/design/io-contracts.md`

## 現在地

- `Phase 1`: step10/20/30/40/50/60/70 + archive/recover 実装済み
- `Phase 1.5`: `agentrunner` / `scorecore` 抽出済み
- `Phase 2`: orchestrator cycle / golden fixture 実装済み
- `Phase 3`: Actions / launchd / release/install 実装済み
- `Phase 4`: docs finalization 進行中

実装の正本は `cmd/harnest/**`, `internal/**`, `scripts/**`, `.github/workflows/**` です。
履歴として Phase 分割や review 反映の経緯を追いたい場合だけ archive を参照してください。
