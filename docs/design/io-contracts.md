# I/O契約 (Inter-Step Contracts)

並列実装 agent がお互いに壊さないための「通貨」。
すべての step は `src/contracts.ts` から型/schema をimportし、`src/io/*` の helper 経由でのみファイルを読み書きする。

- **Single source of truth**: `src/contracts.ts`
- **Read/Write helpers**: `src/io/*.ts`(atomic write / jsonl append / json read with zod validation)
- **Run layout**: `src/io/run-layout.ts`(ディレクトリ構造の宣言)

---

## run ディレクトリ構造

1 PR の処理 = 1 run。`run_id` で特定する。

```
<runs_base>/<YYYY-MM-DD>-PR<NUM>-<short-run-id>/
├── config.snapshot.yaml            # 実行時 config のコピー
├── task-package.json               # step 10 出力
├── base.sha                        # step 10 出力(1行、改行なし)
├── 20-pass1/
│   ├── a1/
│   │   ├── manifest.json           # ★atomic完成の目印。無ければ未完成扱い
│   │   ├── session.jsonl           # claude CLI 生ログ
│   │   ├── diff.patch              # base.sha からの git diff
│   │   ├── checklist-result.json   # strict JSON版
│   │   └── (worktree: 別途 worktree.base 配下)
│   ├── a2/ ...
│   └── a3/ ...
├── 30/
│   ├── scores-A.jsonl              # 1行 = 1(pr, agent) の採点
│   ├── compliance-A.jsonl          # 1行 = 1(pr, agent, rule) の compliance verdict
│   └── raw/                        # ジャッジ生レスポンス
│       ├── a1-claude.txt
│       ├── a1-codex.txt
│       └── a1-arbiter.txt
├── 40/
│   ├── candidates.json             # strict JSON(md版は任意)
│   └── classification.jsonl        # new/update/duplicate 分類
├── 50-pass2/
│   └── a{1,2,3}/manifest.json + ...  # 20-pass1 と同型
├── 60/
│   ├── scores-B.jsonl              # 1行 = 1(pr, agent) の採点
│   ├── pairwise.jsonl              # 1行 = 1(pr, agent) の A vs B 勝敗
│   └── raw/ ...
└── 70/
    └── decision.json               # 採用判定結果
```

**外部(run ディレクトリの外)**:
- `<runs_base>/processed.jsonl` — 全 run 共通の state(既存)
- `<runs_base>/rules-registry.jsonl` — 全 PR 横断の rule ライフサイクル正本

---

## 原則

### 1. Atomic write
すべての「step完了マーカー」系ファイル(manifest.json, decision.json, task-package.json 等)は `writeAtomic()` で書く。`<path>.tmp` に書いて `fs.rename()` で完成。rename は同一FS内で atomic なので、途中で落ちても破損ファイルが後続stepに読まれない。

### 2. Completion marker
各 step の「完了」は**マニフェストファイルの存在**で判定する(既存ファイルの中身ではない):
- step 10 完了 = `task-package.json` 存在
- step 20 agent完了 = `20-pass1/<agent>/manifest.json` 存在
- step 30 完了 = `30/scores-A.jsonl` 存在 + `compliance-A.jsonl` 存在
- step 40 完了 = `40/candidates.json` 存在
- step 50 agent完了 = `50-pass2/<agent>/manifest.json` 存在
- step 60 完了 = `60/scores-B.jsonl` + `60/pairwise.jsonl` 存在
- step 70 完了 = `70/decision.json` 存在

manifest がない agent は **採点対象外**(Codex H5)。

### 3. Append-only jsonl
scores / compliance / pairwise / processed / rules-registry は全て **append-only jsonl**。1行 = 1 entry。retryで同じpr+agentが複数出る場合は**後勝ち**(下流は最新1件を使う)。

### 4. Strict JSON everywhere
agent生成のfree-textは instruction injection 耐性のため、**別フィールドに隔離**して、絶対にprompt embedding時のシステム命令と混ざらないよう sanitize する(Codex H6)。

### 5. `run_id` は全 step 共通
step 10 で `run_id` を決め、以降の step では**引き継ぐだけ**。ログ・manifest・scores・decision が同じ `run_id` で紐付く。

### 6. 冪等性
各 step は「前段の成果物 + 自分のoutput」を見て、既に完了していればskip可能に書く(re-run時コスト最小化)。

---

## 各 step I/O (要約)

### step 10: restore-base

**入力**:
- PR番号
- config(gh認証情報、best_branch、harness_files)

**処理**:
- `gh pr view <num>` で base SHA / 本文 / linked issues 取得
- 対応worktreeを3体分(pass1用)切る + best設定を展開
- 対応worktreeを3体分(pass2用)も同時に切る(手戻り削減)
- `task-package.json` を生成(reconstructed task prompt 含む)

**出力**:
- `<run>/task-package.json`(`TaskPackageSchema`準拠)
- `<run>/base.sha`(commit SHA)
- `<worktree.base>/<run_id>-pass1-a{1,2,3}/`(3 worktree)
- `<worktree.base>/<run_id>-pass2-a{1,2,3}/`(3 worktree、初期は pass1と同じ state)

---

### step 20: implement (pass1) / step 50: implement (pass2)

**入力**:
- `task-package.json`
- worktree dir(step 10 で用意済み)
- best設定(適用済みの前提) or 候補ルール追加版(pass2)

**処理**:
- claude CLI を worktreeで並列起動(3体)
- 各agent は実装 + checklist記入 + commit
- timeout / error を個別に記録
- 完了時に**atomic**で `manifest.json` を書く

**出力**:
- `<run>/20-pass1/<agent>/manifest.json`(`ManifestSchema`準拠)
- `<run>/20-pass1/<agent>/session.jsonl`
- `<run>/20-pass1/<agent>/diff.patch`
- `<run>/20-pass1/<agent>/checklist-result.json`(`ChecklistResultSchema`準拠)
- step 50 は `50-pass2/` 配下に同型

---

### step 30: score pass1 / step 60: score pass2 + pairwise

**入力**:
- pass1成果物(全agent manifest 必須)
- `task-package.json`
- ルーブリック(`rubrics/default.md`)
- 実PRのdiff(Fidelity採点用)

**処理**:
- manifestがあるagentのみ採点対象
- Panel review: Claude判定 + Codex判定 → Codex arbiter(中立化)
- 5次元スコア + 理由 + compliance audit(Discipline内包)

**出力**:
- `<run>/30/scores-A.jsonl`(1行=1agent、`ScoreEntrySchema`準拠)
- `<run>/30/compliance-A.jsonl`(1行=1ルール、`ComplianceEntrySchema`準拠)
- step 60 は `60/` 配下 + `pairwise.jsonl`(`PairwiseEntrySchema`)

---

### step 40: extract-rules

**入力**:
- `30/scores-A.jsonl`(採点理由)
- `30/compliance-A.jsonl`(違反ルール)
- pass1 `diff.patch` と 実PR diff の差分
- 既存 `rules-registry.jsonl`(重複/updateチェック用)

**処理**:
- LLM(Claude)で問題点 → 候補ルール生成
- 既存ルールとの類似度判定 → new / update / duplicate 分類

**出力**:
- `<run>/40/candidates.json`(`CandidatesSchema`準拠、strict JSON)
- `<run>/40/classification.jsonl`(候補ごとの分類結果)

---

### step 70: decide + apply

**入力**:
- `30/scores-A.jsonl`, `60/scores-B.jsonl`, `60/pairwise.jsonl`
- `40/candidates.json`
- 現行 `rules-registry.jsonl`
- config(閾値)

**処理**:
- 採用判定(パス2スコア > パス1スコア)
- transactional promotion:
  1. `processed.jsonl` に `promoting` append
  2. best_branch へ commit(候補ルール追加)
  3. `rules-registry.jsonl` に new entries append
  4. `processed.jsonl` に `promoted` append
- 失敗時: rollback(best_branch revert + `rollback` event)

**出力**:
- `<run>/70/decision.json`(`DecisionSchema`準拠)
- `rules-registry.jsonl` 更新(採用時)
- `processed.jsonl` 更新(event append)
- best_branch の commit(採用時)

---

## エラー時の挙動(共通)

| 状況 | 記録 | 次 step |
|---|---|---|
| step 20/50 agent timeout | `manifest.json` 無し → `timeout` event in processed | その agent は採点対象外 |
| step 20/50 全agent 失敗 | 全 manifest 無し → `failed` event | step 30 は空 scores を出力しない → step 70 も走らない |
| step 30 ジャッジ失敗 | リトライ2回、それでもダメなら `failed` event | step 40 skip |
| step 40 候補0件 | `candidates.json` 空 | step 50-60 skip → best維持 → `completed` event |
| step 70 transactional失敗 | `rollback` event + best_branch revert | 次 run から再試行 |

---

## 並列実装時の約束(agent向け)

1. **Schemas は `src/contracts.ts` だけ編集可**。他 step のschemaを勝手に変えない
2. **io helper を使う**。生 `fs.writeFile` を直接呼ばない(atomic保証が効かないため)
3. **run_layout で与えられたパスを使う**。自前で path 組み立てない
4. **schema違反は即 throw**。下流の壊れたファイルを作らない
5. **既存 23件のテストを壊さない**(state.test.ts / paths.test.ts / detect-merged.test.ts)
