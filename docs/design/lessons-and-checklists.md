# Lessons と checklist 生成設計

この文書は、learned policy の第一段階を `lesson` format と checklist 自動生成に
整理するための設計メモである。

現行実装では `rules-registry.jsonl` / `rules/*.md` という名前が残っている。
ただし、今後の概念名は `rule` ではなく `lesson` とする。
`rule` は Claude / Codex / lint の rule と衝突しやすいため、学習された知見の
source of truth を `lesson` と呼ぶ。

## 方針

第一段階では hooks を作らない。

まずは次の 2 つだけを作る。

1. `lesson` format
2. `lesson` から生成される `checklist.md`

通常の実装 agent には `checklist.md` だけを見せる。
`lesson` 本体は、checklist だけでは判断できない場合、judge が根拠を確認する場合、
新しい lesson を作る時に既存 lesson へ統合できないか判断する場合に読む。

## 配置

repo-specific な learned policy は対象 repo の `.harnest/` に置く。

```text
.harnest/
├── checklist.md
├── work/
│   └── checklist-result.md
└── lessons/
    ├── no-temp-artifact-commit.md
    └── preserve-public-api.md
```

`lessons/<id>.md` の file stem を lesson ID とする。
front matter に `id` は持たせない。

理由:

- checklist から `lessons/<id>.md` を直接探せる
- 人間も agent も参照先を推測しやすい
- ID とファイル名の不一致を防げる

## Checklist format

checklist は薄く保つ。
各項目はチェックボックス、内容、対応 ID だけを持つ。

```markdown
# Checklist

- [ ] `no-temp-artifact-commit` 作業用ファイルや一時ファイルを実装差分に含めない
- [ ] `preserve-public-api` 既存 API の公開 contract を理由なく変えない
```

対応 ID は `lessons/<id>.md` に解決する。
引用や詳細確認が必要な場合は、その lesson file を開く。

checklist には原則として以下を入れない。

- 問題の背景
- 長い根拠
- 詳細な例外条件
- 新規 lesson との統合判断材料
- automation / lint / hook の候補情報

これらはすべて lesson 側に置く。

## Checklist result format

`checklist.md` は参照用であり、作業結果は `.harnest/work/checklist-result.md` に
記録する。

作業開始時に `checklist.md` をコピーし、agent は各 item の `[ ]` を次のどれかへ
置き換える。

| marker | verdict | 意味 |
| --- | --- | --- |
| `[x]` | `compliant` | 該当し、確認済み / 対応済み |
| `[-]` | `n_a` | 今回のタスクには該当しない |
| `[!]` | `exception` | 該当するが正当な例外として外す |
| `[ ]` | unresolved | 未確認。PR 前 hook / verify では失敗 |

`[!]` は直下に `reason:` を必須とする。
`[x]` と `[-]` の理由は任意でよい。

```markdown
# Checklist

- [x] `no-temp-artifact-commit` 作業用ファイルや一時ファイルを実装差分に含めない
- [-] `preserve-public-api` 既存 API の公開 contract を理由なく変えない
- [!] `some-lesson` 例外として外した
  reason: このタスクは意図的に公開 API を変更するため
```

provider hook は provider 固有設定に直接判定ロジックを書かず、
`harnest lessons verify-checklist-result` を呼ぶ。
これにより、Claude / Codex / CI で同じ判定を使える。

## Lesson format

lesson は長めの背景や判断材料を持つ source of truth である。
通常の実装 agent に常時読ませる文章ではない。

```markdown
---
status: active
severity: high
confidence: high
category: git-hygiene
---

# no-temp-artifact-commit

## Checklist Item

作業用ファイルや一時ファイルを実装差分に含めない

## Problem

実装 agent が `checklist.md` や作業用ファイルを成果物として commit し、
本来の実装差分に不要な artifact が混入した。

## Evidence

- run: `2026-04-21-PR42-abcdef0`
- judge finding: 作業用 checklist が diff に含まれていた
- affected path: `checklist.md`

## Guidance

作業用ファイルは worktree root に置いてよいが、commit 前に `git status` を確認し、
成果物ではないファイルを staged / committed に含めない。

## Exceptions

タスク自体が docs / checklist 更新を要求している場合は対象外。

## Merge Notes

同じ「一時ファイル混入」系の lesson が出たら、新規作成よりこの lesson への追記を優先する。
```

## Lesson fields

front matter は絞る。

| field | 必須 | 用途 |
| --- | --- | --- |
| `status` | yes | `active | deprecated | archived` |
| `severity` | yes | `critical | high | medium | low` |
| `confidence` | yes | `high | medium | low`。誤検知しやすさを区別する |
| `category` | yes | 統合候補の探索や一覧表示に使う大分類 |

本文 section は次を標準形とする。

| section | 必須 | 用途 |
| --- | --- | --- |
| `# <id>` | yes | file stem と同じ ID を見出しにする |
| `Checklist Item` | yes | checklist に出す短い文 |
| `Problem` | yes | lesson を作る原因になった問題 |
| `Evidence` | yes | PR / run / diff / judge 指摘などの根拠 |
| `Guidance` | yes | checklist だけで判断できない時の詳細 |
| `Exceptions` | no | 例外条件 |
| `Examples` | no | OK / NG の具体例 |
| `Merge Notes` | yes | 新規 lesson 作成時の統合判断材料 |
| `Automation` | no | 将来 lint / test / hook 化できるか |

## Checklist 生成

`checklist.md` は active lessons から自動生成する。
Phase 1 で生成に使う主情報は、file stem の ID、front matter の `severity`、
本文の `Checklist Item` である。

生成方針:

- `status=active` の lesson だけを対象にする
- Phase 1 では active lesson を全件出す
- `severity` と ID で並び替える
- checklist は短く保つ
- checklist には詳細本文を入れない
- 各 item は ID から `lessons/<id>.md` に戻れるようにする

task / changed files / task type による絞り込みは、applicability の形式を決めてから
後続 phase で追加する。

第一段階の CLI:

```bash
harnest lessons new no-temp-artifact-commit \
  --checklist-item "作業用ファイルや一時ファイルを実装差分に含めない" \
  --severity high \
  --confidence high \
  --category git-hygiene

harnest lessons generate-checklist
harnest lessons generate-checklist --check
harnest lessons prepare-checklist-result
harnest lessons verify-checklist-result
harnest lessons install-guidance --provider claude,codex
```

`lessons new` は `.harnest/lessons/<id>.md` の skeleton を作る。
`generate-checklist` は active lessons から `.harnest/checklist.md` を生成する。
`--check` は CI / review 用で、既存 checklist が生成結果と一致しない場合に失敗する。
`prepare-checklist-result` は作業用 result を `.harnest/work/checklist-result.md` にコピーする。
`verify-checklist-result` はすべての item が `[x]` / `[-]` / `[!]` のいずれかで解決済みか、
また `[!]` に理由があるかを確認する。
`install-guidance` は既存ファイルを上書きせず、managed block または追加 hook entry として
`CLAUDE.md` / `AGENTS.md` / `.claude/settings.json` / `.codex/hooks.json` を更新する。

## 実装 agent の見え方

基本は `checklist.md` だけを見せる。

理由:

- lesson 本体を全件読ませるとコンテキスト量が増える
- 実装前に読む量が多いと、実装品質よりチェック消化が目的化する
- 短い checklist の方が pass1 / pass2 の比較条件を揃えやすい

ただし、次の場合は lesson 詳細を参照してよい。

- checklist item の意味が実装文脈だけでは判断できない
- 例外に該当するか判断が必要
- judge が違反根拠を確認する
- step40 が新規 lesson を作る前に既存 lesson と統合できるか確認する

`CLAUDE.md` / `AGENTS.md` へ投影する場合は、対象 repo の
`@.harnest/checklist.md` を参照させる。

最小指示:

```markdown
Before implementation, read @.harnest/checklist.md.
Run or perform the equivalent of:

harnest lessons prepare-checklist-result --force

Before creating a PR, update .harnest/work/checklist-result.md:

- [x] means compliant
- [-] means not applicable
- [!] means valid exception and requires an indented reason:

Then run:

harnest lessons verify-checklist-result
```

## 新規 lesson 作成時の統合判断

step40 は新規 lesson を作る前に、既存 lesson の ID、`category`、`Checklist Item`、
`Problem`、`Guidance`、`Merge Notes` を見て分類する。

現在の Go pipeline では、下流 step50/60/70 との互換性のため
`40/candidates.json` / `40/classification.jsonl` という artifact 名を残す。
ただし Step40 が作る本文の実体は rule candidate ではなく experiment lesson であり、
sidecar は `40/experiment/lessons/<lesson-id>.md` に置く。
その lesson から `40/experiment/checklist.md` を自動生成する。

Step30 が `30/issues-A.jsonl` を出している場合、Step40 はその explicit issue を
lesson 生成の第一入力にする。`issues-A.jsonl` が無い、または空の場合だけ、
従来通り `scores-A.jsonl` の採点理由から heuristic に fallback 抽出する。

分類:

- `new`: 既存 lesson ではカバーできない
- `update`: 既存 lesson に evidence / 例外 / scope を足せばよい
- `duplicate`: 実質同じで、追加不要

統合判断では checklist の文面だけを見ない。
checklist は短く要約されているため、似て見えるが詳細では別問題の lesson を
誤って統合する可能性がある。

## 重要度の扱い

`severity` は checklist の表示順と採用判断の補助に使う。
単独で採用 / reject を決める hard gate にはしない。

| severity | 意味 |
| --- | --- |
| `critical` | データ破壊、security、誤った採用判定など harness 自体を壊す |
| `high` | 実装品質や比較結果を大きく歪める |
| `medium` | 頻度または影響が中程度。放置すると品質低下につながる |
| `low` | 改善余地。誤検知しやすい、または効果が限定的 |

## Phase 分割

### Phase 1: lesson format + checklist generation

- lesson format を定義する
- active lessons から `checklist.md` を生成する
- implementer prompt は原則 `checklist.md` だけを読む
- 通常利用では `.harnest/work/checklist-result.md` を作業用 result とする
- harness 内部の `checklist-result.json` は既存通り agent の自己申告 artifact として残す

### Phase 2: generated guidance

- lesson から短い agent guidance を生成する
- provider ごとに `CLAUDE.md` / `AGENTS.md` / Codex rules へ投影できるようにする
- ただし、source of truth は lesson のままにする

### Future: hooks / lint / test projection

hooks は後回しにする。
機械的に検出できる lesson だけを、将来 diff validation、lint、test、hook に投影する。

hook / lint / test は lesson の代替ではなく、lesson から派生する enforcement である。
