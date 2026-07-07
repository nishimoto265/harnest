# Managed Agent / 外部 trigger / 副作用層の構成整理

この文書では、本プロジェクトの実行構成を **3 層構造** として整理する:

| 層 | 担当 | 型 |
|---|---|---|
| ① **trigger 層** | Databricks Workflow（schedule + Job dependency）、GitHub Actions（PR event） | フロー型 |
| ② **agent 層** | Anthropic Managed Agents（`/v1/agents`、multiagent coordinator） | エージェント主導型 |
| ③ **副作用層** | GitHub Actions step / Databricks notebook が DB INSERT / Issue 作成 / PR push | フロー型 |

**設計原則: agent は副作用ゼロ**。LLM 推論が必要な工程（candidate 抽出 / review verdict / SKILL.md 整形）のみ agent 内で実行し、Databricks への書き込み / GitHub Issue 作成 / branch push 等の副作用は **すべて外側のフロー層が決定論的に行う**。これにより:

- agent が prompt injection されても外部書き込みは block される（権限ゼロ）
- 副作用は schema validate → SQL / API 呼び出しの決定論パスで実行され、retry / audit が容易
- 認証情報は GitHub Actions Secret / Databricks Secret に集約、Anthropic vault は最小限の用途に限定

Claude Code on the web Routines（claude.ai/code 側）、Claude Code SDK の自前 orchestration はこの文脈に含めない。

## 1. 用語

| 用語 | 本設計での意味 |
|---|---|
| Managed Agent | Anthropic API `/v1/agents` で作成する persisted, versioned config（model / system / multiagent） |
| Session | Managed Agent を起動した 1 回の実行単位（`/v1/sessions`）。agent 内で multiagent loop 完結 |
| Environment | Managed Agent session の container 設定。`cloud` を使用 |
| Trigger 層 | session を起動する外部システム（Databricks Workflow、GitHub Actions） |
| 副作用層 | agent 出力の JSON を schema validate して DB / GitHub に書き込む外側のフロー層 |
| Multiagent coordinator | main agent が roster の subagent（review）に native delegate できる仕組み |
| Subagent | coordinator から spawn される別 agent。本設計では rule-review-agent |
| Custom tool | host-side で実行する任意ロジック（本設計では未使用、agent は JSON 出力のみ） |
| MCP server | agent から外部システムへアクセスする拡張点（本設計では原則使用しない、後段で read-only として再評価） |

## 2. 公式機能として確認できること

| 要件 | 実現方法 | 確認状況 |
|---|---|---|
| Anthropic 側で agent loop / tool 実行を完結 | `/v1/sessions` 起動 | ✅ 実機確認済（main + review experimental） |
| Agent 定義を Console で管理 | `platform.claude.com/workspaces/default/agents` で agent CRUD | ✅ |
| Multiagent（main → review の自動 delegate） | `multiagent: {type: "coordinator", agents: [...]}` を agent.create() / update() で declare | ✅ 実機確認済（main v3 multiagent declare + revise loop が agent 内で 2 iter 完結） |
| 定期実行（trigger 層） | Databricks Workflow schedule（1h or daily） | ✅ Databricks 側機能 |
| データ着信検知（trigger 層） | Databricks Workflow Job dependency（OTel ingest 完了で発火）| ✅ Databricks 側機能 |
| GitHub event 起動（trigger 層） | GitHub Actions workflow `on: pull_request.closed` | ✅ |
| **副作用反映**（DB INSERT / Issue 作成 / PR push） | **GitHub Actions step / Databricks notebook step**（agent 内では行わない） | ✅ |
| Token / cost 監視 | session events.list の `span.model_request_end` + OTel `dev_bronze.s3_otel_logs.api_request` の `cost_usd` 列 | ✅ |
| 同時起動防止 | Databricks 側 `processed_prs` テーブルの compare-and-set | ✅（既存設計） |

## 3. できない / 使わないこと

| 項目 | 理由 | 方針 |
|---|---|---|
| Managed Agents 単体で trigger を持つ | API 仕様上、session は外部から POST する必要がある | trigger は Databricks Workflow / GitHub Actions に出す |
| agent 内から DB / Issue / PR push 等の副作用を出す | agent 出力は確率的（LLM）、副作用は決定論的にしたい。prompt injection 経路の遮断 | **副作用は外側フロー層**（GitHub Actions step / Databricks notebook step） |
| MCP server を agent に attach（write 系）| 副作用を agent に渡してしまう | 本設計では使用しない。read-only MCP は Phase 3 で再評価候補 |
| Claude Code Routines を主 runner にする | 別 product。Managed Agent と組み合わせると二重構造 | 本設計では使わない |
| Agent teams（experimental） | experimental、disabled by default | multiagent coordinator（GA）で main → review を構成 |
| agent コンテナに Databricks / GitHub token を直接置く | bash 実行や prompt injection で漏洩経路になる | そもそも agent は副作用ゼロ、token を持つ必要がない |

## 4. 採用する実行モデル

```
┌──────────────────────────────────────────────────────────────────────┐
│ ① Trigger 層（フロー型）                                              │
│                                                                      │
│  [Databricks Workflow]            [GitHub Actions]                   │
│   ├─ schedule (1h or daily)        └─ pull_request.closed            │
│   ├─ Job dependency:                   (head: claude/harness-        │
│   │   OTel Bronze ingest 完了           rules/*)                     │
│   ├─ Notebook が                                                     │
│   │  SELECT 未処理 PR                                                │
│   └─ POST /v1/sessions per PR                                        │
│                                                                      │
└──────────────────────────────────────────────────────────────────────┘
                              │ user.message に PR メタを渡す
                              ▼
┌──────────────────────────────────────────────────────────────────────┐
│ ② Agent 層（エージェント主導型、副作用ゼロ）                          │
│                                                                      │
│ [Managed Agent: rule-creation-agent (multiagent coordinator)]        │
│  ├─ model: claude-sonnet-4-6                                         │
│  ├─ tools: [] （MCP 接続なし、副作用権限なし）                       │
│  ├─ multiagent: { coordinator, agents: [review_id] }                 │
│  │                                                                   │
│  └── spawn → [Subagent: rule-review-agent]                           │
│                model: claude-sonnet-4-6                              │
│                tools: []                                             │
│                                                                      │
│ output = JSON のみ                                                   │
│   {"status":"approved","title":"...","problem":"...","iterations":N} │
│                                                                      │
└──────────────────────────────────────────────────────────────────────┘
                              │ events.list で JSON 取得
                              ▼
┌──────────────────────────────────────────────────────────────────────┐
│ ③ 副作用層（フロー型、決定論的）                                      │
│                                                                      │
│  [GitHub Actions step]                                               │
│   ├─ JSON schema validate                                            │
│   ├─ secret linter（gitleaks/trufflehog）                            │
│   ├─ Databricks SQL Statement API で INSERT/UPDATE                   │
│   ├─ github-script で Issue 作成 / 更新                              │
│   └─ processed_prs に status='processed' を UPSERT                   │
│                                                                      │
│  [Databricks notebook step]（trigger 1 / 3 の場合）                  │
│   ├─ 同上、ただし notebook 内で databricks-sdk-py を使う             │
│   └─ Issue 作成は requests + GitHub API token                        │
│                                                                      │
└──────────────────────────────────────────────────────────────────────┘
```

## 5. Trigger × 副作用 の組み合わせ（3 系統）

`workflow.md` で定義した 3 種類の発火タイミングに対して、誰が何を担当するか:

| # | 発火タイミング | trigger 層 | agent 層 | 副作用層 |
|---|---|---|---|---|
| **1** | OTel Bronze ingest 完了 or 1h schedule（rule-maintenance） | Databricks Workflow notebook が未処理 PR を SELECT → POST /v1/sessions | rule-creation-agent + multiagent review で candidate JSON 出力 | **Databricks notebook step** が DB INSERT + Issue 作成 + processed_prs UPSERT |
| **2** | `pull_request.closed` (head: claude/harness-rules/*, merged=false)（rule-pr-close cleanup） | GitHub Actions workflow が起動 | **agent 不要**（regex で PR body から unchecked rule_id 抽出） | **GitHub Actions step** が Databricks UPDATE SET status='archived' |
| **3** | daily schedule（skill-drift-check） | Databricks Workflow が daily 発火 | rule-creation-agent が promoted rule をもとに SKILL.md 整形 JSON を出力 | **GitHub Actions step**（trigger からの workflow_dispatch で呼ぶ）が branch push + PR 作成、または Databricks notebook が git CLI で push |

MVP では `1` のみ稼働。`2`（PR 運用開始後）と `3`（SKILL.md 再生成後）は後段で有効化する。

## 5.1 MVP の出力

MVP では PR を作らず、GitHub Issue を出力にする:

- repo 差分を発生させずに、抽出品質・副作用層の動作・trigger 層の整合性を検証する
- reviewer が Issue 上で candidate を確認できる
- SKILL.md 再生成 / branch push / PR checkbox cleanup を Phase 2 / Phase 3 に倒す

MVP の Issue には以下を載せる:

- 対象 repo / PR / commit
- 抽出された candidate rule（title / problem）
- review-agent の verdict + iteration 数
- Databricks row id
- session ID（events URL）

## 6. Provisioning の境界

CLI / coding agent に任せられるもの:

- Managed Agent (main / review) の作成・update（`/v1/agents`）
- Environment の作成（`/v1/environments`）
- Databricks Workflow notebook / Python タスクの実装
- GitHub Actions workflow yml の作成
- 副作用スクリプト（`scripts/db_insert.py`、`scripts/create_issue.py`、`scripts/process_rule_cleanup.py`）
- Databricks schema / SP 権限の設計

`platform.claude.com` の Web UI で人が設定するもの:

- Workspace と API key の管理
- Webhook destination の URL 登録（必要な場合）
- API usage / rate limit の監視

Databricks 管理者が設定するもの:

- Anthropic API key の Databricks Secret 格納（`anthropic_api_key`）
- Service Principal の作成と権限付与（`dev_bronze.test.harness_*` への SELECT / INSERT / UPDATE、`dev_bronze.s3_otel_logs.*` への SELECT、`harness_session_context` の SELECT）
- Databricks Workflow の権限委譲

GitHub 管理者が設定するもの:

- `ANTHROPIC_API_KEY` を GitHub Actions Secret に登録
- `DATABRICKS_TOKEN`（SP の Personal Access Token）を GitHub Actions Secret に登録
- branch protection（`main` 直 push 禁止）と CODEOWNERS（後段、PR 運用開始時）

→ Anthropic vault は本設計では未使用（agent に MCP server を attach しないため）。Phase 3 で read-only MCP を導入する場合のみ vault 登録が必要。

## 7. PoC ブロッカー（実装着手前に検証）

| 項目 | 確認内容 | 状況 |
|---|---|---|
| Anthropic Managed Agents API 利用可否 | `/v1/agents` / `/v1/sessions` / `/v1/environments` が API key で叩けるか | ✅ 確認済（experimental main + review 作成・session 起動成功） |
| multiagent 機能 | main を multiagent coordinator として宣言し、main → review の自動 delegate と revise loop が agent 内で完結するか | ✅ **実機確認済**（session `sesn_014HNByDrdKpwaULfM6CCxsh` で 2 iteration loop 完了、最終 approve JSON 出力） |
| Databricks Workflow → Anthropic API | Databricks notebook から `anthropic` SDK で `POST /v1/sessions` + events.list polling が動くか | 🟡 Phase 1 で実装・検証 |
| GitHub Actions → Anthropic API | yml から `curl POST /v1/sessions` + events.list polling で session を完了まで待てるか | 🟡 Phase 2 で実装・検証 |
| Databricks SP credential | SP の PAT を Databricks Secret に格納 → notebook 内で SELECT/INSERT/UPDATE できるか | 🟡 Phase 1B で確認 |
| OTel session_id 構造 | `s3_otel_logs.user_prompt.session_id` がトップレベル STRING で `harness_session_context` と JOIN 可能か | ✅ 確認済（PoC A、2026-05-26） |

## 8. 実装順序（Phase 構成）

### Phase 1: プロトタイプ（手動 kick、副作用は小型スクリプト）

1. Databricks table 作成 + test data seed ✅
2. Managed Agent (main multiagent coordinator + review) ✅
3. **手動 kick の CLI スクリプト**（curl + jq）: PR メタを user message に埋め込んで session 起動 → events.list で JSON 取得
4. **副作用スクリプト**（Python）: schema validate → Databricks INSERT → GitHub Issue 作成
5. 数件の実 PR で手動実行、抽出品質を検証

→ ここまでで「rule 抽出品質」「副作用層の動作」「ガード（schema validate / secret linter）」を確認

### Phase 2: フロー型自動化（Databricks Workflow + GitHub Actions）

1. **Databricks Workflow**: schedule 1h（後で OTel ingest Job dependency に切替）→ 未処理 PR を SELECT → 各 PR について Phase 1 のスクリプトを順次実行
2. **GitHub Actions cleanup workflow** (`harness-pr-cleanup.yml`): `pull_request.closed` で発火、regex で unchecked rule_id 抽出 → Databricks UPDATE。agent 不要
3. 監視: Databricks `harness_run_metrics` + GitHub Actions run ログ

→ pilot 1 repo で 1〜2 週間運用、誤検出 / false positive / cost を測定

### Phase 3: 後段機能

1. **SKILL.md 再生成**: Databricks Workflow daily で agent → SKILL.md JSON 整形出力 → GitHub Actions step が branch push + PR 作成
2. **ドラフト PR 化**: Issue → PR への昇格、checkbox UI で reviewer 操作
3. **read-only MCP の再評価**: agent に GitHub MCP (read) や Databricks SQL MCP (read) を attach するかを再検討

## 9. 参照

- Anthropic Managed Agents API: `platform.claude.com/docs/en/managed-agents/overview`
- Multiagent: `platform.claude.com/docs/en/managed-agents/multi-agent`
- Webhooks: `platform.claude.com/docs/en/managed-agents/webhooks`
- Anthropic CLI (`ant`): `platform.claude.com/docs/en/api/sdks/cli`
- Databricks Workflows: 公式 docs（schedule + Job dependency + File arrival trigger）
- GitHub Actions: 公式 docs（pull_request event、Secrets）
