# ワークフロー

> 関連ドキュメント:
> - レッスン / SKILL / 評価シート / state ファイルの構造、強制（hooks / pre-push / CI）の三層構造: [`rule-model.md`](./rule-model.md)
> - データ収集 / 認証配布 / プラグイン配布: [`data-collection.md`](./data-collection.md)
> - rule-maintenance-agent の I/O 仕様・処理 pseudocode: [`agent-spec.md`](./agent-spec.md)
> - Managed Agent + 外部 trigger（Databricks Workflow / GitHub Actions）の実現性: [`managed-agent-capabilities.md`](./managed-agent-capabilities.md)

## 実行基盤の前提

本 workflow の rule maintenance は **Anthropic Managed Agents API（`platform.claude.com/v1/agents`、beta `managed-agents-2026-04-01`）で作成した agent を、外部 trigger から `POST /v1/sessions` で起動して実行する**。

trigger 機構は外部に持ち、以下の 2 系統を採用する:

- **Databricks Workflows**: schedule（1h）または OTel Bronze ingest Job dependency で発火。trigger 1（rule-maintenance）と trigger 3（skill-drift-check）を担当
- **GitHub Actions**: `pull_request.closed`（head: `claude/harness-rules/*`、merged=false）で発火。trigger 2（rule-pr-close cleanup）を担当

ローカル CLI / launchd / cron は主 runner にしない。Claude Code on the web Routines は使わない（[`managed-agent-capabilities.md`](./managed-agent-capabilities.md) §3 参照）。GitHub Actions は trigger と cleanup の薄い呼び出し層に限定し、rule 抽出ロジック本体は Managed Agent 内で完結させる。

Managed Agent の作成・update は CLI / SDK / Web UI から可能（`platform.claude.com/workspaces/default/agents`）。Vault credential のみ Web UI または vault API 経由で人が登録する。

## MVP の進め方（Issue-first）

最初の MVP は **PR を作らず GitHub Issue に candidate を出す**。
これにより、repo 差分 / SKILL.md 再生成 / PR cleanup を後段に倒し、Databricks と外部 trigger と Managed Agent の最小ループだけを検証する。

実装順序:

| 順番 | 作業 | 完了条件 |
|---|---|---|
| 1 | Databricks table 作成 | `harness_rules` / `harness_rule_evidence` / `harness_processed_prs` / `harness_run_metrics` が作成済み |
| 2 | テストデータ投入 | merged PR 相当の fixture、review comment、eval-sheet、OTel/session context を最小行で SELECT できる |
| 3 | Agent 作成 | `rule-creation-agent` と `rule-review-agent` が candidate を構造化して返せる |
| 4 | 外部 trigger 作成 | Databricks Workflow（schedule + Job dependency）と GitHub Actions workflow から `POST /v1/sessions` で Managed Agent session が起動し、MCP 経由で Databricks と GitHub にアクセスできる |
| 5 | Issue 出力 | candidate rule を GitHub Issue として作成し、Issue URL と Databricks row id を相互参照できる |
| 6 | PR / SKILL.md 化 | Issue-first の精度が見えた後に、branch 作成・SKILL.md 再生成・PR cleanup へ進む |

Issue-first の間は §5 cleanup と §6 SKILL.md 再生成は **設計済みだが未実行** とする。

## 1. 全体像（俯瞰）

この節は **PR / SKILL.md 運用まで有効化した後段の最終形** を示す。
MVP の Issue-first では、agent の出力は GitHub Issue で止め、ドラフト PR と cleanup はまだ動かさない。

agent の起動と reviewer の判断は **独立した時間軸**で進む。agent が ドラフト PR を継続的に育て、reviewer は自分のタイミングで checkbox を操作・merge/close する。

```
[時間軸 A: agent の incremental 更新]                   [時間軸 B: reviewer の判断]
                                                       （A とは独立した cadence）

  feature PR が情報揃った                                reviewer が見に来る
    │                                                    │
    ▼                                                    ▼
  rule-creation-agent 起動                              ドラフト PR description を見る
    │                                                    │
    ├─ ループ A: candidate 抽出 / 昇格                    ├─ [ ] にチェック付ける（採用）
    ├─ （ループ C 自動マーク は Phase 2、MVP では無し）   ├─ [x] を外す（削除したい）
    ├─ rule-review-agent を spawn（candidate ごとに      └─ 既存 [x] は維持
    │   verdict: approve / revise / reject /              │
    │   merge_with_existing / max_iter_reached）          │
    └─ ドラフト PR description を incremental 更新           │
       （checkbox の [x]/[ ] 状態は保持、絶対上書きしない）   │
       （フッタに review verdict サマリを追加）                │
                                                            │ 都合がついたら
                                                            ▼
                                                       [Event: PR の merge or close]
                                                            │
                                                            ▼
                                                       PR closed event で
                                                       GitHub Actions cleanup workflow が起動
                                                            │
                                              ┌─────────────┴─────────────┐
                                              │                           │
                                              ▼                           ▼
                                          全 [x] で merge             [ ] 残しで close
                                          → 何もしない                → unchecked を archive
                                          （state は main に確定）     （新 PR は作らない、
                                                                       次の candidate 出現を待つ）

                                          ※ どちらの場合も draft PR は無くなる
                                          ※ 新しい変更が発生したら trigger 1 経由で
                                            「無ければ新規作成、あれば append」

[ループ B: 実装エージェントが各タスクで使用 / 別系統]
   ↓ 評価シート蓄積
   → ループ C 集計の入力に戻る（時間軸 A 経由）
```

### agent のトリガは 2 種類

| # | トリガ | 動作 |
|---|---|---|
| 1 | feature PR の情報揃った | ループ A + C 集計。**変更がある場合のみ** draft PR を更新（既存があれば append / 無ければ新規作成） |
| 2 | ドラフト PR の close/merge | cleanup: close なら unchecked を archive（merge なら何もしない、state は main に確定済み）。**いずれも新 PR は作らない**（次の candidate が出るまで待つ） |

**reviewer の checkbox 操作自体は agent をトリガしない**。agent は incremental に description を更新するだけ。reviewer の意思は次回 merge/close 時に確定する。

### Draft PR の生死

| 状態 | 状況 |
|---|---|
| 開いてる | 直近の更新が反映中、reviewer 待ち |
| 閉じてる（merge 直後 / close 直後） | **正常状態**。新しい candidate が来るまで PR は作らない |
| 新 candidate / 変更が発生 | trigger 1 経由で draft PR が「無ければ新規作成、あれば append」 |

つまり draft PR は **「変更がある時だけ存在する」 lazy なリソース**。空のドラフトを作って待たせることはしない。

ループ B（実装エージェントのルール適用 + 評価シート記入）は別系統で、評価シートのデータを介してループ C 集計に signal を提供する。詳細は 3 節。

---

## 2. ルール作成フロー（ループ A）

PR 単位に処理する。情報が揃った PR を順に処理し、検出されたパターンを **Databricks `dev_bronze.test.harness_rules` の `status='candidate'` で蓄積し、3 回検出で `status='promoted'` に更新** する（rule-model.md §5）。

### Step 1. PR ごとに「情報が揃ったか」を判定

各 PR について以下が揃ったかをチェック:

- PR がマージ済み（GitHub API）
- OTel ログが Bronze 取り込み済み（session_id で突合可）
- 評価シートが commit に含まれている

未到達なら次のサイクルで再検査。揃った PR から順に Step 2 へ。

### Step 2. その PR から情報を取得

- PR 本文・レビューコメント
- 該当セッションの OTel ログ（user_prompt / tool_result 等）
- 該当ブランチの評価シート
- Databricks 上のプロンプト（その時期に該当する版）

### Step 3. パターン候補の抽出

その PR から「ルール化しうる繰り返し指摘」を抽出する。1 PR で複数のパターン候補が出ても OK。

### Step 4. 既存 candidate / lesson との照合

抽出したパターンと、既に `rules` テーブルにある既存項目（archived も含む）を比較:

- **マッチあり** → 該当ファイルの `evidence_count++`、`evidence[]` に今回の情報を追加
- **マッチなし** → `rules` に新規 INSERT、`status='candidate'`、`evidence_count=1`

類似度の判断は LLM ベース（`category` / `Checklist Item` / `Problem` を見て semantic に判定）。

### Step 5. 昇格判定

`evidence_count` 更新の結果:

- **3 到達** → `rules.status = 'promoted'` に UPDATE、ドラフト PR に含める（SKILL.md 再生成で agent から見えるようになる）
- **2** → そのまま `status='candidate'` のまま次回検出を待つ
- **1** → 新規候補として保留

### Step 6. 出力の更新（MVP は Issue、後段でドラフト PR）

MVP では GitHub Issue を作成 / 更新する。PR と SKILL.md 更新は Issue-first の精度が確認できてから有効化する。

Issue には以下を載せる:

- 対象 PR / session / Databricks row id
- candidate rule の `category` / `checklist_item` / `problem`
- evidence excerpt
- 重複判定 / review-agent verdict
- reviewer が採用判断できる checkbox

#### 後段: ドラフト PR の更新（lazy 作成 + incremental 更新）

変更（新規 candidate / 昇格 / マーク付与）があった場合のみ:

| 状態 | 動作 |
|---|---|
| draft PR が無い | **新規作成**。description に「全ルールの現在状態」を 1 枚の checklist として描画 |
| draft PR が有る | **既存に append**。description は incremental に更新（既存 `[x]/[ ]` 状態は保持、新規追加・マーク更新のみ） |

rule 本体の変更は Databricks 側で完結。**PR に含まれる git diff は SKILL.md の更新と PR description の更新のみ**（実装エージェントが repo を grep / glob した時にノイズが入らない設計）。

**変更が無い場合は何もしない**（空 PR は作らない）。

#### description のフォーマット

```markdown
## 現在の全ルール

### git-hygiene
- [x] no-temp-artifact-commit: 作業用ファイルや一時ファイルを実装差分に含めない
- [x] preserve-public-api: 既存 API の公開 contract を理由なく変えない
- [ ] consistent-naming-new: 変数名は snake_case で統一（NEW — 今サイクル追加）

### testing
- [x] always-add-test: 機能追加時はユニットテスト必須
- [x] [DORMANT] mock-database: テストで DB をモックしない（6 ヶ月発火なし、要確認）

### error-handling
- [ ] swallow-exceptions: catch しても無視しない（NEW）
- [x] [DEMOTE 推奨] old-rule-x: exception 率 25%、見直し推奨
```

マーク:

| マーク | 意味 |
|---|---|
| `[x]` | 前回承認済み（または今サイクルでも維持） |
| `[ ]` | 新規候補 or 削除候補（reviewer の意思待ち） |
| `[NEW]` | 今サイクルで新規追加された candidate / promoted |
| `[DORMANT]` | 長期不発火、確認推奨（[`rule-model.md`](./rule-model.md) のライフサイクル） |
| `[DEMOTE 推奨]` | exception 率高、見直し推奨 |

PR は 1 リポジトリにつき 1 本を常時オープン。**マージ / close（reject）が次サイクルの起点**。

### Step 7. 人レビュー → checkbox による意思表示

レビュワーは description の checkbox を操作:

| 操作 | 意味 |
|---|---|
| 新規 `[ ]` にチェック | 採用 |
| 既存 `[x]` を外す | 削除したい |
| `[DORMANT]` / `[DEMOTE 推奨]` の `[x]` を外す | 確認した上で削除 |
| `[x]` のまま維持 | 現状維持 |

そして:

| reviewer の最終操作 | システムの解釈 |
|---|---|
| **全部 `[x]` の状態で merge** | 現状維持で確定。`rules` テーブルの最新 status がそのまま生き、SKILL.md が DB から再生成される |
| **`[ ]` を残したまま close（reject）** | reject された状態を agent が次サイクルで処理 |

### Step 8. close / merge された場合の挙動

| 結末 | agent の動作（次回起動時） |
|---|---|
| 全 `[x]` で **merge** | 何もしない。state はすでに main に反映済み |
| `[ ]` を残して **close** | description を読み、`[ ]` の項目を `status: archived` に変更（ファイル削除 or archive ディレクトリへ移動）。`[x]` は維持 |

**どちらの場合も新しい draft PR は即時に作らない**。次に candidate / 変更が発生した時点で Step 6 経由で lazy に作られる。

つまり PR は **「変更がある時だけ存在する」**。空のドラフトを開いて待たせることはしない。

---

## 3. ルール適用フロー（ループ B）

実装エージェントが各タスクで回す。評価シートはこのフローの中で、**push 直前の hook をトリガに reactive に作成・更新**される。事前初期化は無し。

### Step 1. タスク開始

ブランチを切る or 切り替える（`feature/foo` など）。
この時点では評価シートは存在しない。

### Step 2. 実装作業

通常通りタスクを進める。

- タスク文脈に合致する category の `SKILL.md` を Claude Code が自律ロード
- 各 Skill にはチェック項目（レッスン ID + 一行要約）が並ぶ
- 判断つかない / 詳細が必要な項目は Databricks `rules` テーブルで該当 id を都度確認

評価シートはまだ触らない。

### Step 3. push 試行

エージェントが `git push` / `gh pr create` 等を呼ぶ。

### Step 4. PreToolUse hook が検証 + 指示（第 1 層）

hook が `verify-eval-sheet` を実行し、状況に応じて応答を返す。

| 状態 | hook の応答 |
|---|---|
| ファイル無し | deny + 「以下のパスにファイルを作成し、verdict を埋めてください」（path + 全 active レッスンの template を含む） |
| ファイル有り、不足あり | deny + 「以下のレッスンに verdict を埋めてください」（不足リスト） |
| 全 active レッスンに verdict あり | tool 実行を通す |

deny メッセージにはエージェントが次にやるべき作業が全部含まれる。

### Step 5. エージェントが自己修正

deny メッセージに従って動く:

- ファイル無しの場合: `Write` で指定パスにファイル作成
- 各 active レッスンについて、自分のタスクと照らして verdict を判定（`compliant` / `n_a` / `exception`）
- `exception` の場合は `reason` を必須記入
- 完了後、再度 push を試行

Step 4 と Step 5 は通るまで往復する。

### Step 6. ローカルクライアントの検証（第 2 層 — 任意）

ターミナル直 push や他 IDE からの push を捕まえる場合、`git pre-push` フックでも同じ `verify-eval-sheet` を走らせる。

### Step 7. PR 作成成功 → CI 検証（第 3 層）

PR が作成されると CI で `verify-eval-sheet` が走り、required status check として **マージをブロック**する。

→ 三層のいずれかが通れば evaluation が完了している保証。

### Step 8. マージ → 評価データの集約

評価シートはブランチに commit されているので、PR の diff の一部として永続的に残る。
マージ後の集約方法（main 上での扱い、集計用 DB への取り込み）は情報収集プラグインの責務（6 節）。

---

## 4. 改善・削除ループ（ループ C）— **Phase 2**

**MVP では実装しない**。agent-spec §11.4 で archive cap = 0 / demote cap = 0 と決めたため、agent からの自動 demote / dormant_review / archive は走らない。状態を減らす方向に動くのは **reviewer の checkbox `[ ]` 残し close** だけ（§5 GitHub Actions cleanup trigger）。

Phase 2 で導入予定の signal は以下。**MVP では参考情報**として保留:

| signal | Phase 2 の agent 自動アクション | PR description 表記 |
|---|---|---|
| exception 率 > 20% かつ `compliant + exception` >= 10 | `evidence_count--`（0 になったら `status: archived`） | `[x] [DEMOTED] <id>` |
| 長期不発火（過去 180 日で `compliant + exception` == 0、セッション数 >= 50） | `status: promoted` → `dormant_review` | `[x] [DORMANT] <id>` |
| dormant_review で再検出 | `status: dormant_review` → `promoted` に復帰 | `[x] [REVIVED] <id>` |
| 人却下（PR が `[ ]` 残しで close される） | **PR 運用開始後に実装**: GitHub Actions cleanup trigger → Managed Agent cleanup session 経由で DB の `status='archived'` に UPDATE | （次サイクルの description に出ない） |
| インシデント連動 archive | 別チャネルで手動 UPDATE | — |

### Phase 2 で取り入れる集計の基本方針

**`compliant + exception`（= 実際に効いた回数）だけを見る**。`n_a` は分母にも分子にも入れない。

### Phase 2 集計の実装方針

評価シートは **main の `.harness/work/eval-sheet-*.jsonl` を直近 N=50-100 件 glob し、agent が on-the-fly で集計**する（Databricks ingest なし）。集計結果は `review_outcomes` ではなく Phase 2 で追加する `rule_metrics` テーブルに書く想定。

---

## 5. クリーンアップ自動化（GitHub Actions trigger）

ドラフト PR が **close（reject）** された場合の cleanup を担当する deterministic な層。
**GitHub Actions workflow** が trigger し、`POST /v1/sessions` で cleanup 用 Managed Agent session（rule-creation-agent を再利用、cleanup mode を user message で指示）を起動する。

### 5.1 トリガと識別

GitHub Actions workflow を以下の条件で設定する:

```yaml
# .github/workflows/harness-pr-cleanup.yml
on:
  pull_request:
    types: [closed]
    branches: [main]

jobs:
  cleanup:
    if: |
      github.event.pull_request.merged == false &&
      startsWith(github.event.pull_request.head.ref, 'claude/harness-rules/')
    runs-on: ubuntu-latest
    steps:
      - name: Trigger Managed Agent cleanup session
        run: |
          curl -sS https://api.anthropic.com/v1/sessions \
            -H "x-api-key: ${{ secrets.ANTHROPIC_API_KEY }}" \
            -H "anthropic-version: 2023-06-01" \
            -H "anthropic-beta: managed-agents-2026-04-01" \
            -H "Content-Type: application/json" \
            -d "{\"agent\":{\"type\":\"agent\",\"id\":\"$AGENT_ID\"},\"environment_id\":\"$ENV_ID\",\"vault_ids\":[\"$VAULT_ID\"],\"title\":\"cleanup-pr-${{ github.event.pull_request.number }}\"}"
          # session ID を控えて events.send で PR メタ + cleanup 指示を投入
```

`pull_request.closed` event ごとに新しい session を起動するため、cleanup は Databricks 側で idempotent にする（`status='archived'` への UPDATE は重複しても問題なし）。
PR body / PR number / head branch は **GitHub MCP server** 経由で agent が取得し、Databricks 更新は **Databricks SQL MCP server** 経由で agent が実行する。
credential は GitHub Actions Secret に保持された Anthropic API key と、Anthropic vault に格納された MCP credential の 2 系統で完結する（agent コンテナの env には乗らない）。

cleanup 完了後、SKILL.md 再生成が必要なら drift check trigger（§6）で次回 daily run で同期される。

### 5.2 処理ロジック（pseudocode）

rule 本体は Databricks `dev_bronze.test.harness_rules` にあるため、cleanup は **DB の UPDATE** だけで済む。
git の rule ファイル書き換えは不要。下記は処理の擬似コードであり、DB 更新は connector / MCP tool で実行する。

```python
#!/usr/bin/env python3
# scripts/process_rule_cleanup.py
import re, sys

# GitHub connector / MCP tool から取得する
PR_BODY = github.current_pr_body()
PR_NUMBER = github.current_pr_number()
ID_RE = re.compile(r"^- \[ \] (?:\[[^\]]+\]\s+)*([a-z][a-z0-9-]{0,63})\b")

# 1. description から `- [ ]` 行の rule_id を抽出（path traversal 対策 + id 規約 enforce）
unchecked_ids = []
for line in PR_BODY.replace("\r", "").splitlines():
    m = ID_RE.match(line)
    if m:
        unchecked_ids.append(m.group(1))

if not unchecked_ids:
    sys.exit(0)

# 2. Databricks MCP tool で archived に更新
for rid in unchecked_ids:
    databricks.archive_rule(rule_id=rid, source_pr=PR_NUMBER)

# 3. SKILL.md 再生成は別経路（Databricks Workflow daily drift-check）に委譲
regenerate_skill_md()
```

セキュリティ・堅牢化のポイント:

- `PR_BODY` / `PR_NUMBER` は GitHub connector / MCP tool 経由で取得し、shell 経由で展開しない
- `ID_RE` で id を `^[a-z][a-z0-9-]{0,63}$` に制限（rule-model.md §1.2 の規約と一致）
- DB へのアクセスは Databricks SQL connector / MCP tool 経由（data-collection.md §3.2）
- credential は connector / MCP server 側に保持し、agent env には渡さない

### 5.3 archive 後の保存

DB の `rules.status` を `archived` に更新するだけ。**行は削除しない**（id の tombstone として永久予約、再提案防止）。re-promote するには human operator が CODEOWNERS 経由の operations PR で `UPDATE` を実行する。

### 5.4 失敗時の挙動

| 状況 | 対応 |
|---|---|
| description の checklist が parse 不能 | GitHub Actions run を失敗扱いにし、人が手動 cleanup |
| rule_id が DB に存在しない | warning ログ、その項目はスキップ |
| Databricks への接続失敗 | Managed Agent session 失敗、GitHub Actions の retry で OK（UPDATE は idempotent） |

### 5.5 merge の場合の挙動

`merged == true` なら cleanup は skip される。merge は「checkbox が `[x]` のまま」の確定行為で、archive すべき rule は無い。
SKILL.md の同期が必要な場合は、Databricks Workflow の daily drift-check trigger が起動した Managed Agent session が GitHub MCP 経由で再生成 PR を作る。

---

## 6. SKILL.md 自動再生成（Databricks Workflow + GitHub MCP）

実装エージェントが見る `.claude/skills/harness/<category>/SKILL.md` を **Databricks `rules` テーブル (`status='promoted'`) の最新状態に同期する** ための deterministic な層。
**Databricks Workflow（日次 schedule、harness-skill-drift-check trigger）** が起動した Managed Agent session 内で、GitHub MCP server 経由で repo を更新する。

### 6.1 トリガ

| Trigger | 動作 |
|---|---|
| Databricks Workflow: harness-rule-maintenance（schedule 1h） | rule が promoted / archived された場合、同じ session 内で再生成可能（MVP では Issue-only なのでスキップ） |
| GitHub Actions: harness-pr-cleanup | archived が発生したらメッセージで次回 drift check に再生成を委譲 |
| **Databricks Workflow: harness-skill-drift-check（daily schedule）** | Databricks の promoted rule と repo の SKILL.md を JOIN（GitHub MCP 経由で repo 取得）、drift があれば再生成 PR を作る |

rule 本体は Databricks にあるため、main への push を trigger にしない。
GitHub MCP server が `claude/` prefix の branch への push と PR 作成を担当する（Claude GitHub App の権限内、`main` 直 push は branch protection で禁止）。生成 branch は `claude/harness-rules/<date>` を使う。

### 6.2 処理ロジック（pseudocode）

SKILL.md は Databricks の `rules` テーブル（status='promoted'）から生成:

```python
#!/usr/bin/env python3
# scripts/regenerate_skill_md.py
import re, sys, subprocess
from pathlib import Path
from collections import defaultdict

SKILLS_DIR = Path(".claude/skills/harness")
CATEGORY_RE = re.compile(r"^[a-z][a-z0-9-]{0,32}$")  # path traversal 対策

# Databricks connector / MCP tool 経由で SELECT する。
rows = databricks.query("""
    SELECT id, category, checklist_item
    FROM dev_bronze.test.harness_rules
    WHERE status = 'promoted'
    ORDER BY category, id
""")

by_category = defaultdict(list)
for r in rows:
    if not CATEGORY_RE.match(r.category):
        print(f"skip: invalid category {r.category} for id {r.id}", file=sys.stderr)
        continue
    # prompt injection 防御: < > ` を strip（SKILL.md 内に楽観的解釈されないように）
    item = re.sub(r"[<>`]", "", r.checklist_item)
    by_category[r.category].append({"id": r.id, "item": item})

# 既存の harness/ 配下を一旦全削除して再生成（drift を吸収）
if SKILLS_DIR.exists():
    subprocess.run(["rm", "-rf", str(SKILLS_DIR)], check=True)
SKILLS_DIR.mkdir(parents=True, exist_ok=True)
for category, lessons in by_category.items():
    skill_dir = SKILLS_DIR / category
    skill_dir.mkdir(exist_ok=True)
    content = ["---", f"name: harness/{category}",
               f"description: harness 自動生成のチェックリスト（{category}）",
               "---", "",
               f"# {category} ルール", ""]
    for l in lessons:
        content.append(f"- {l['id']}: {l['item']}")
    (skill_dir / "SKILL.md").write_text("\n".join(content) + "\n")

# commit + push（変更があれば）
subprocess.run(["git", "config", "user.name", "harness-bot"], check=True)
subprocess.run(["git", "config", "user.email", "harness-bot@every.tv"], check=True)
subprocess.run(["git", "add", str(SKILLS_DIR)], check=True)
r = subprocess.run(["git", "diff", "--cached", "--quiet"])
if r.returncode != 0:
    subprocess.run(["git", "commit", "-m", "chore(harness): regenerate SKILL.md from DB"], check=True)
    branch = f"claude/harness-rules/{run_date()}"
    subprocess.run(["git", "push", "origin", f"HEAD:{branch}"], check=True)
    github.create_or_update_pull_request(
        head=branch,
        base="main",
        title="chore(harness): regenerate SKILL.md from DB",
    )
```

セキュリティ強化のポイント:

- `CATEGORY_RE` で category 名を制約 → `mkdir` での path traversal 完全防止
- DB 取得時点で `WHERE status = 'promoted'` で絞るため、archived rule は構造的に SKILL.md に含まれない
- `checklist_item` 本文から `<`, `>`, `` ` `` を除去 → SKILL.md への prompt injection 防御
- 出力先を一旦全削除 → 再生成することで、DB 上で削除/archive された category の SKILL.md も自動的に消える（drift 解消）

### 6.3 配置

| 種類 | 配置 | 配布 |
|---|---|---|
| **harness-plugin（git 情報送信用）** | プラグイン | Server-managed Settings 経由で全社共通 |
| **SKILL.md（agent に見せるチェックリスト）** | 各 repo の `.claude/skills/harness/<category>/` | **repo に直接 commit**、repo 固有 |
| **rule 本体 / evidence / state** | Databricks `dev_bronze.test.harness_*` | repo には置かない（rule-model.md §5） |

`.claude/skills/harness/` namespace 配下に置くことで、developer が手書きで作った skill (`.claude/skills/<their-name>/`) と衝突しない。Databricks Workflow drift-check trigger から起動された Managed Agent はこの namespace のみを管理対象とする。

### 6.4 archive 時の扱い

DB で `rules.status = 'archived'` になった rule は、`WHERE status = 'promoted'` の SELECT で自動的に除外される。GitHub Actions cleanup trigger で archive を発生させた翌日の Databricks Workflow daily drift-check が SKILL.md 反映 PR を出す（cleanup と drift-check は別 trigger なので、最遅で 24h ラグあり）。

---

## 6.5 harness-allowlist-guard（GitHub Action）

agent-spec.md §10.5 で定義された **protected paths** への改竄を **事前 block** する required status check。PR / push の両 trigger に対応し、PR 内の **全 commit** を走査する。

**Protected paths**（agent-spec.md §10.5 と完全一致）:

- `.harness/secret-linter.allow`
- `.harness/secret-linter.config.yaml`
- `.harness/state/disabled`
- `.github/workflows/**`
- `scripts/secret_linter.py` / `scripts/sanitize.py`（linter / sanitize 関数本体）

```yaml
on:
  push:
    branches: [main]
  pull_request:
    branches: [main]

jobs:
  guard:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          # PR は merge_base から全 commit、push は before..after の range
          fetch-depth: 0
      - name: Determine diff range
        id: range
        run: |
          if [ "${{ github.event_name }}" = "pull_request" ]; then
            base="${{ github.event.pull_request.base.sha }}"
            head="${{ github.event.pull_request.head.sha }}"
          else
            base="${{ github.event.before }}"
            head="${{ github.event.after }}"
            # force-push / initial push 検出
            if [ "$base" = "0000000000000000000000000000000000000000" ]; then
              echo "::error::Initial push / force-push detected; require manual security-team review"
              exit 1
            fi
          fi
          echo "base=$base" >> "$GITHUB_OUTPUT"
          echo "head=$head" >> "$GITHUB_OUTPUT"
      - name: Check protected paths per-commit
        env:
          PROTECTED_PATHS: ".harness/secret-linter.allow .harness/secret-linter.config.yaml .harness/state/disabled .github/workflows scripts/secret_linter.py scripts/sanitize.py"
          BASE: ${{ steps.range.outputs.base }}
          HEAD: ${{ steps.range.outputs.head }}
        run: |
          set -euo pipefail
          # PR / push を問わず BASE..HEAD の全 commit を走査
          commits=$(git rev-list --reverse "$BASE..$HEAD")
          fail=0
          for sha in $commits; do
            changed=$(git diff-tree --no-commit-id --name-only -r "$sha" -- $PROTECTED_PATHS || true)
            if [ -z "$changed" ]; then continue; fi
            # author / committer の両方を確認
            author_email=$(git show -s --format='%ae' "$sha")
            committer_email=$(git show -s --format='%ce' "$sha")
            msg=$(git show -s --format='%s' "$sha")
            # bot identity 判定:
            # GitHub App commit の noreply email は `<app-id>+<app-slug>[bot]@users.noreply.github.com`
            # `[` `]` を regex で正しく escape し、`+` の前置も許容する
            is_bot=0
            if echo "$author_email $committer_email" | grep -Eq "(^|[+])harness-bot\[bot\]@|^harness-bot@"; then
              is_bot=1
            fi
            if [ "$is_bot" = "1" ]; then
              echo "::error::commit $sha by harness-bot modifies protected paths: $changed"
              fail=1
              continue
            fi
            # 人間 commit は CODEOWNERS で security-team レビュー必須なので、
            # prefix 要求は省く（reviewer の認知負荷を下げる）。
            # ただし audit log として、人間が触ったことを log に明示
            echo "::notice::commit $sha (human) modifies protected paths: $changed"
          done
          exit $fail
```

**重要**:

- `push` event でも `pull_request` event でも同じロジックが走り、両方を required check に登録する
- `BASE..HEAD` の **全 commit** を走査するため、中間 commit に保護パス変更を仕込むケースもカバー
- author / committer の **両方** を確認、bot identity の正規表現は `[`, `]` を escape して `<app-id>+harness-bot[bot]@users.noreply.github.com` 形式に対応
- force-push / initial push（`before = 0000...`）は **alert + fail**（security-team の手動審査が必要、bootstrap 経路は別途 `harness-bootstrap` label PR で対応）
- **squash merge の禁止は harness 系 PR のみに限定**（branch protection ruleset で `claude/harness-rules/**` 系 branch の PR のみ merge / rebase only）。他の通常 PR は squash 自由

repo 設定で main の **required status check** に `guard` を指定する。harness-bot は GitHub App として運用し、**branch protection bypass を与えない**（agent-spec.md §10.5 L1 参照）。

**Canary（死活監視、MVP は runbook ベース）**: pilot 中は security-team が **週次で手動チェック**（runbook で dummy PR を作って guard が reject することを目視確認）。独立 GitHub App による自動 canary + dead-man switch は **Phase 2**（agent-spec.md §10.5 / §12 参照）。

---

## 6.6 Observability（transcript / metrics / cost）

agent-spec.md §11.8 で規定する transcript / metrics / cost の収集経路。MVP では Managed Agent session の events.list を正本とし、
agent が run summary を Databricks に書く。

- **transcript**: Managed Agent の `/v1/sessions/{id}/events` (events.list) を session URL とともに Databricks `harness_run_metrics` に保存する。SDK event stream の外部収集は使わない。
- **metrics**: agent が Databricks `harness_run_metrics` に tool 呼び出し概数 / processed PR 数 / created rule 数 / failure reason を書く。token/cost の精密集計は OTel `dev_bronze.s3_otel_logs.api_request` の `cost_usd` / `*_tokens` 列で `session_id` JOIN により実現可能。
- **cost 閾値**: MVP は Anthropic API usage limits（rate limit / token quota）と runbook alert で運用する。自前 abort controller は Managed Agents の `user.interrupt` 経路で対応可能、必要になれば Phase 2 で実装。

詳細は agent-spec.md §11.8 を参照。

---

## 7. 採点（将来検討）

現時点では採点を持たない。理由:

- 実運用は人が回していくものであるため
- 評価シートでの観測だけでも、改善・削除ループは機能するため

導入するとしたら、

- judge ロールで verdict 分布や PR 差分を読み、レッスンの品質スコアを算出
- 自動マージの判断材料に使う

必要性が顕在化した段階で着手する。

---

## 8. 情報収集プラグインへの要件

ループ A の入力（情報収集）と、ループ C の入力（評価シート集約）の両方を、継続的・自動的に取れる仕組みが必要。

収集対象:

- PR / レビューコメント
- Databricks プロンプト
- **評価シート JSONL** — 削除ループに必須
- セッションログ等

特に **評価シート JSONL の収集経路** が抜けると、ループ C が回らずシステム全体が機能しなくなるので、ここを最初に整備する必要がある。
