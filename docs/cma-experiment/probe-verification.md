# Hook プローブ検証結果

## 1. 検証した内容

### 目的

Claude Code のフックを使って、ユーザのセッション情報と git / GitHub の情報を取得し、それを既存の OTel ログ（Databricks `dev_bronze.s3_otel_logs.*`）と **session_id で紐付け可能か** を確認する。

### 検証用プラグイン

`probe-plugin/` として実装:

```
probe-plugin/
├── .claude-plugin/plugin.json
├── hooks/hooks.json              # SessionStart / UserPromptSubmit / PreToolUse / Stop
└── scripts/probe.sh              # 取得情報を JSON にまとめて ~/probe-log.jsonl に append
```

取得項目:

- フック種別、stdin の JSON、cwd
- cwd の git 情報（toplevel / origin / upstream / branch / head / common_dir / git_dir / superproject / is_worktree）
- tool_input.file_path のディレクトリの git 情報
- gh CLI 経由の PR 情報（timeout 3 秒）
- `CLAUDE_*` / `ANTHROPIC_*` / `WORKSPACE_*` 系の環境変数

### 検証したシナリオ

| シナリオ | 構成 |
|---|---|
| A | git repo の中で起動 |
| B | git でない親ディレクトリで起動 |
| C | 親ディレクトリ起動 + 子 repo のファイルを編集（PreToolUse） |
| D | git worktree |
| E | submodule |
| F | fork ワークフロー（origin = fork, upstream = canonical） |

### JOIN 検証

既存の `dev_bronze.s3_otel_logs.user_prompt` から実セッション ID を取得し、架空の `session_context` と `session_id` で JOIN できるかを SQL で確認。

---

## 2. 取れた結果

### シナリオ別の取得可否

| シナリオ | 結果 | 主に効くフィールド |
|---|---|---|
| A. 単独 repo | ✅ | cwd の git 一式 |
| B. 親ディレクトリ起動 | △ | cwd 単独では取れず（C で補完） |
| C. 親ディレクトリ + file_path 逆引き | ✅ | `git_from_tool_file_path.*` |
| D. worktree | ✅ | `branch`（worktree 固有）+ `common_dir`（元 repo に集約可能） |
| E. submodule | ✅ | `superproject`（親 - 子の関係が判定可） |
| F. fork | ✅ | `remote_origin`（fork）+ `remote_upstream`（canonical）両方取得 |

→ **実用上の主要パターンはすべて取得可能**。

### 主な発見

- `CLAUDE_CODE_SESSION_ID` 環境変数で session_id を即取得できる（stdin 解析不要）
- `CLAUDE_CODE_ENABLE_TELEMETRY=1` が既に有効、OTel パイプラインは稼働中
- worktree は `common_dir` で同一 repo に集約可能
- submodule は `superproject` で親 - 子関係を自動判定可能
- fork は `origin` と `upstream` 双方を保持しているので、PR 対象（canonical）を識別できる

### JOIN 動作確認

既存テーブルの実セッション ID（`f028a2ff-...`）を使って `session_context` との JOIN をシミュレートしたところ、

- user_prompt の 5 イベントすべてが session_context の repo / branch / PR で enrich された
- `session_id` を JOIN key にすることで、`user_prompt` / `tool_result` / `api_request` / `api_error` / `tool_decision` の全テーブルが repo 情報と紐付け可能

→ **JOIN メカニズムは機能する**ことを確認。

### Databricks 側のテーブル設計案

下記は probe 検証時点（2026-05-25）の **フル設計案**（17 列）。プローブで取得可能な範囲を列挙したもの。

```sql
CREATE TABLE dev_bronze.test.harness_session_context (
  event_timestamp     TIMESTAMP,
  event_date          DATE,
  session_id          STRING,    -- JOIN key（既存 OTel テーブルと一致）
  user_email          STRING,
  hook_event          STRING,    -- SessionStart / PreToolUse / Stop 等
  tool_name           STRING,    -- PreToolUse のみ
  tool_input_file_path STRING,   -- PreToolUse のみ
  cwd                 STRING,
  git_toplevel        STRING,
  git_remote_origin   STRING,    -- fork URL の可能性あり
  git_remote_upstream STRING,    -- canonical URL（fork 検出用）
  git_branch          STRING,
  git_head            STRING,
  git_common_dir      STRING,    -- worktree 集約用
  git_superproject    STRING,    -- submodule 検出用
  pr_number           BIGINT,
  pr_url              STRING
)
PARTITIONED BY (event_date);
```

**注: 実装版は 6 列**（2026-05-25 ユーザ判断、列数を絞ることでスキーマ運用コストを削減）:

```
event_timestamp / event_date / session_id / git_remote_origin / git_branch / pr_number
```

`pr_url` 列を持たないため、agent からの session_id 逆引きは `(git_remote_origin, pr_number)` の組で行う（agent-spec.md §5 pseudocode 参照）。`user_email` / `git_remote_upstream` / `git_common_dir` / `git_superproject` 等の補助列はフル設計案には残し、必要が顕在化した段階で `ALTER TABLE ADD COLUMNS` で個別追加する方針。PoC A（2026-05-26）で 6 列構成での JOIN 整合性は確認済み。

これがあれば、

```sql
-- 任意の event を repo 情報で enrich
SELECT p.*, c.git_remote_origin AS repo, c.git_branch AS branch, c.pr_number
FROM dev_bronze.s3_otel_logs.user_prompt p
LEFT JOIN dev_bronze.test.harness_session_context c
  ON p.session_id = c.session_id
```

の形で全 OTel イベントに repo / branch / PR が紐付く。

---

## 3. 補足

### データ取り込みの遅延

`dev_bronze.s3_otel_logs.user_prompt` の最新 event は `2026-05-21 01:37`（検証日: 2026-05-25）。
S3 → Bronze の取り込みに **約 4 日の遅延**がある。今走らせたセッションの完全な実証は、その遅延分だけ後にずれる。

→ この実測値が [`agent-spec.md`](./agent-spec.md) §11.2 の Bronze 4 日 cooldown の根拠。

### 残る不確実性

`CLAUDE_CODE_SESSION_ID` 環境変数の値と、OTel テーブルの `session_id` 列の値が **完全に一致**するかは、

- フォーマット（UUID v4）が同じ
- 公式仕様上、OTel の `session.id` は Claude Code のセッション ID と同義

から強い確信を持てるが、**4 日遅延の都合で今このセッションでは実証できなかった**。実 Claude Code で probe を仕込んで 4〜5 日後にテーブルを再確認すれば確定する。

### timeout 対策

`gh pr view` がネットワーク不調で hang するリスクに備え、`gtimeout 3` / `timeout 3` を適用（macOS / Linux 双方で動作）。インストールされていない環境ではタイムアウト無しでフォールバックする。

### 次のステップ候補

- 実 Claude Code セッションで `--plugin-dir ./probe-plugin` を使い、本物の hook stdin を観察
- `dev_bronze.test.harness_session_context` テーブルの作成権限調整
- hook script を probe から本番版に置き換え（Databricks SQL Statement API への POST）
- ログ送信時の認証配布（Server-managed Settings 経由）
