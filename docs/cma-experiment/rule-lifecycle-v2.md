# Rule ライフサイクル v2（方針変更の設計基盤）

> 関連: [`rule-model.md`](./rule-model.md) / [`workflow.md`](./workflow.md) / [`agent-spec.md`](./agent-spec.md) / [`self-check-design-options.md`](./self-check-design-options.md)

harness rule システム（PR レビューの指摘を rule 化し、Claude Code に SKILL.md として配布する仕組み）の大きな方針変更を文書化する。本書は明日の実装の設計基盤であり、確定事項（decided）/ 未実装（todo）/ 実測値（measured）を明確に区別して記載する。

本書は新規設計のための独立文書であり、既存 Docs は編集していない。既存 Docs と本書で記述が食い違う箇所（tier 上限、distribute の 1 PR まとめ、SKILL.md の category 単位 1 ファイルなど）は、**本書の v2 を最新の確定方針**とする。

---

## 1. 概要 / 変更の狙い

### 1.1 旧方針（v1）と新方針（v2）の対比

| 項目 | 旧（v1） | 新（v2） |
|---|---|---|
| distribute mode の PR 粒度 | 全 rule をまとめて 1 PR | **1 rule = 1 PR** |
| tier 上限 | 2 | **3** |
| 配布条件 | tier=2 | **tier=3** |
| SKILL.md の構造 | category 単位 1 ファイル（rule を列挙） | **category=skill + `rules/` 個別ファイル**（SKILL.md は rule を列挙しない静的 index） |
| harness 中間階層 | あり（`.claude/skills/harness/<category>/`） | **廃止**（普通の Claude Code skill 方式 `.claude/skills/<category>/`） |
| review の打ち切り扱い | max_iter_reached は採用扱い | **保留（採用しない）。Issue も上げない** |
| 抽出個数の判断 | （回数で判断する余地） | **0〜N 個。回数で判断しない**（繰り返し性は tier 昇格で後段測定） |

### 1.2 狙い

- **ルール作成の厳格化**: 本当に重要な rule だけを配布する。tier=3（3 回観測）まで引き上げ、review の bias を保守的にし、computational sensor（linter 等）で拾えるものを rule にしない。
- **個別 review 可能**: 1 rule 1 PR にすることで、reviewer は rule ごとに merge（採用）/ close（却下）を判断できる。
- **コンフリクト回避**: 1 rule 1 ファイル + SKILL.md を静的にすることで、別々の PR が別ファイルを触り、Git コンフリクトが起きない。

---

## 2. Skill ファイル構造（decided）

### 2.1 ディレクトリ構成

```
.claude/skills/<category>/
├── SKILL.md            ← 静的 index。rule を列挙しない。
│                          「このカテゴリの作業時は rules/ 配下を全部読んでチェックせよ」と明示する。
└── rules/
    ├── <rule-name>.md  ← 1 rule = 1 ファイル
    ├── <rule-name>.md
    └── ...
```

- harness 中間階層（`.claude/skills/harness/<category>/`）は**廃止**し、普通の Claude Code skill 方式（`.claude/skills/<category>/`）に合わせる。
- SKILL.md は **rule を列挙しない**。rule 追加で SKILL.md を編集しなくて済むため、SKILL.md がコンフリクトしない。SKILL.md には「このカテゴリの作業をするときは `rules/` 配下のファイルを全部読み、それぞれのチェック項目を満たしているか確認せよ」という静的な指示だけを書く。

### 2.2 SKILL.md の役割（静的 index）

- `description` frontmatter に「いつこの skill をロードすべきか」を書く（Claude Code の skill 選択ロード判定に使われる）。
- 本文は固定で、`rules/` を全件読めという指示のみ。rule の追加・削除で本文は変わらない。

### 2.3 各 rule ファイルの構造

各 rule ファイル `rules/<rule-name>.md` は以下を持つ。

- **frontmatter**: `rule`（rule 名）/ `when`（適用条件、フィルタ用）/ `tier`
- **本文**: 守ること（checklist_item を命令形 = Guide として）/ 問題（problem）/ ガイダンス（guidance）/ 例（examples）

```markdown
---
rule: no-temp-artifact-commit
when: コミット対象に一時ファイル/生成物が含まれうるとき
tier: 3
---

# 一時ファイル・生成物をコミットしない

## 守ること
- 〜を確認してからコミットせよ（命令形）

## 問題
- ...（problem）

## ガイダンス
- ...（guidance）

## 例
- OK: ...
- NG: ...
```

### 2.4 運用上の上限

- **1 category は 20〜30 rule まで**に抑える運用とする。肥大化したらサブ category に分割する。
- 根拠は §3 の read コスト実測。

### 2.5 根拠（Claude Code Skill 機構）

- Claude Code の Skill 機構は **SKILL.md という固定名** + progressive disclosure（必要になったときに本文・配下ファイルを読む）。
- 複数 skill は `description` で選択ロードされる。category=skill にすることで、その category の作業時にだけ該当 skill がロードされる。
- SKILL.md → `rules/` 全件 read という progressive disclosure に乗せることで、rule を増やしても SKILL.md 本文を変えずに済む。

---

## 3. read コストの実測結果（measured / 根拠）

category skill ロード時に `rules/` を全件 read するコストの実測。category を 20〜30 rule に保つ運用上限の根拠。

| ファイル数 | 概算トークン | 1 往復並列 read | 負荷 |
|---|---|---|---|
| 20 | 5〜6.5K | 可（実測） | 軽い |
| 50 | 13〜16K | 可だが冗長 | 中（実用上限） |
| 100 | 26〜33K | 非現実的 | 重い |

- 全件 read は category を **20〜30 rule** に保てば快適。
- harness が既読ファイルを **"Wasted call" でデデュープ**するため、同一セッション内の重複 read はコンテキストを二重に消費しない。
- **50 超**になったら `when:` frontmatter でフィルタするか、サブ category に分割する。

---

## 4. PR 運用（1 rule 1 PR / decided）

### 4.1 基本ルール

- **1 rule = 1 PR**。
- branch 命名: `claude/harness-rule/<rule-name>`
- **merge = 採用 / close = 却下**（旧チェックリスト方式は廃止）。

### 4.2 close（却下）時の流れ

1. reviewer が PR にコメントで**却下理由**を記載する。
2. agent がそのコメントを読んで **DB に反映**する（tier や状態の更新）。

### 4.3 PR description に書く内容

rule の詳細を以下のフィールドで記載する。

- problem
- guidance
- examples
- rationale
- source PR URL（その指摘が観測された元 PR）

### 4.4 コンフリクトしない理由

- 1 rule 1 ファイルなので、別々の PR が**別ファイル**を触る → Git コンフリクトしない。
- SKILL.md は静的なので rule 追加で編集されない → ここもコンフリクトしない。

---

## 5. tier 体系（-1 〜 3 / decided）

### 5.1 tier の定義

```
tier -1 = rejected    review が却下。再抽出しない（archived_skip 照合の対象）
tier  0 = archived     一度採用されたが廃止 / 削除確定
tier  1 = candidate    1 回観測
tier  2 =              2 回観測
tier  3 = promoted     3 回観測 → SKILL.md に配布
```

| tier | 名称 | 意味 | 配布 |
|---|---|---|---|
| -1 | rejected | review が却下。再抽出しない（archived_skip 照合の対象） | × |
| 0 | archived | 一度採用されたが廃止 / 削除確定 | × |
| 1 | candidate | 1 回観測 | × |
| 2 | — | 2 回観測 | × |
| 3 | promoted | 3 回観測 → SKILL.md に配布 | ✅ |

### 5.2 昇格と配布

- 昇格は観測ごとに **+1**: `tier = LEAST(tier + 1, 3)`
- 配布条件: **tier=3 のみ**（旧 tier=2 から引き上げ = 厳格化）。
- **3 つの別 PR**で同じ指摘が観測されて初めて配布される。

---

## 6. maintain モード（rule 抽出）の改訂（decided）

### 6.1 抽出個数

- **0〜N 個**。**回数で判断しない**（繰り返し性は tier 昇格で後段測定する）。

### 6.2 採用基準

- レビューコメントで指摘された問題。
- diff から読める semantic な問題。
- いずれも**一般化できる**もの。

### 6.3 除外基準

| 除外対象 | 理由 |
|---|---|
| linter / formatter / type checker / test で機械検出できるもの | computational sensor の役目。rule にしない（§9 参照） |
| PR 固有の事情 | 一般化できない |
| 1 回限りのもの | 再現性がない |
| 個人の好み | 一般性・客観性がない |

### 6.4 粒度

- 「**次に同種 PR を書く人がこれを読めば同じ指摘を避けられる**」レベル。

### 6.5 情報源の優先順位

```
レビューコメント  >  diff の変更意図  >  PR body
```

### 6.6 生成フィールド

| フィールド | 内容 |
|---|---|
| `name` | rule 名 |
| `category` | skill = category |
| `checklist_item` | **命令形で行動誘導**（= Guide として書く） |
| `problem` | 問題の説明 |
| `guidance` | 詳細ガイダンス |
| `examples` | OK / NG の具体例 |
| `skill_description` | いつ参照すべきか（Claude Code のロード判定用、§8 で DB カラム化） |
| `rationale` | 採用根拠 |

### 6.7 classification

| classification | 意味 |
|---|---|
| `new` | 新規 rule |
| `update` | 既存 rule の更新 |
| `duplicate` | 既存 active rule と重複 |
| `archived_skip` | **tier<=0 の rule と類似**（rejected / archived と被るので抽出しない） |
| `merge` | 既存 rule に統合 |

---

## 7. review subagent との往復（decided）

### 7.1 フロー

各候補を review subagent に delegate し、verdict を受け取る。

| verdict | 処理 |
|---|---|
| `approve` | 採用へ進む |
| `revise` | `suggested_revision` を反映して**再 delegate** |
| `reject` | **tier=-1** で `harness_rules` に記録（name / problem / rationale に却下理由）。再抽出を防ぐ |

### 7.2 iteration 上限

- 上限 **N=5 iteration**。
- 5 到達で approve に至らなければ**打ち切り**、**Issue は上げない**（採用しない、保留）。
- これは旧 v10 の「max_iter_reached は採用扱い」を**覆す**確定変更。

### 7.3 review criteria（厳格化）

| criteria | 内容 |
|---|---|
| 一般性 | PR 固有でなく一般化できるか |
| 再現性 | 繰り返し起こりうるか |
| 明確性 | 指示が明確で行動可能か |
| 重複・矛盾 | 既存 active rule と重複・矛盾しないか |
| 粒度 | 同種 PR を書く人が読んで避けられる粒度か |
| computational 除外 | **linter で拾えるものを弾く** |
| bias | **保守的**。重要でなければ reject |

---

## 8. DB スキーマ変更点（一部 todo）

### 8.1 変更点

- `harness_rules` に **`skill_description STRING`** カラムを追加（when_to_use。maintain 時に agent が生成）。**【todo】**
- `tier` カラムに **-1 を許容**する。CHECK 制約があれば緩和、なければそのまま。**【todo / 確認要】**

### 8.2 維持する既存カラム

以下の既存カラムは維持する。

```
id / name / tier / evidence_count / category / checklist_item /
problem / guidance / examples / rationale / source_pr_url /
created_at / updated_at / distributed / distributed_at / distribution_pr_url
```

---

## 9. Martin Fowler "Harness Engineering" との対応（補強）

本方針は Martin Fowler の "Harness Engineering" の枠組みに対応づけられる。

| 本システムの要素 | Harness Engineering の概念 | 性質 |
|---|---|---|
| rule（SKILL.md / `rules/`） | **Guide** | feedforward（行動前に誘導） |
| eval-sheet 検証 / review | **Sensor** | feedback |
| 「linter で拾えるものは rule にしない」 | **Computational sensor** と **Inferential guide** の切り分け | — |
| tier 昇格 | 「繰り返す問題だけ制御化する」の実装 | — |

- 出典: https://martinfowler.com/articles/harness-engineering.html

---

## 10. 未実装 / 次のステップ（todo）

| 項目 | 内容 |
|---|---|
| DB スキーマ変更 | `skill_description` 追加（§8） |
| agent prompt v11 | maintain 抽出の改訂（§6）/ review criteria（§7.3）/ tier=3（§5） |
| distribute の 1 rule 1 PR 化 | 現行は全 rule まとめ 1 PR（§4） |
| close 理由の読み取り | reviewer の close コメントを agent が読んで DB 反映（§4.2） |
| 実機確認 | Claude Code が category skill をロードして `rules/` を全件読むか（別セッションで要テスト、§2 / §3） |
