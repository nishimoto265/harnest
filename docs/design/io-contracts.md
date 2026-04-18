# I/O契約 (Inter-Step Contracts)

並列実装 agent がお互いに壊さないための「通貨」。
すべての step は `src/contracts.ts` から型/schema をimportし、`src/io/*` の helper 経由でのみファイルを読み書きする。

- **Single source of truth**: `src/contracts.ts` (全schema)
- **Read/Write helpers**: `src/io/*.ts`
  - `atomic.ts` - writeAtomic (rename-based + tmp cleanup on failure)
  - `json.ts` - writeJsonAtomic / readJson (zod validation付き)
  - `jsonl.ts` - appendJsonl / readJsonl / collapseByKey (4KB line制限)
  - `run-layout.ts` - 全パスと RunContext の唯一の定義元
  - `safe-text.ts` - 外部テキストをjudge/extractor promptに埋める前の sanitize
- **Layout helper**: `src/io/run-layout.ts`

---

## run ディレクトリ構造

1 PR の処理 = 1 run。`run_id` で特定する。

```
<runs_base>/<runId>/                     # runId = "YYYY-MM-DD-PR<num>-<hex7>"
├── config.snapshot.yaml
├── task-package.json                    # step 10 出力
├── base.sha
├── 20-pass1/
│   ├── a1/
│   │   ├── manifest.json                # ★atomic完成の目印
│   │   ├── session.jsonl
│   │   ├── diff.patch
│   │   └── checklist-result.json
│   ├── a2/ ...
│   └── a3/ ...
├── 30/
│   ├── scores-A.jsonl
│   ├── compliance-A.jsonl
│   └── raw/
│       ├── a1-claude.txt
│       ├── a1-codex.txt
│       └── a1-arbiter.txt
├── 40/
│   ├── candidates.json
│   └── classification.jsonl
├── 50-pass2/
│   └── a{1,2,3}/manifest.json + ...     # 20-pass1 と同型
├── 60/
│   ├── scores-B.jsonl
│   ├── compliance-B.jsonl
│   ├── pairwise.jsonl
│   └── raw/ ...
└── 70/
    └── decision.json
```

**外部(run ディレクトリの外)**:
- `<runs_base>/processed.jsonl` — 全 run 共通の state log
- `<runs_base>/rules-registry.jsonl` — 全 PR 横断の rule ライフサイクル正本

**worktree は runs/ の外**:
`<worktree_base>/<runId>-pass{1,2}-a{1..N}/` に置く。コード上は `pass1WorktreePath(ctx, agent)` / `pass2WorktreePath(ctx, agent)` で取得。実際のメタデータ (`{agent, pass, path, branch, base_sha, head_sha}`) は **`task-package.json.worktrees[]` が正本**。step 70 の cleanup はそれを読む。

---

## 原則

### 1. Atomic write
「step完了マーカー」系ファイル(manifest.json, task-package.json, decision.json 等)は `writeAtomic()` か `writeJsonAtomic()` を使う。`<path>.tmp-<pid>-<ms>-<rand>` に書いて `fs.rename()` で完成。rename or write 失敗時は tmp を自動 cleanup。**生 `fs.writeFile()` を completion marker に使わない**。

### 2. Completion marker
各 step の「完了」は**該当ファイルの存在**で判定する:

| step | 完了マーカー |
|---|---|
| 10 | `task-package.json` |
| 20 agent | `20-pass1/<agent>/manifest.json`(schema validation通過を要求) |
| 30 | `30/scores-A.jsonl` AND `compliance-A.jsonl` |
| 40 | `40/candidates.json` |
| 50 agent | `50-pass2/<agent>/manifest.json` |
| 60 | `60/scores-B.jsonl` AND `pairwise.jsonl` |
| 70 | `70/decision.json` |

manifest が無い or schema 不一致のagent は **採点対象外**。判定は `run-layout.ts#loadFinalizedManifest()` を使うこと。

### 3. Append-only jsonl
`scores-*` / `compliance-*` / `pairwise` / `processed` / `rules-registry` は全て append-only。各エントリは 4KB 未満を保証(PIPE_BUF 原子性)。longer free-text は **schema で capping済み** + overflow時は sidecar (sha256参照) を使う。

「最新勝ち」のreduceは `collapseByKey()` を使う(自前reduceを書かない)。

### 4. Strict JSON
- 全 schema は `.strict()` で unknown key をreject(schema drift 検出)
- agent-produced free-text は strict field 配下に保存
- 下流 prompt に入れる時は `safe-text.ts#sanitizeForPromptEmbedding` を通す(**必須**)

### 5. `run_id` は全 step 共通
step 10 が `newRunId(pr)` で生成 → task-package.json に含める → 以降の step は `runContextFromTaskPackage(pkg, {runsBase, worktreeBase})` で再構築する。

### 6. state.append の責務
`processed.jsonl` への append は **orchestrator (run-cycle2.sh / ts) のみ** が行う。各 step は自身の output ファイル(manifest, decision 等)を置くだけで、state は触らない。**二重append防止**のための規約。

例外: step 10 だけは `started` を自分で append してよい(task-package が書かれる前に起動記録が必要なため)。以降の state.append は orchestrator に集約。

### 7. Path segment 安全性
`runId` と `agent` は `assertSafeSegment()` / `assertAgentId()` で `../` や `/` 混入を早期 reject。path helper は内部で全て validate 済みなので agent は心配しなくてよい。

---

## Prompt embedding safety

外部入力(PR body, linked issues, agent 生成 reasons / rationale 等)を judge/extractor prompt に埋める場合は:

```typescript
import { sanitizeForPromptEmbedding } from '@auto-improve/io/safe-text';

const safe = sanitizeForPromptEmbedding(pkg.reconstructed_task_prompt, {
  label: 'task_prompt',
});
const prompt = `
You are a code judge. Consider this task description:
${safe}
...
`;
```

何をしているか:
- `<untrusted-text source="label">…</untrusted-text>` で fence
- `SYSTEM:` / `ASSISTANT:` / ` ```` ` を zero-width space で分解
- 上限長でtruncate

攻撃者モデルは「自分 or 偶然」だが、再帰的に LLM-as-Judge を回すので自己増幅による regression を防ぐ価値がある。

---

## 各 step I/O (要約)

### step 10: restore-base

**入力**: PR番号, config(gh認証, best_branch, harness_files)

**処理**:
- `gh pr view <num>` で base SHA / 本文 / linked issues 取得
- **worktree 6個**(pass1/pass2 × 3agent)を一度に切る(手戻り削減)
- `reconstructed_task_prompt` を生成
- `processed.jsonl` に `started` event append(例外的に step 10 が自分でappendしてよい)

**出力**:
- `<run>/task-package.json` (`TaskPackageSchema` 準拠、`worktrees` に 6 WorktreeAllocation 含む)
- `<run>/base.sha`

---

### step 20: implement (pass1) / step 50: implement (pass2)

**入力**:
- `task-package.json`(`worktrees` から自分の pass の worktree dir を取得)
- best設定(step 10 が適用済み) or best + 候補ルール(pass2)

**処理**:
- claude CLI を 3体並列起動
- 各 agent は実装 + checklist記入 + commit
- 完了時に **atomic write** で `manifest.json` を出す(中身 = `ManifestSchema` 準拠)
- timeout/error も manifest に記録(`exit_status`: `success` | `error` | `timeout` の discriminated union)

**出力**:
- `<run>/{20-pass1|50-pass2}/<agent>/manifest.json`
- `<run>/{20-pass1|50-pass2}/<agent>/session.jsonl`
- `<run>/{20-pass1|50-pass2}/<agent>/diff.patch`
- `<run>/{20-pass1|50-pass2}/<agent>/checklist-result.json`(`ChecklistResultSchema`)

---

### step 30: score pass1 / step 60: score pass2 + pairwise

**入力**:
- `task-package.json`
- pass 成果物(`loadFinalizedManifest()` で存在+整合確認済みのagentだけ)
- `rubrics/default.md` (rubric_version を記録)
- prompts 各種 (prompt_version を記録)
- 実PR diff (Fidelity採点用、sanitizeして embedding)

**処理**:
- Panel review: primary判定 + secondary判定 → 必要なら arbiter
- 5次元スコア + 理由 + compliance audit(Discipline内包)
- `verdict_path` に 解決モード(`single` / `agreement` / `arbitrated` / `arbiter_overruled`)を記録

**出力**:
- `<run>/30/scores-A.jsonl` または `60/scores-B.jsonl`
- `<run>/30/compliance-A.jsonl` または `60/compliance-B.jsonl`
- step 60 のみ: `<run>/60/pairwise.jsonl`

---

### step 40: extract-rules

**入力**:
- `30/scores-A.jsonl`(採点理由)
- `30/compliance-A.jsonl`(違反ルール)
- pass1 `diff.patch` と 実PR diff の差分(sanitize 必須)
- 既存 `rules-registry.jsonl`

**処理**:
- LLM(Claude)で問題点 → 候補ルール生成
- 既存ルール群と類似度判定 → new / update / duplicate 分類

**出力**:
- `<run>/40/candidates.json` (`CandidatesSchema`)
- `<run>/40/classification.jsonl` (1候補1行、`ClassificationEntrySchema`)

---

### step 70: decide + apply

**入力**:
- `30/scores-A.jsonl`, `60/scores-B.jsonl`, `60/pairwise.jsonl`
- `40/candidates.json`
- 現行 `rules-registry.jsonl` + best_branch HEAD SHA
- config(閾値)

**処理**:
- `action = 'adopt' | 'reject' | 'noop'` 判定
- adopt の場合、transactional promotion:
  1. 現在の best SHA を `best_sha_before` に記録
  2. `processed.jsonl` に `promoting` append(orchestrator)
  3. best_branch へ commit
  4. `rules-registry.jsonl` に append (`added` or `updated` event、version_seq + prev_hash)
  5. `decision.json` を atomic write
  6. `processed.jsonl` に `promoted` append
- 失敗時: best_branch を `best_sha_before` に revert + `processed.jsonl` に `rollback`

**出力**:
- `<run>/70/decision.json` (`DecisionSchema`)
- `rules-registry.jsonl` 追記(採用時)

---

## エラー時の挙動(共通)

| 状況 | 記録 | 次 step |
|---|---|---|
| step 20/50 agent timeout | `manifest.json` に `exit_status: "timeout"` で書かれる(なければ orchestrator が `timeout` event append) | その agent は採点対象外 |
| step 20/50 全agent 失敗 | 全 manifest 不在 → `failed` event(orchestrator) | step 30 以降 skip |
| step 30 ジャッジ失敗 | リトライ2回、それでもダメなら `failed` event | step 40 skip |
| step 40 候補0件 | `candidates.json.candidates: []` | step 50-60 skip → best維持 → `completed` event |
| step 70 transactional失敗 | `decision.json.action: "rollback"` + best_branch revert + `rollback` event | 次 run から再試行可 |

---

## 並列実装時の約束(agent向け)

1. **Schemas は `src/contracts.ts` だけ編集可**。他 step の schema を勝手に変えない。
2. **io helper を使う**。生 `fs.writeFile` を直接呼ばない(atomic保証が消える)。
3. **run_layout で与えられたパスを使う**。自前で path 組み立てない。
4. **schema違反は即 throw**。下流の壊れたファイルを作らない。
5. **`loadFinalizedManifest` を使う**(生 `existsSync` で判定しない)。
6. **外部テキストは `sanitizeForPromptEmbedding`**(生 string を prompt に入れない)。
7. **`collapseByKey` を使う**(append-onlyの後勝ち reduce を自前で書かない)。
8. **`processed.jsonl` に append しない**(orchestratorの責務、例外は step 10の `started` のみ)。
9. **既存テストを壊さない**(現状 95件)。

---

## schema 変更時のプロトコル

- `contracts.ts` を変える PR は独立させる(並列実装中の step コードと混ぜない)。
- `.strict()` 外し / field 削除 / discriminator 変更は breaking。既存 runs/ を読む `readJsonl` が throw する可能性あるため、migration コメント必須。
- 互換を保った拡張(optional field 追加、enum に要素追加)は安全。

schema 変更後:
1. 影響を受ける step のテストを追加/更新
2. `pnpm exec tsc --noEmit` でtype check
3. `pnpm exec vitest run` で全テストpass
