# ルールのデータモデルと強制

ルールは **Databricks の `dev_bronze.test.harness_*` テーブル**に保存し、`.claude/skills/harness/<category>/SKILL.md` だけが repo に派生する。実装エージェント（loop B）に余計な情報を読ませないため、rule 本文・evidence・state はすべて Databricks に閉じ込める（§5 参照）。

```
Databricks dev_bronze.test.harness_*       ← rule 本文 / embedding / evidence / state（agent から直接見えない）
        │
        ▼ （Databricks Workflow daily schedule + GitHub MCP が SKILL.md を再生成して repo に PR）
repo .claude/skills/harness/<category>/SKILL.md  ← Claude Code に見せるルール一覧（agent はこれだけを読む）
        │
        ▼ （push 直前に hook が deny メッセージで指示、実装エージェントが作成・記入）
repo .harness/work/eval-sheet-<branch>.jsonl     ← タスクごとの verdict 記録（Append-only）
```

**rule の status は DB の `rules.status` カラムで管理**（`candidate` / `promoted` / `dormant_review` / `archived`）。ファイルパスでの区別はしない。

## 1. レッスン（lesson）

ルールの本体。Databricks `dev_bronze.test.harness_rules` に 1 行 1 レッスンで保存する。

### 1.1 配置

`rules` テーブルの `status` カラムで状態を区別:

| status | 意味 | SKILL.md に含まれる？ |
|---|---|---|
| `candidate` | 候補（evidence_count に応じて昇降） | × |
| `promoted` | 採用済み（3 回検出で昇格） | ✅ |
| `dormant_review` | 採用済みだが長期不発火、人レビュー喚起中 | × |
| `archived` | 削除扱い、再提案防止のため行は残す | × |

レッスン id（`rules.id`）が一意キー。SKILL.md 再生成時は `WHERE status = 'promoted'` で抽出する。

### 1.2 構造（雛形）

rule の各列は `dev_bronze.test.harness_rules` テーブル（§5.2.1）に保存。レッスン本文の構造化フィールドは以下の通り。

```
id: no-temp-artifact-commit
status: candidate                 -- candidate | promoted | dormant_review | archived
evidence_count: 2                  -- 現在の有効ポイント
category: git-hygiene              -- SKILL.md の分割キー
checklist_item: "..."              -- SKILL.md に出る短い一行
problem: "..."                     -- 問題の説明
guidance: "..."                    -- 詳細
exceptions: "..."                  -- 例外条件
examples: "..."                    -- OK / NG 具体例
merge_notes: "..."                 -- 統合判断材料
first_seen: 2026-05-25T10:00:00Z  -- 初回検出時刻、immutable
last_seen: 2026-05-28T15:00:00Z   -- 直近検出時刻、monotonic
updated_at: 2026-05-28T15:00:01Z  -- 行更新時刻、UPSERT で自動
```

evidence は別テーブル `rule_evidence`（§5.2.3）、embedding は別テーブル `rule_embeddings`（§5.2.2）。

#### MVP 必須カラム（不変条件）

| カラム | required | type | mutability | 備考 |
|---|---|---|---|---|
| `id` | ✅ | string | immutable | `^[a-z][a-z0-9-]{0,63}$`。**archived な id とも衝突する**（再利用禁止）、衝突時は `-2`, `-3` を suffix（最大 99） |
| `status` | ✅ | enum | agent / Action | candidate / promoted / dormant_review / archived |
| `evidence_count` | ✅ | int >= 0 | agent | `0 <= evidence_count <= COUNT(rule_evidence WHERE rule_id = id)` |
| `category` | ✅ | string | agent (確定後 immutable) | `^[a-z][a-z0-9-]{0,32}$`、SKILL.md ディレクトリ名と一致 |
| `checklist_item` | ✅ | string | agent | SKILL.md に出る短文 |
| `first_seen` | ✅ | timestamp | immutable | UTC、agent は改竄しない |
| `last_seen` | ✅ | timestamp | agent (monotonic) | UTC、単調増加 |
| `updated_at` | ✅ | timestamp | DB UPSERT | UPSERT 時に自動セット |

problem / guidance / exceptions / examples / merge_notes は optional（NULL 許容）。

#### Phase 2 で追加する想定カラム（MVP では持たない）

- `frozen_evidence_count`: dormant 凍結用。MVP では `dormant_review` 自動マーク自体を Phase 2 に倒すため不要
- `last_demoted_at`: demote 冪等性ガード用。MVP は `promoted < 10` で cap=0 のため demote 自体が走らず不要
- `merged_into`: merge 経路用。MVP では agent から merge を実行しない（review-agent が `merge_with_existing` を出した場合のみ、簡略な「片方 archived + evidence 移動」で代替）

### 1.3 ライフサイクル

#### MVP の状態遷移

```
[初検出] → candidate (evidence_count=1)
[再検出] → evidence_count++（dedup 後）
[evidence_count >= 3] → promoted（SKILL.md に登場）
[reviewer が PR の [ ] のまま close] → archived（GitHub Actions trigger → Managed Agent cleanup session 経由、deterministic）
```

`dormant_review` / 自動 demote は **Phase 2**。MVP では rule は基本「増える」方向のみ動き、archive は reviewer の checkbox 操作だけが trigger。

#### evidence dedup

同じ `(rule_id, pr_number, comment_id)` の組は `rule_evidence` の PK で構造的に重複できない（§5.2.3）。`comment_id` が NULL（PR body 由来）の場合も PK 内で一意。

#### archived からの復活（人手のみ）

agent は archived rule を自動復活させない（再提案防止）。誤 archived の救済は人手で `UPDATE rules SET status='candidate', evidence_count=1 WHERE id='...'`（CODEOWNERS 経由の operations PR + security-team approve）。

#### Phase 2 のライフサイクル拡張

| イベント | 操作 | フェーズ |
|---|---|---|
| 運用中の exception 率 > 20%（`compliant + exception >= 10`） | `evidence_count--`、`last_demoted_at` を更新 | Phase 2 |
| 長期不発火（180 日で `compliant + exception == 0`、最低セッション数 50） | `status: dormant_review` に変更 | Phase 2 |
| dormant_review で再検出 | `status: promoted` に戻す、`evidence_count = frozen + 1` | Phase 2 |
| `evidence_count == 0` | `status: archived` | Phase 2 |

詳細な冪等性ガード（demote 二重発火防止、集計ウィンドウ 180 日）も Phase 2 で導入。MVP は **promote と人手 archive の 2 経路のみ**で動作確認する。

#### dormant_review 判定基準（Phase 2 で導入時）

`compliant + exception`（実際に効いた回数）だけを見る。`n_a` は分母にも分子にも入れない。理由:
- マイナー分野のレッスンは `n_a` ばかりになりがちだが、それを陳腐化と誤判定したくない
- 「該当はするが効かなかった」は `n_a` 側に集約してあるので、`compliant == 0` が長期間続けば確実に「効いていない」と言える

### 1.4 マージ

類似 candidate が見つかった場合は **1 つに統合し、evidence を合算、`evidence_count` は max を維持**:

| 元 candidate A | 元 candidate B | 統合後 |
|---|---|---|
| count: 1 | count: 1 | count: 2（合算） |
| count: 2 | count: 1 | count: 2（max） + evidence 追加で 3 になり promote |
| promoted (lessons/) | count: 1 | 既存 lesson 本文を拡張、archive 側に `merged_into` 記録 |

## 2. SKILL.md（カテゴリ単位）

Claude Code に見せるルール一覧。**カテゴリごとに 1 つの Skill** として配置する。

### 2.1 役割

- そのカテゴリに属するレッスンの **Checklist Item** を列挙する
- レッスン ID をリンクし、詳細が必要なら `lessons/<id>.md` を参照させる
- レッスン本文そのものは載せない（コンテキスト効率のため）
- Skill の description は「いつこのカテゴリが該当するか」を簡潔に書き、Claude Code が自律ロード判定できるようにする

### 2.2 配置

```
.claude/skills/harness/
├── git-hygiene/SKILL.md
├── testing-conventions/SKILL.md
├── pr-description/SKILL.md
└── ...
```

`harness/` namespace 配下に置く。理由:

- developer が手書きで作った skill (`.claude/skills/<their-name>/`) と衝突しない
- Databricks Workflow（drift-check trigger）から起動された Managed Agent は `.claude/skills/harness/` 配下のみを管理対象とする（手書き skill は触らない）

ディレクトリ名 = category 名（レッスンの front matter `category` と一致させる）。

### 2.3 再生成のタイミング

レッスン側のイベントで再生成する。

- レッスンの追加
- レッスンの更新（特に `status` / `Checklist Item` / `category`）
- レッスンの削除 / archive

CLI: `harness skill regenerate` 相当。category 単位で差分再生成する。

### 2.4 category の決め方

`category` はレッスンの front matter で指定する大分類。Skill のディレクトリ単位になるため、

- 領域別（`git-hygiene` / `testing` / `pr-description` …）または
- レビュー観点別（`naming` / `safety` / `style` …）

のどちらか一貫した軸で運用する。新規レッスンをどの category に入れるかの判断は、ルール作成エージェントの仕事の一部。

## 3. 評価シート（eval-sheet）

タスクごとの verdict 記録。Append-only JSONL。**カテゴリは跨ぐ**（1 ブランチ = 1 ファイル）。

### 3.1 配置とファイル名

```
.harness/work/
├── eval-sheet-<branch>.jsonl     ← 現在のタスク
└── archive/
    └── eval-sheet-<pr-id>.jsonl  ← クローズ済み
```

ブランチ名のスラッシュ等は `--` 等に正規化（例: `feature/foo` → `feature--foo`）。

### 3.2 行フォーマット

```json
{"session_id": "abc-123", "task_id": "feature--foo", "lesson_id": "no-temp-artifact-commit", "verdict": "compliant", "ts": "2026-05-25T14:30:00Z"}
{"session_id": "abc-123", "task_id": "feature--foo", "lesson_id": "preserve-public-api", "verdict": "n_a", "ts": "..."}
{"session_id": "abc-123", "task_id": "feature--foo", "lesson_id": "some-lesson", "verdict": "exception", "reason": "意図的に公開 API を変更するタスク", "ts": "..."}
```

| field | 必須 | 値 |
|---|---|---|
| `session_id` | yes | Claude Code の `CLAUDE_CODE_SESSION_ID`（DORMANT 判定で session 数を数えるため必須） |
| `task_id` | yes | 通常はブランチ名の正規化 |
| `lesson_id` | yes | `lessons/<id>.md` の stem |
| `verdict` | yes | `compliant` / `n_a` / `exception` |
| `reason` | `exception` のみ必須 | 例外と判断した理由（**secret linter のチェック対象**、機密が混入したら commit がブロックされる） |
| `ts` | yes | ISO 8601 |

#### verdict の意味（重要）

| verdict | 意味 |
|---|---|
| `compliant` | **このレッスンが実際に改善を促した**（読んで実装を変えた / 注意した）。**強い signal** |
| `n_a` | 該当しない、または **該当はするが何の影響も無かった**（読んだが結果として実装に変化なし）。**弱い signal、集計時は無視可** |
| `exception` | 該当するが、理由ありで意図的に外した。`reason` 必須 |

「該当した」と「効いた」を区別する。**`compliant` は「効いた」だけに使う**。これにより、

- 効いた回数 (`compliant + exception`) が長期間ゼロのレッスンを「陳腐化候補」として検知できる
- マイナー分野のレッスンが `n_a` 連発で誤削除されない

エージェントへのプロンプトでこの定義を明示すること。

### 3.3 ライフサイクル

評価シートは **push 直前の PreToolUse hook をトリガにして、エージェントが reactive に作成・記入**する。事前初期化は行わない。

| タイミング | 動作 |
|---|---|
| タスク開始〜実装中 | 何も起きない。ファイルは存在しない |
| push / PR 作成を試行 | PreToolUse hook が発火 → `verify-eval-sheet` が状態を判定 → ファイル無し / 不足あり / OK のいずれかを返す |
| ファイル無し or 不足 | hook が deny + 「このパスに作って verdict を埋めろ」と template 付きで指示。エージェントが Write で作成・記入 |
| 全 verdict 揃った状態 | hook が push を通す |
| PR マージ後 | 評価シートはブランチに commit 済み、PR の diff として残る。main 上の `.harness/work/eval-sheet-*.jsonl` として **そのまま蓄積**される（追加処理なし） |
| Loop C 集計時 | rule-creation agent が `git/GitHub MCP` 経由で main の `.harness/work/eval-sheet-*.jsonl` を直近 N 件読み込んで集計する。Databricks の専用テーブルは Phase 1 では作らない |

### 3.4 並列タスクの扱い

ファイル名がブランチ単位で分かれるため、別ブランチで並列に走っても物理的に衝突しない。
同一ブランチで複数セッションが走った場合は `(task_id, lesson_id)` の重複行が起きうるが、verify は **最新行を採用**する。

## 4. 派生関係のサマリ

| 階層 | 編集 | 派生元 | 生成手段 |
|---|---|---|---|
| レッスン (`lessons/<id>.md`) | 人 / エージェント | — | 直接編集 |
| SKILL.md (`<category>/`) | 自動 | レッスン（同 category） | Databricks Workflow + Managed Agent + GitHub MCP で再生成 |
| 評価シート (`eval-sheet-*.jsonl`) | 実行エージェント | active なレッスン | push 直前に hook が deny で指示、エージェントが Write で作成・記入 |

**人が同期維持を意識するのはレッスンだけ**。他はすべてレッスンの写像。

## 5. データの保存先

ルール本体・state・派生物の保存先は **repo / Databricks / object storage** に役割で分かれる。実装エージェントが repo を grep / glob で探索した時に rule 本文や evidence を意図せず読み込まないよう、agent に見せたいもの（SKILL.md）以外は Databricks に置く。

### 5.1 全体像

| データ | 保存先 | 理由 |
|---|---|---|
| **SKILL.md（カテゴリ単位、agent に見せるもの）** | repo `.claude/skills/harness/<category>/SKILL.md` | 実装エージェントが Claude Code 経由で参照する唯一のソース |
| **eval-sheet（実装エージェントが書く評価シート）** | repo `.harness/work/eval-sheet-<branch>.jsonl` | 実装エージェントが PR push 時に書くため git 経路が自然 |
| **secret-linter.allow / config.yaml / linter source** | repo | CI で動く保護対象、CODEOWNERS で守る |
| **`.harness/state/disabled`**（kill switch flag） | repo | 確実に動く経路。env var との二系統で運用 |
| **rules（candidate / lesson 本体）** | Databricks `dev_bronze.test.harness_rules` | 実装エージェントの context に混入させない。secret 含みうるデータを repo の git history に残さない |
| **rule_embeddings**（vector） | Databricks `dev_bronze.test.harness_rule_embeddings` | rules と同じ理由 + vector の diff bloat 防止 |
| **rule_evidence**（PR / コメント由来の根拠） | Databricks `dev_bronze.test.harness_rule_evidence` | secret や個人情報を含みうるため repo に残さない |
| **processed_prs**（PR ごとの処理ログ） | Databricks `dev_bronze.test.harness_processed_prs` | rule と同じ DB で join しやすく |
| **review_outcomes**（review loop の結果記録） | Databricks `dev_bronze.test.harness_review_outcomes` | metrics / 監査用 |
| **demote_queue**（Phase 2 のみ、MVP は使わない） | Databricks `dev_bronze.test.harness_demote_queue` | MVP は flat cap=0、Phase 2 で導入 |
| **transcripts**（agent session の event stream） | S3 `s3://<bucket>/harness/transcripts/<session_id>.jsonl.gz` | object storage、30 日 lifecycle、secret scrub 後保存 |

**設計意図**:

- 実装エージェント（loop B）は `.claude/skills/harness/<category>/SKILL.md` 以外を読まないので、rule 本文 / evidence / state を repo に置く必要がない
- repo の diff は SKILL.md と eval-sheet の更新だけになり、reviewer の認知負荷が下がる
- protected paths が `.harness/secret-linter.*` / `.harness/state/disabled` / `.github/workflows/**` / `scripts/*` のみに縮小、§10.5 多層防御を大幅に簡素化できる
- candidates / lessons の history は Databricks の `updated_at` / audit カラムで追える

### 5.2 Databricks スキーマ

すべて `dev_bronze.test.harness_*` 配下。SP の権限は本スキーマ内の SELECT / INSERT / UPDATE のみ。

#### 5.2.1 `rules`（candidate / lesson 本体）

```sql
CREATE TABLE dev_bronze.test.harness_rules (
  id              STRING NOT NULL,
  status          STRING NOT NULL,
  evidence_count  INT NOT NULL,
  category        STRING NOT NULL,
  checklist_item  STRING NOT NULL,
  problem         STRING,
  guidance        STRING,
  exceptions      STRING,
  examples        STRING,
  merge_notes     STRING,
  first_seen      TIMESTAMP NOT NULL,
  last_seen       TIMESTAMP NOT NULL,
  updated_at      TIMESTAMP NOT NULL,
  PRIMARY KEY (id)
);
```

- `id`: kebab-case、`^[a-z][a-z0-9-]{0,63}$`、衝突時は active + archived の id を両方確認して `-2`, `-3`, ... を suffix（§1.2 参照）
- `status`: `candidate` / `promoted` / `dormant_review` / `archived`
- `evidence_count`: 0 以上、`COUNT(rule_evidence WHERE rule_id = id)` 以下
- `category`: `^[a-z][a-z0-9-]{0,32}$`
- `first_seen` は immutable、`last_seen` は monotonic

Phase 2 で追加する field（`frozen_evidence_count` / `last_demoted_at` / `merged_into`）は **MVP では持たない**。

#### 5.2.2 `rule_embeddings`

```sql
CREATE TABLE dev_bronze.test.harness_rule_embeddings (
  rule_id     STRING NOT NULL,
  model       STRING NOT NULL,
  vector      ARRAY<FLOAT> NOT NULL,
  updated_at  TIMESTAMP NOT NULL,
  PRIMARY KEY (rule_id, model)
);
```

model field を持つことで model 変更時の混在を検出可能（cosine 比較は同 model 内のみ）。

#### 5.2.3 `rule_evidence`

```sql
CREATE TABLE dev_bronze.test.harness_rule_evidence (
  rule_id     STRING NOT NULL,
  pr_number   BIGINT NOT NULL,
  comment_id  BIGINT,
  commit_sha  STRING,
  note        STRING,
  ts          TIMESTAMP NOT NULL,
  PRIMARY KEY (rule_id, pr_number, COALESCE(comment_id, -1))
);
```

- dedup は PK で構造的に保証
- `note` は **insert 前に secret linter を通す**（agent-spec §10.5）、最大 200 chars

#### 5.2.4 `processed_prs`

```sql
CREATE TABLE dev_bronze.test.harness_processed_prs (
  pr_url             STRING NOT NULL,
  status             STRING NOT NULL,
  first_seen_at      TIMESTAMP NOT NULL,
  updated_at         TIMESTAMP NOT NULL,
  reason             STRING,
  patterns_extracted INT,
  PRIMARY KEY (pr_url)
);
```

MVP は **3 状態のみ**:

- `in_progress`: 処理中（cooldown 待ちも含む）
- `processed`: 完了
- `skipped_permanent`: 永久 skip（OTel 未着等）

`updated_at` が真実源（DB の UPSERT で atomic に書き換え）。`skipped_retry` / `force_retry` / `pushed` / `started` 等の細分化は Phase 2 で必要になれば追加。

`first_seen_at` は PR を最初に観測した時刻で、PR の close→reopen で merged_at が変わっても cooldown が再起動しないように使う。

#### 5.2.5 `review_outcomes`

```sql
CREATE TABLE dev_bronze.test.harness_review_outcomes (
  rule_id          STRING NOT NULL,
  pr_url           STRING NOT NULL,
  final_verdict    STRING NOT NULL,
  iterations       INT NOT NULL,
  feedback_history STRING,
  ts               TIMESTAMP NOT NULL,
  PRIMARY KEY (rule_id, pr_url, ts)
);
```

- `final_verdict`: `approve` / `max_iter_reached` / `reject` / `merge_with_existing`
- `feedback_history`: JSON 配列文字列、各 iteration の reviewer feedback

### 5.3 repo に残る state ファイル（縮小版）

| パス | 編集主体 | 用途 | 保護機構 |
|---|---|---|---|
| `.claude/skills/harness/<cat>/SKILL.md` | Databricks Workflow + Managed Agent が再生成 | agent が参照する唯一のソース | 通常のレビュー |
| `.harness/work/eval-sheet-<branch>.jsonl` | 実装エージェント | 評価シート | CI required check |
| `.harness/secret-linter.allow` | 人（security-team CODEOWNERS） | secret linter FP 抑制 | CODEOWNERS + harness-allowlist-guard |
| `.harness/secret-linter.config.yaml` | 人 | linter パターン設定 | 同上 |
| `.harness/state/disabled` | 人（security-team CODEOWNERS） | kill switch flag | 同上。env var との二系統 |
| `scripts/secret_linter.py` / `scripts/sanitize.py` | 人 | linter / sanitize 関数本体 | CODEOWNERS（KMS-signed manifest は Phase 2） |

**MVP では `secret-linter.allow` は raw 文字列でも受理**（hash 形式強制は Phase 2）。reviewer が PR で実 secret を見せない運用ルールでカバーする。

### 5.4 transcripts（object storage）

agent session の event stream を JSONL として S3 等の object storage に保管。

- 配置: `s3://<bucket>/harness/transcripts/<agent_session_id>.jsonl.gz`
- `agent_session_id` は Managed Agent の session id（`sesn_...`）を使う。`review_outcomes` 等で同じ id を使う
- retention: pilot 中 **30 日**（bucket lifecycle policy で自動削除）、Phase 2 で 180 日に
- secret scrub: 書き込み前に §10.5 linter で masking、Bash tool_result の stdout も対象
- 読み取り権限: security team 限定
- bucket は **SSE-S3 + bucket policy で書き込み制限のみ**（MVP）、KMS CMK + object-lock は Phase 2

### 5.5 archived rule の扱い

`status: archived` になった rule は DB に残し続ける（削除しない）。理由:

- 再検出時の「過去に却下済み」判定に必要（再提案防止）
- id の tombstone として永久予約（同 id の再利用禁止）

`rules` テーブルから抽出する場合は `WHERE status != 'archived'` で除外、再提案チェックは archived も含めて lookup する。

---

## 6. 強制（enforcement）

評価シートが全項目埋まっていない PR を **マージさせない**ことが、本システムが機能するための最低条件。

### 6.1 共通の検証ロジック: `verify-eval-sheet`

各層から呼び出す検証コマンドは同じ。

```
verify-eval-sheet
  ├→ current_task_id = git branch --show-current（正規化）
  ├→ 該当 JSONL をフィルタ
  ├→ active レッスンの lesson_id が全部 verdict 付きで存在するか確認
  ├→ verdict が `exception` のものは reason が空でないか確認
  └→ 不備があれば exit 1 + 説明を stderr に出す
```

### 6.2 三層構造

| 層 | 配布 | 役割 | 抜け穴 |
|---|---|---|---|
| Claude Code PreToolUse フック | プラグイン | 実装エージェントへの即時フィードバック | プラグイン無効化 / Claude Code 外からの push |
| git pre-push フック | opt-in インストール | ターミナル直 push / 他 IDE をカバー | フック未インストールのリポジトリ |
| CI required check | リポジトリの workflow | **真の enforcement**（管理者以外は外せない） | — |

導入の優先順位は **CI → Claude Code フック → git フック**。CI さえあれば「マージできない」は確実に守れる。

### 6.3 第 1 層: Claude Code PreToolUse フック

プラグインに同梱して配布。Bash matcher で push 系コマンドを捕捉する。

```json
{
  "hooks": {
    "PreToolUse": [{
      "matcher": "Bash",
      "command": "harness-pre-push-guard"
    }]
  }
}
```

スクリプトの動作:

1. tool input の `command` が `git push` / `gh pr create` / `gh pr edit` を含むか判定
2. 該当なら `verify-eval-sheet` を実行
3. 結果に応じて応答:

   | 状態 | 応答 |
   |---|---|
   | OK | tool を通す |
   | ファイル無し | deny + path と全 active レッスンの template を含む指示を返す |
   | 不足あり | deny + 不足レッスン一覧を返す |

deny メッセージはエージェントへの**指示書**になっており、これに従って Write でファイル作成または該当行の追記をすれば再 push で通る。

guard rail であり security boundary ではない。プラグインを無効化されれば動かない。最終的な enforcement は CI に置く。

### 6.4 第 2 層: git pre-push フック（任意）

ターミナル直 `git push` や、Claude Code 外のクライアントから push されるケースをカバー。

インストール方法:

- プラグイン SessionStart 時に `git config core.hooksPath` を共有ディレクトリに向ける
- 明示コマンド `harness install-git-hooks` で `.git/hooks/pre-push` を設置
- リポジトリの `.husky/` 等で管理（プロジェクト側の opt-in）

中身は `verify-eval-sheet` を呼ぶだけ。失敗したら exit 1 で push を止める。

### 6.5 第 3 層: CI の必須チェック

GitHub Actions などで `verify-eval-sheet` を回し、required status check に指定する。

```yaml
# 概念図
- name: Verify eval-sheet
  run: harness verify-eval-sheet --task-id "${{ github.head_ref }}"
```

ローカルフックは無効化可能だが、CI required check は管理者権限が無いと外せない。**PR をマージさせない** という強制は、ここでだけ確実に成立する。

### 6.6 抜け穴と多層防御の関係

| 想定される抜け穴 | カバーする層 |
|---|---|
| エージェントが checklist を埋め忘れて push を試みる | 1 層（即時 deny） |
| ユーザがターミナルから直接 push する | 2 層 |
| プラグイン / git フック未インストール | 3 層 |
| 管理者が一時的にチェックを外す | 運用ルールで担保 |
