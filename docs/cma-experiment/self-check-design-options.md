# Self-check 設計オプション比較 (Plan A / Plan B)

> 関連: [`rule-model.md`](./rule-model.md) §3.3 ライフサイクル / [`workflow.md`](./workflow.md) §3 ルール適用フロー / [`data-collection.md`](./data-collection.md) §1 / [`agent-spec.md`](./agent-spec.md) §3, §4.1 / [`probe-verification.md`](./probe-verification.md) §3

明日の社内ミーティング用、設計比較ドキュメント。harness rule の **self-check (評価シート / eval-sheet / checklist)** を「誰が・いつ・どこで」書くかについて、現時点で考えうる 2 案を比較整理する。

---

## 1. 背景と目的

### 1.1 これまでに decide / 実装されたこと

- harness rule の **`tier=2 / distributed=true`** を Claude Code に配布するところまでは「**設計済み + 配布経路は確定**」。
  - 配布経路: Claude.ai admin console の Server-managed Settings の `enabledPlugins` で plugin を強制有効化 ([`data-collection.md`](./data-collection.md) §4.2)。
  - Plugin の最小構成は [`data-collection.md`](./data-collection.md) §4 に既述。
- 評価シートの **データ形式 / verdict の意味 / 強制方法** は [`rule-model.md`](./rule-model.md) §3.2-3.3 で決定済み。
  - verdict は 3 種: `compliant` / `n_a` / `exception`（[`rule-model.md`](./rule-model.md) L208-210, L200）。
  - 「該当」と「効いた」を区別し、**`compliant` は「効いた」だけに使う**（[`rule-model.md`](./rule-model.md) L212）。
- **未着手**: 配布された rule が「**実際の PR で守られたか**」を集計する仕組み。
  - これが self-check (eval-sheet) の話。
  - 集計用テーブル `dev_bronze.test.harness_rule_check_results` を新設するスキーマ案を本書 §6 で詳述する（既存 Docs に該当テーブルの定義は無い）。

### 1.2 本書のスコープ

self-check を **誰が・いつ・どこで書くか** の 2 案を比較し、明日の議論で「**Plan A / Plan B / 併用**」のどれを採るかを決める材料にする。

未決事項は §7 にハイライト。執筆者の推奨案は §8。

---

## 2. Plan A: hook 主導 (push 前のリアルタイム判定)

### 2.1 フロー

```
開発者の Claude Code セッション
   │
   ├─ 実装作業
   │
   └─ git push / gh pr create を呼ぼうとする
        │
        ▼
      PreToolUse hook 発火
        │
        └─ verify-eval-sheet スクリプト実行
             │
             ├─ ローカル .claude/skills/harness/**/SKILL.md を glob で active rule 取得
             ├─ .harness/work/eval-sheet-<branch>.jsonl を検証
             │
             ├─ 不足あり → deny + template 提示 → agent が Write で記入 → 再 push
             └─ 全 active rule に verdict あり → push を通す
                  │
                  ▼
                Zerobus 経由で結果を Databricks に POST
                  │
                  ▼
                harness_rule_check_results に INSERT
                  （source='hook'）
```

この設計は [`rule-model.md`](./rule-model.md) §3.3 L220-230、[`workflow.md`](./workflow.md) §3 Step 4-5 (L264-285) でほぼ既述済み。**本書の追加点は「verdict 判定後の Zerobus POST → 集計テーブル」を明示的に組み込むこと**。

### 2.2 必要な実装

| # | 実装物 | 既存 / 新規 | 備考 |
|---|---|---|---|
| 1 | harness-plugin に PreToolUse Bash matcher 追加 | 設計済み・未実装 | [`data-collection.md`](./data-collection.md) §4.1 |
| 2 | `verify-eval-sheet` スクリプト本体 | 新規 | ローカル `.claude/skills/harness/**/SKILL.md` を glob で active rule 取得、`.harness/work/eval-sheet-<branch>.jsonl` を検証 |
| 3 | 不足時の deny + template 生成 | 新規 | deny メッセージにエージェントが次にやるべき作業を全部含める（[`workflow.md`](./workflow.md) L274） |
| 4 | 通過後の Zerobus POST スクリプト | 新規 | 既存 plugin で使用中の Zerobus token を流用 |
| 5 | `harness_rule_check_results` テーブル | 新規 | 本書 §6 に定義 |

### 2.3 前提

- **Zerobus token のみ**。既存 plugin で使用中のため新規認証配布不要。
- **SP (Service Principal) の発行は不要**。daichi に PAT 発行権限が無い現状でも進められる。

### 2.4 既存設計の根拠

- [`rule-model.md`](./rule-model.md) §3.3 L220-230: ライフサイクル（push 直前 PreToolUse hook をトリガに reactive に作成・記入）
- [`workflow.md`](./workflow.md) §3 Step 4-5 L264-285: hook が deny + template を返し、agent が自己修正する往復ループ
- [`data-collection.md`](./data-collection.md) §4: プラグインの最小構成（PreToolUse hook 同梱）

### 2.5 利点

- **即時性**: push 試行時に判定 → そのセッション中に修正が促せる。
- **修正促し**: 守ってないと push が通らない（deny 経路）。
- **全 repo install 後は自動運用**: Server-managed Settings の `enabledPlugins` 強制有効化で配布完了。
- **認証配布が軽い**: Zerobus token 1 系統で完結。

### 2.6 欠点

- **開発者の往復が発生**: deny → 記入 → 再 push の loop が走る。Step 4-5 の往復は agent が自動でやるが、最低 1 ラウンドは発生する。
- **全 repo に plugin install が必要**: 配布範囲が広がるほど onboarding コストが効いてくる。
- **開発者 (agent) の自己申告ベース**: hook が見るのは「agent が書いた verdict」なので、agent が雑に `n_a` を埋めれば signal が劣化する。

---

## 3. Plan B: agent 主導 (PR 作成後のバックグラウンド判定)

### 3.1 フロー

```
開発者の Claude Code セッション
   │
   └─ PR 作成（評価シートは書かれていない、または雑に書かれている）
        │
        ▼ （Bronze ingest 待ち、実測 ~5 日）
        │
   [trigger 層: Databricks Workflow notebook など]
        │
        ├─ PR diff を GitHub MCP / API で取得
        ├─ Claude Code 会話履歴を OTel logs (dev_bronze.s3_otel_logs.user_prompt 等) から取得
        ├─ active rule を Databricks から取得
        │
        └─ rule-creation agent (v10, maintain mode) を起動
             │
             └─ <document type="otel_log">, <document type="pr">,
                <document type="existing_rules"> を user message で投入
                  │
                  ▼
                agent が PR diff + 会話履歴 + active rule を見て
                自分で verdict (compliant / violation / n_a / exception) を判定
                  │
                  ▼
                harness_rule_check_results に INSERT
                  （source='agent'）
```

### 3.2 必要な実装

| # | 実装物 | 既存 / 新規 | 備考 |
|---|---|---|---|
| 1 | Databricks SQL MCP または connector の設定（OTel logs 取得経路） | 設計済み・未実装 | [`data-collection.md`](./data-collection.md) §3.2 Option B、SP の M2M OAuth 発行依頼が要 |
| 2 | trigger 層 (Databricks Workflow notebook) が pre-fetch して agent に user message として渡す | 設計済み・未実装 | [`agent-spec.md`](./agent-spec.md) §3 L86-97（構成 B: agent は JSON のみ出力、副作用は trigger 層） |
| 3 | rule-creation agent の prompt 拡張 | 新規 | PR 分析時に eval-sheet 判定も並行で行う責務追加 |
| 4 | `harness_rule_check_results` テーブル | 新規 | 本書 §6 に定義（Plan A と共有） |

### 3.3 前提

- **SP の M2M OAuth 発行依頼が必要**: 現状 daichi に PAT 発行権限なし。security-team / Databricks admin への依頼が必要 ([`data-collection.md`](./data-collection.md) §3.2 L137-156)。
- **OTel logs は既に流れている**: `dev_bronze.s3_otel_logs.user_prompt` に到達済み ([`probe-verification.md`](./probe-verification.md) §2 L41 で確認済み)。
- **session_id ↔ repo / PR の紐付けも確認済み**: `dev_bronze.test.harness_session_context` 経由で `(git_remote_origin, pr_number)` から session_id 逆引き可能 ([`agent-spec.md`](./agent-spec.md) §5 L259-265)。

### 3.4 制約: Bronze 取り込み遅延

**実測 5 日 ([`probe-verification.md`](./probe-verification.md) §3 L128-133)**。

- S3 → Bronze 取り込みに約 4 日（probe 時の実測値は 4 日、ただし運用上 5 日見ておく）。
- **PR 作成直後の即時判定は不可**。
- [`agent-spec.md`](./agent-spec.md) §5 L247-251 でも 4 日 cooldown を実装している（`age < timedelta(days=4): return`）。

### 3.5 既存設計の根拠

- [`agent-spec.md`](./agent-spec.md) §3 L86-97: 集計ソースは trigger 層が pre-fetch して agent に渡す（構成 B）
- [`agent-spec.md`](./agent-spec.md) §4.1 L100-112: GitHub MCP + Databricks SQL MCP で PR / OTel / rule を取得
- [`agent-spec.md`](./agent-spec.md) §5 L223-275: pseudocode、session_id 逆引きと OTel SELECT 経路
- [`agent-spec.md`](./agent-spec.md) §6.2 L506-509: `<document type="otel_log">` セクションで OTel ログを agent に投入する I/O 形式
- [`agent-spec.md`](./agent-spec.md) §13 L1152: OTel スキーマ全 5 テーブルが session_id JOIN 可能

### 3.6 利点

- **開発者の手間ゼロ**: hook 経由の往復が発生しない。
- **全 repo install 不要**: agent が中央で動くだけで済む。配布範囲が狭くて良い。
- **agent の判断で補正可能**: 自己申告ではなく、PR diff + 会話履歴を見て agent が独立に verdict を判定する。雑な `n_a` を補正できる。

### 3.7 欠点

- **5 日遅延**: PR 作成直後には verdict が出ない。「今のセッションを直す」用途には使えない。
- **修正促しなし**: 後追い分析なので、開発者の挙動を変えるフィードバックループにならない（reviewer への通知設計が別途必要、§7 参照）。
- **SP 待ち**: M2M OAuth 発行が完了するまで動かせない。

---

## 4. 比較表

| 観点 | Plan A (hook 主導) | Plan B (agent 主導) |
|---|---|---|
| **判定タイミング** | push 試行時（即時） | PR 作成から **5 日後** (Bronze ingest 待ち) |
| **開発者の手間** | 最低 1 ラウンドの自己修正（agent が自動） | ゼロ |
| **修正促し** | あり（deny で push をブロック） | なし（後追い分析） |
| **必要な認証** | Zerobus token のみ（既存） | SP の M2M OAuth（**daichi に発行権限なし、依頼が必要**） |
| **配布範囲** | 全対象 repo に plugin install 必須 | 中央 trigger 層のみ |
| **既存 PoC** | hook 周りは [`probe-verification.md`](./probe-verification.md) で実証済み（session_id 取得 / JOIN は ✅） | OTel テーブル / `harness_session_context` JOIN は実証済み ([`probe-verification.md`](./probe-verification.md) §2)、agent から MCP 経由の SELECT は未実証 |
| **誰が verdict を書くか** | 実装エージェント（自己申告） | rule-creation-agent（第三者判定） |
| **判断材料** | active rule + 自分のセッション context | PR diff + 会話履歴 (OTel) + active rule |
| **`source` 列の値** | `'hook'` | `'agent'` |
| **遅延に対する許容性** | 不要（同期的） | 5 日遅延を許容できる用途のみ |
| **MVP 着手の難度** | 低（Zerobus token のみ） | 中〜高（SP 発行待ち + trigger 層 + agent prompt 拡張） |

---

## 5. 併用パターン

Plan A と Plan B は **`source` 列で区別すれば併用可能**。

### 5.1 併用フロー

1. **push 前**: Plan A で開発者の自己申告を取り、即座に Zerobus POST → `source='hook'` で INSERT
2. **5 日後**: Plan B で agent が OTel logs を見て補正 → `source='agent'` で INSERT (新規行、`event_timestamp` を上書きせず append-only)
3. **集計時**: SQL で `(session_id, rule_id)` の **最新を採用** するロジックを書けば、後追い補正が反映される

### 5.2 集計 SQL のイメージ

```sql
-- 最新 verdict を採用（agent 補正があれば agent を、なければ hook を）
WITH ranked AS (
  SELECT *,
         ROW_NUMBER() OVER (
           PARTITION BY session_id, rule_id
           ORDER BY event_timestamp DESC, source DESC
         ) AS rn
  FROM dev_bronze.test.harness_rule_check_results
)
SELECT * FROM ranked WHERE rn = 1;
```

`ORDER BY ... source DESC` により、同一 timestamp 時は `hook` より後の文字列順 `agent` が優先される（必要ならルール固定の優先度列を別に持つ）。

### 5.3 併用の利点

- 即時性 (Plan A) と **質の高い verdict** (Plan B) の両取り
- agent 補正は append-only で残るので、後から監査・rollback 可能
- どちらか片方の障害でも集計が止まらない

### 5.4 併用の欠点

- 実装物が両方必要 → コストは Plan A + Plan B
- `(session_id, rule_id)` の dedup ロジックを忘れると重複カウントになる

---

## 6. `harness_rule_check_results` テーブルスキーマ案

```sql
CREATE TABLE dev_bronze.test.harness_rule_check_results (
  id                 BIGINT NOT NULL GENERATED ALWAYS AS IDENTITY,
  event_timestamp    TIMESTAMP NOT NULL,
  session_id         STRING NOT NULL,
  git_remote_origin  STRING NOT NULL,
  git_branch         STRING,
  commit_sha         STRING,
  pr_number          BIGINT,
  rule_id            BIGINT NOT NULL,
  rule_name          STRING NOT NULL,
  verdict            STRING NOT NULL,    -- compliant / violation / n_a / exception
  note               STRING,
  source             STRING NOT NULL     -- 'hook' or 'agent' (Plan A / B どちらが書いたか)
) USING DELTA
```

### 6.1 列の解説

| 列 | 必須 | 解説 |
|---|---|---|
| `id` | yes | IDENTITY、append-only の primary key |
| `event_timestamp` | yes | 判定時刻。Plan A は push 試行時、Plan B は agent 判定時 |
| `session_id` | yes | OTel `dev_bronze.s3_otel_logs.*.session_id` と join 可能 |
| `git_remote_origin` | yes | `harness_session_context` と同じ表記（[`probe-verification.md`](./probe-verification.md) §2 L106-107） |
| `git_branch` | no | hook 経路では取れる、agent 経路では PR head ref から推定 |
| `commit_sha` | no | push 試行時の HEAD（Plan A）/ PR head_sha（Plan B） |
| `pr_number` | no | PR 作成前の push では NULL になりうる |
| `rule_id` | yes | `dev_bronze.test.harness_rules.id` への論理 FK |
| `rule_name` | yes | 可読性向上のため snapshot を残す（rule 改名対応） |
| `verdict` | yes | 4 種: `compliant` / `violation` / `n_a` / `exception` |
| `note` | no | `exception` の場合の理由など（[`rule-model.md`](./rule-model.md) §3.2 L201 の `reason` 相当） |
| `source` | yes | `'hook'` (Plan A) / `'agent'` (Plan B) |

### 6.2 既存 verdict 定義との関係

[`rule-model.md`](./rule-model.md) §3.2 L206-210 は 3 種 (`compliant` / `n_a` / `exception`) を定義している。本テーブルは **`violation` を追加した 4 種**:

| verdict | 既存定義 (`rule-model.md`) | 本テーブルでの扱い |
|---|---|---|
| `compliant` | 該当・効いた | そのまま |
| `n_a` | 該当しないか、該当したが影響なし | そのまま |
| `exception` | 該当するが理由ありで意図的に外した | そのまま、`note` に reason |
| `violation` | （定義なし） | **新規**: 該当したが守らなかった（特に Plan B で agent が判定する場合に必要） |

> **未決**: `violation` を加えるか、`exception` の `reason` で「意図的かどうか」を表現するかは §7 で議論したい。

### 6.3 既存テーブルとの関係

- [`rule-model.md`](./rule-model.md) §5 で定義済みの `harness_rules` / `harness_rule_evidence` / `harness_processed_prs` / `harness_run_metrics` とは **別物**。
- 本テーブルは「**configured rule が実際の PR で守られたかどうか**」の **観測ログ**。

---

## 7. 未決事項 (明日の議論材料)

| # | 論点 | 補足 |
|---|---|---|
| 1 | **どっちを採るか / 両方やるか** | §8 の推奨案は Plan A 先行 + 後で Plan B 併用 |
| 2 | **Plan A の plugin 配布をどの repo から始めるか** | `everytv/harness-rule-test` で実証 → 順次社内 repo に展開、が妥当か? |
| 3 | **Plan B の OTel 取得経路** | SP 発行依頼を進めるか、別の手段 (例: U2M OAuth refresh) を取るか。**ただし [`data-collection.md`](./data-collection.md) L137 で「SSO と narrow-scope SP は本質的に両立しない」と既決**。U2M に逃げる場合は権限スコープが緩む点を承知の上で判断する必要あり |
| 4 | **verdict の値** | `compliant` / `violation` / `n_a` / `exception` の 4 種で良いか。既存 [`rule-model.md`](./rule-model.md) §3.2 は 3 種なので、`violation` の追加是非を要確認 |
| 5 | **5 日遅延の許容** | agent 主導の自動補正が 1 週間後でも価値があるか。Plan B 単独だと「直近 PR の改善」には使えないが、長期 signal としては有効 |
| 6 | **「rule が守られていない PR」の reviewer への明示方法** | PR body? Slack 通知? GitHub Actions check? どこに出すか未決 |
| 7 | **`source` 列の値追加余地** | 今後 CI / pre-push hook など別経路が出た時に拡張するか、enum を縛るか |

---

## 8. 推奨案 (執筆者意見)

### 8.1 MVP: **Plan A から先行実装**

理由:

- **Zerobus token と既存 plugin で完結**。SP 発行待ちを回避できる。
- **即時性と修正促しが得られる**。これは Plan B では得られない価値。
- **既存設計の延長線**: [`rule-model.md`](./rule-model.md) §3.3 と [`workflow.md`](./workflow.md) §3 で既に hook 主導のライフサイクルが書かれており、self-check はその自然な拡張になる。

進め方:

1. `everytv/harness-rule-test` で動作確認
2. 1 件でも実 PR で deny / pass のループが回ったら社内 repo に順次展開
3. 配布は Server-managed Settings の `enabledPlugins` ([`data-collection.md`](./data-collection.md) §4.2)

### 8.2 拡張: SP が来たら Plan B も併用

理由:

- 5 日遅延でも **「ぼーっとして書いた eval-sheet を補正してくれる」価値はある**。
- agent 視点の verdict は **自己申告バイアスを補正**できる。長期 signal として `compliant + exception` のカウント精度が上がる ([`rule-model.md`](./rule-model.md) L214: 効いた回数の長期集計に効く)。
- 集計 SQL を §5.2 のように書けば、`source='agent'` 行が後から append されても自然に最新が採用される。

### 8.3 進めない判断もありうる

以下のケースでは Plan A 単独 / Plan B 見送りもアリ:

- SP 発行が今後数 ヶ月 来ない見込みなら、Plan B の設計だけ凍結
- Plan A の self-report 精度が十分高ければ、Plan B の補正が overkill になる可能性

---

## 9. 結論

**明日の議論で決めたいこと**:

1. **Plan A 先行 + 将来 Plan B 併用** で進めて良いか (§8.1, §8.2)
2. `harness_rule_check_results` のスキーマ（§6）を確定させて良いか
3. verdict に `violation` を加えるか (§6.2, §7-4)
4. Plan A の最初の配布対象 repo (§7-2)
5. SP 発行依頼を並行で始めるか (§7-3)

未決のまま残る項目（reviewer 通知方法 §7-6 など）は MVP 後に再議論する想定。

---

## 10. 確定仕様（Plan B 実装版 / 2026-05-27 追記）

> 本節は §1-§9 の設計比較を踏まえて **実装・E2E 検証で確定した self-check の仕様** を記録する。§6-§7 の未決論点（特に §7-4 の `violation` 追加、verdict 種別）は本節で確定とする。記述が §1-§9 と食い違う場合は **本節 §10 を最新の確定方針** とする。

self-check は **Plan B（rule-creation-agent が第三者判定）を先行実装** した。専用 subagent `harness-rule-selfcheck-experimental`（`agent_01UNMsb56AQzWrNEuEP9DmYB`）を新設し、main coordinator が delegate する。

### 10.1 verdict 4 種（確定）

§6.2 の論点を確定。verdict は **4 種**:

| verdict | 意味 |
|---|---|
| `compliant` | rule が該当する場面があり、守っていた |
| `violation` | rule が該当する場面があったが、守らなかった |
| `exception` | rule が該当したが、正当な理由で従わなかった（その状況では従わない方が適切だった） |
| `n_a` | rule が該当する場面がなかった |

各 verdict に `reason`（根拠）を必ず付ける。`violation` は「なぜ守られなかったか」（rule が不明確 / skill の配置・記述が原因で参照されなかった / 単純な見落とし 等）を具体的に書く。憶測で `violation` を付けず、該当場面が読み取れなければ `n_a` とする。

### 10.2 critical 判定（確定）

各 rule に `critical`（boolean）を付ける:

- `critical=true`: **rule 自体が致命的に問題**（事実誤認の指示 / 従うと有害 / 他 rule と矛盾）。→ maintain step 0 で即 `tier=0`（archive）にする。
- `critical=false`: 上記以外。特に「skill の配置・記述が悪くて読まれなかった」は **rule の問題ではなく配布の問題** なので `critical=false` とし、reason に「配置問題」と明記する（rule を消すのではなく再配布で直すべきケース）。

### 10.3 実行タイミング（確定）

self-check は **maintain の step 0**（既存 step 1 の rule 変更処理の前）で main が delegate する:

1. PR URL → PR 番号
2. `harness_session_context` で PR ↔ session_id を照合（`git_remote_origin` + `pr_number`）。見つからなければ **graceful skip**（session 不明 = 評価不可。error ではない）。
3. 見つかれば: `harness_otel_user_prompt` から会話を `event_sequence` 順に取得 + tier=3 active rule + PR diff を XML で self-check subagent に渡す。
4. 返った results を `harness_rule_check_results` に INSERT（`source='agent'`）。
5. `critical=true` の rule は即 `UPDATE harness_rules SET tier=0`。

rule 変更前に評価するのは、変更後の rule 集合ではなく **その PR 実装時に配布されていた rule** に対する遵守を測るため。

### 10.4 集計判定（Phase 2）

集計による tier=0 判定（rule 自体の品質シグナル）:

> **(violation + exception) / (compliant + violation + exception) > 0.2** かつ **該当総数（n_a を除く）>= 10** → tier=0

`n_a` は分母から除外する（該当場面が無いのは rule の良し悪しと無関係）。この集計判定は per-rule で十分なサンプルが溜まってから効かせる Phase 2 機能。MVP（step 0 の記録 + critical 即 archive）とは独立。

### 10.5 再配布の枠組み

- **配置問題**（skill が読まれなかった等、`critical=false` の `violation`）: rule 自体は正しいので消さない。reason に「配置問題」を記録し、`distributed=false` に戻して distribute が再配置する。
- **MVP では reason 記録のみ**。自動再配布（distributed=false への自動巻き戻し）は Phase 2。

### 10.6 実テーブルとの差分（§6 スキーマからの確定変更）

実装した `dev_bronze.test.harness_rule_check_results` は §6 案から以下を変更:

- `note` → **`reason`**（[`rule-model.md`](./rule-model.md) §3.2 の `reason` 命名に統一）
- **`critical BOOLEAN` を追加**（§10.2）
- **`created_at TIMESTAMP` を追加**（行の物理作成時刻。`event_timestamp` は判定時刻）
- `commit_sha` は MVP では未使用（列なし）

### 10.7 E2E 検証結果（2026-05-27）

実データ session `d7c07d87-...`（delish-web2 PR #4386: 構造化データ改善）+ tier=3 fixture 3 件で単体検証:

- rule 27 `structured-data-spec-verification` → **compliant**（Google 推奨 3 アスペクト比の image 配列を実装）
- rule 28 `iso8601-for-structured-data-dates` → **compliant**（ISO 8601 getter を分離して JSON-LD に渡す）
- rule 29 `verify-node-version-before-deploy` → **n_a**（Node 版問題は別 repo/別サービス = Lambda resizer の話で本 PR の対象外）

verdict + reason + critical が期待どおり返り、`harness_rule_check_results` への記録も成功。maintain step 0 の graceful skip（session_context が空の PR）も確認済み。
