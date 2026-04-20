# I/O契約 (Inter-Step Contracts)

並列実装 agent がお互いに壊さないための「通貨」。
すべての step は `internal/contracts/` から型/schema をimportし、`internal/io/` の helper 経由でのみファイルを読み書きする。

- **Single source of truth**: `internal/contracts/*.go` (全schema、validator tag 付き)
- **Step I/O contract**: `internal/contracts/stepio/*.go` (step 間 request/response)
- **Read/Write helpers**: `internal/io/*.go`
  - `atomic.go` - WriteAtomic (rename-based + tmp cleanup on failure, darwin/linux のみ)
  - `json.go` - WriteJSONAtomic / ReadJSON (DisallowUnknownFields + EOF check + validator)
  - `jsonl.go` - AppendJSONL / ReadJSONL / CollapseByKey (4KB line制限 + overflow sidecar)
  - `run_layout.go` - 全パスと RunContext の唯一の定義元
  - `safe_text.go` - 外部テキストを judge/extractor prompt に埋める前の sanitize

---

## run ディレクトリ構造

1 PR の処理 = 1 run。`run_id` で特定する。

```
<runs_base>/<runId>/                     # runId = "YYYY-MM-DD-PR<num>-<hex7>"
├── config.snapshot.yaml
├── task-package.json                    # step 10 出力
├── base.sha
├── processed-details/                   # interrupted / needs_manual_recovery 等の detail overflow sidecar
│   └── <sha256>.txt
├── 20-pass1/
│   ├── a1/
│   │   ├── manifest.json                # ★atomic完成の目印
│   │   ├── session.jsonl
│   │   ├── diff.patch
│   │   ├── checklist-result.json
│   │   ├── .resume-state.json           # rev11: agent worktree lease
│   │   ├── .heartbeat                   # rev11: live agent heartbeat (mtime-based)
│   │   └── rescued/                     # rev22: crash-safe rescue 退避
│   │       └── <rescue_id>/
│   │           ├── commits.bundle       # pending commits (base..HEAD)
│   │           ├── tracked.patch
│   │           ├── staged.patch
│   │           ├── untracked/           # cp -a で保存された untracked files
│   │           ├── untracked-symlinks.txt  # symlink は record only
│   │           ├── ignored.txt          # ignored ファイル list (記録のみ)
│   │           └── state.json           # 各 artifact の sha256
│   ├── a2/ ...
│   └── a3/ ...
├── 30/
│   ├── scores-A.jsonl                   # final layer (panel 解決後 verdict)
│   ├── scores-A-raw.jsonl               # rev11: raw layer (primary/secondary/arbiter 別 verdict)
│   ├── compliance-A.jsonl               # final layer
│   ├── compliance-A-raw.jsonl           # rev11: raw layer
│   ├── done.marker                      # rev11: cardinality + content_hashes
│   ├── reasons/                         # reasons overflow sidecar
│   │   └── <sha256>.txt
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
│   ├── scores-B.jsonl                   # final layer
│   ├── scores-B-raw.jsonl               # rev11: raw layer
│   ├── compliance-B.jsonl               # final layer
│   ├── compliance-B-raw.jsonl           # rev11: raw layer
│   ├── pairwise.jsonl
│   ├── done.marker                      # rev11: cardinality + content_hashes
│   ├── reasons/                         # reasons overflow sidecar (pass2 用)
│   │   └── <sha256>.txt
│   └── raw/                             # rev25 明示: step60 も raw retention mandatory
│       ├── a1-claude.txt
│       ├── a1-codex.txt
│       └── a1-arbiter.txt
└── 70/
    ├── decision.json
    └── intention.json                   # rev11: staged transaction (planning→finalized、finalize で削除)
```

**外部(run ディレクトリの外)**:
- `<runs_base>/processed.jsonl` — 全 run 共通の state log
- `<runs_base>/rules-registry.jsonl` — 全 PR 横断の rule ライフサイクル正本
- `<runs_base>/rules-idempotency-index.jsonl` — rev11: 閾値超過時 自動生成、rebuildable cache
- `<runs_base>/rules/<rule_id>.md` — rev11: rule 本体 sidecar(registry は path + sha256 のみ)
- `<runs_base>/promotion.lock` — rev11: step70/recover/sunset_tick 共有 flock
- `<runs_base>/needs-recovery/<run_id>.json` or `.aborted.json` — rev11: durable sentinel
- `<runs_base>/sunset-running.marker` / `last-sunset-at` — rev11: sunset tick state(rev18: lock は `promotion.lock` を共有、`.sunset-lock` 廃止)

**worktree は runs/ の外**:
`<worktree_base>/<runId>-pass{1,2}-a{1..N}/` に置く。コード上は `pass1WorktreePath(ctx, agent)` / `pass2WorktreePath(ctx, agent)` で取得。実際のメタデータ (`{agent, pass, path, branch, base_sha, head_sha}`) は **`task-package.json.worktrees[]` が正本**。step 70 の cleanup はそれを読む。

---

## 原則

### 1. Atomic write
「step 完了マーカー」系ファイル(manifest.json, task-package.json, decision.json 等)は `internal/io/atomic.go#WriteAtomic()` か `internal/io/json.go#WriteJSONAtomic()` を使う。`<path>.tmp-<pid>-<ms>-<rand>` に書いて `os.Rename()` で完成。rename or write 失敗時は tmp を自動 cleanup。**生 `os.WriteFile()` を completion marker に使わない**(darwin/linux の rename atomicity を前提、Windows は対象外)。

### 2. Completion marker
各 step の「完了」は**該当ファイルの存在**で判定する:

| step | 完了マーカー |
|---|---|
| 10 | `task-package.json` |
| 20 agent | `20-pass1/<agent>/manifest.json`(schema validation通過を要求) |
| 30 | `30/done.marker` (cardinality verified、rev9 同期) |
| 40 | `40/candidates.json` |
| 50 agent | `50-pass2/<agent>/manifest.json` |
| 60 | `60/done.marker` (cardinality verified、rev9 同期) |
| 70 | `70/decision.json` |

manifest が無い or schema 不一致の agent は **採点対象外**。判定は `internal/io/run_layout.go#LoadFinalizedManifest()`(成功/失敗/timeout 全種)または採点時限定の `LoadScorableManifest()`(成功のみ、他は `ErrNotScorable`)を使う。step30/60 は必ず後者を使う(契約)。

### 3. Append-only jsonl
`scores-*` / `compliance-*` / `pairwise` / `processed` / `rules-registry` は全て append-only。各エントリは 4KB 未満を保証。**Append safety** (rev24、rev26 single-writer 明確化、Codex rev23/rev25 対応): PIPE_BUF atomicity は pipe にのみ適用され、regular file append は(NFS 等で)保証されない。本計画では以下の single-writer 契約で serialize:
- `rules-registry.jsonl` / `rules-idempotency-index.jsonl` への書込は **`promotion.lock` (step70/recover/sunset 共有) 下でのみ single-writer**
`processed.jsonl` / registry への書込排他は **lock 粒度を分離**(rev33、Claude rev32 Critical #2 対応: pipeline 全長独占を回避):

- **`<runs_base>/state.lock`(短命 flock、append 系書込の排他)**:
  - `processed.jsonl` への 1 event append ごとに取得 → write → fsync → release、保持時間 数 ms
  - orchestrator 本体(step10-60)の state append、step70 の state append、recover の state append、sunset_tick の telemetry(書く場合)全て state.lock を短時間で acquire
  - 短命なので同時 acquire 要求が発生しても待機 ms 級
- **`<runs_base>/promotion.lock`(長命 flock、best_branch + registry mutation の排他)**:
  - step70 全 flow (planning→finalized) で保持(branch push + registry append が含まれる数秒〜数十秒)
  - `recover --rollback/--adopt-anyway/--mark-manual-abort/--finalize-cleanup` で保持
  - `sunset_tick` (auto: 30s timeout、manual --force: 無制限 wait) で保持
  - `recover --inspect` は **promotion.lock を短時間 acquire → snapshot 読み取り → release**(rev34 確定、Codex rev33 #2 対応: rev28 では read-lock 不要と書いたが、他所(L604)で取得後と記載あり、全箇所統一で acquire → read → release)。lock 保持時間は ms 級、他 writer を長時間 block しない
  - 長命だが step20/50 の claude 呼出(数十分〜数時間)とは独立、step70 はすぐ終わるので lock 保持時間が短い
- **結果**: pipeline 全長は独立実行(claude 呼出時間 は lock 外)、sunset tick は step70 実行中の数秒〜数十秒のみ待てば済む。single-writer は state.lock と promotion.lock の 2 layer で達成
- lock ordering 規約(deadlock 防止): state.lock ⊂ promotion.lock(promotion.lock 保持中に state.lock 取得 OK、逆は禁止)
- `scores-*-raw.jsonl` / `scores-*.jsonl` / `compliance-*.jsonl` / `pairwise.jsonl` は PR-local、step 内で single goroutine (step20/50/30/60 の main goroutine) のみが append。goroutine 並列は agent ごとの独立 file (manifest, session 等) に限定、共有 jsonl には並列 write しない
- ローカル filesystem (darwin HFS+/APFS, linux ext4/xfs) 前提で NFS は対応外

longer free-text は **schema で capping 済み** + overflow 時は sidecar (sha256 参照) を使う。

**rules-registry idempotency retention**:
- step70 recovery は末尾 N=2000 件を tail scan で idempotency_key を検索
- 個人運用前提(1 PR/日)なら 2000 件 = 約 5.5 年分、安全域として十分
- **step70 append 時に registry size を check**(24h gate と独立、rev19 強化、Codex rev18 R3 対応:sunset 巨大化に備え 1500 で auto index 起動):
  - 1500 件超 → alert log + **`<runs_base>/rules-idempotency-index.jsonl` 自動生成有効化(以降の step70/sunset append で同 transaction に index 行を append)**。`processed.jsonl` に `{ kind: 'registry_size_high', step: 70, count, ... }` event を append(step70 emit、pr/run_id は該当 run 文脈があれば含める)
  - 1800 件超 → 上記 + index lookup を mandatory(tail scan は fallback のみ)
  - 2000 件超 → hard alert(`processed.jsonl` に `{ kind: 'registry_size_critical', step: 70, count, ... }` event を append + stderr 強制、operator は retention/compaction 検討)
- `<runs_base>/rules-idempotency-index.jsonl`(閾値超過時自動生成): **schema** (rev21 明示、Codex rev20 H1 対応):
  - `{idempotency_key, registry_offset, registry_sha256, kind, at}`
  - `idempotency_key` は以下を統合した汎用識別子(rev28 明記、Codex rev26 遅延指摘対応):
    - promotion entries: `added.idempotency_key` / `updated.idempotency_key`
    - rollback entries: `rolled_back.target_op_id`(対象 promotion の idempotency_key 同値)
    - sunset entries: `op_id` (= `sha256(sunset_run_id || rule_id || transition)`)
    - いずれも sha256 hex、同一名前空間で lookup 可能
  - `registry_offset` は registry 末尾 entry の byte offset
  - `registry_sha256` は対応する registry entry row の sha256(整合 verify 用)
  - `kind` は registry entry の kind(added/updated/rolled_back/status_changed/archived/restored)
  - `at` は registry append と同じ timestamp
  - 起動時に in-memory hash map(`map[idempotency_key][]IndexEntry`)に load、O(1) lookup
- **Index は rebuildable cache** (rev7, Codex rev6 R1 #4 対応、rev20 強化):
  - 起動時チェック:
    - index 不在 + registry ≥ 1500 件 → registry 全体から in-memory 再構築 + disk にも書出
    - index 存在 → **full verify**(rev22、Codex rev21 H2 対応: tail 検査だけでは interior corruption 未検出):
      - index 全 entry を scan、各 entry の `registry_offset` / `registry_sha256` を registry 実 entry の sha256 と照合
      - いずれか不一致なら index を完全破棄 → registry から再構築
      - count 整合(index entries == registry promotion+rollback+sunset entries 数)確認
      - interior corruption(中間 entry が欠落 or 改変)を検出し silent false-negative を防止
  - registry append 時は `{registry append → fsync → index append → fsync}` の順。index 書込み失敗時は次回起動時 rebuild で吸収(registry が単一 source of truth)
  - 不整合 or 構築失敗時は **fallback として tail scan を使う**(安全側、idempotency lookup は bounded scan に戻る)

「最新勝ち」の reduce は `internal/io/jsonl.go#CollapseByKey()` を使う(自前 reduce を書かない)。

### 4. Strict JSON (Go 実装ガイド)

全 JSON read は以下パターン:
- `json.NewDecoder(r).DisallowUnknownFields()` → `Decode(&out)` → **2回目 `Decode(&tmp)` で `io.EOF` 確認**(`More()` は top-level EOF 判定として不正、公式仕様上 array/object 内部のみ有効) → `validator.Struct(out)`
- tagged union の custom `UnmarshalJSON` も同パターン: envelope peek で kind 取得 → variant 別 Decoder で `DisallowUnknownFields` + EOF check + validator
- yaml は `yaml.NewDecoder(f).KnownFields(true)` 同様に 2回目 Decode で EOF 確認

失敗系テスト必須: 各 schema / union について unknown top-level key / unknown variant-level key / missing required / wrong kind / trailing token / trailing non-JSON bytes

- agent-produced free-text は strict field 配下に保存
- 下流 prompt に入れる時は `safe_text.SanitizeForPromptEmbedding` を通す(**必須**)

### 5. `run_id` は全 step 共通
step 10 が `internal/io/run_layout.go#NewRunID(pr)` で生成 → task-package.json に含める → 以降の step は `RunContextFromTaskPackage(pkg, RunsBase, WorktreeBase)` で再構築する。

### 6. state.append の責務
`processed.jsonl` への append は **orchestrator のみ** が行う。各 step は自身の output ファイル(manifest, decision 等)を置くだけで、state は触らない。**二重append防止**のための規約。

例外:
- step 10 は `started` を自分で append してよい(task-package が書かれる前に起動記録が必要なため)
- **step 70 は promotion.lock 保持中に限り `promoting / promoted / rollback / needs_manual_recovery / registry_size_high / registry_size_critical` を自己 append 可**(rev9、Codex rev8 R1/R3 対応)。順序の正確性は lock と staged intention の stage 遷移で保証。
- それ以外の step から processed.jsonl 直接 append は禁止。step 20/50 の worktree rescue 上限等の「step 自身では append できない terminal event」は、step が typed result を返却 → orchestrator が append する構造に必ずする。

### 6.0 Legacy compatibility (rev8)

本プロジェクトは **v0.1 以前の `processed.jsonl` / state との互換性を保証しない**(新規プロジェクト扱い)。
- rev8 以前の log が残っている場合、起動時に警告を出して無視(reader は schema error で reject しない、単に skip)するか、`auto-improve run --from-scratch --pr <n>` でリセット
- legacy 互換性コード(reader の special-case 等)は **実装しない**
- 開発中の試験 run はすべて `runs/` ディレクトリ削除で start over

### 6.1 Resume (crash-resistant execution)

rate limit / budget 切れ / OOM / SIGTERM 等で orchestrator が途中停止した場合、次回起動時に **同じ run_id で途中から resume** できることを全 step に要求する。

**Event vocabulary 拡張** (rev7: 全 non-terminal event に `step` required、resume cursor に使用):
- `started { pr, run_id, step: 10, at }` — 起動記録、step は暗黙 10(step10 開始前の意味)
- `step_done { pr, run_id, step, at }` — step 完了、step は完了した step 番号
- `interrupted { pr, run_id, step, reason: 'rate_limit' | 'budget' | 'context' | 'signal' | 'unknown' | 'pre_push_crash', detail?, detail_overflow_ref?, at }` — **non-terminal**、resume 対象、step は中断時に実行していた step。`pre_push_crash` は step70 planning 中 push 未到達 crash 専用(recovery で同 intention を保持して再開)

`detail` は 300 字 cap、超過時は `<run>/processed-details/<sha256>.txt` に sidecar、`detail_overflow_ref: { path, sha256 }` を付与。panic stack trace / long CLI stderr を安全に記録するため。

**Terminal events (resume対象外、detect で再 queue しない)**: `completed / failed / promoted / rollback / skipped / timeout / needs_manual_recovery`
**Non-terminal events (resume対象)**: `started / step_done / interrupted / promoting / registry_size_high / registry_size_critical / rescue_retry`

- `promoting { pr, run_id, step: 70 }` — step70 の intention planning 完了時 append。resume 時は intention.json から stage を読んで判断。
- 運用 alert 3 event(`registry_size_high` / `registry_size_critical` / `rescue_retry`): rev33 で warning sub-kind を廃止し event kind として直接昇格(Codex rev32 drift 修正)。共通 schema:
  - `{ kind, pr?, run_id?, step, count?, detail?, detail_overflow_ref?, at }`
  - rev22 変更 (Codex rev21 H3 対応): `pr` / `run_id` を **optional** に。PR 文脈で emit する場合は両 field を含める
  - resume 対象は `pr` field が present な entry のみ(PR-scoped resume ループはそれだけ見る)。`pr` 不在の entry は global telemetry 扱い、resume queue には乗らない
  - kind 集合は上記 3 種に closed(rev33 確定)。`kind: "warning"` という outer wrapper は存在しない
  - **kind × emitter 対応表**(rev33、Claude rev32 drift 修正):

    | kind | emitter | 取得 lock | pr/run_id |
    |---|---|---|---|
    | `registry_size_high` | step70 または sunset_tick(registry append 時、size check は両方で実施) | promotion.lock(append 自体) + state.lock(processed 書込) | step70: 該当 run の pr/run_id / sunset_tick: 両 null (global telemetry) |
    | `registry_size_critical` | step70 または sunset_tick | promotion.lock + state.lock | step70: 該当 run の pr/run_id / sunset_tick: 両 null |
    | `rescue_retry` | orchestrator (step20/50 rescue 結果受領時) | state.lock(短命) | 該当 run の pr/run_id |

**Resume precedence** (rev8, Codex rev7 R2 対応):
- orchestrator 起動時、`queue.go` は以下の順序で処理:
  1. `processed.jsonl` から non-terminal PR を列挙(`started / step_done / interrupted / promoting / registry_size_high / registry_size_critical / rescue_retry`)
  2. それらを **fresh detect より先に** resume(既存 run_id で cycle.Run() 呼出)
  3. 全 non-terminal run の resolve 完了後、`detect --since ...` を起動し新規 PR を queue
- これにより「resume 待ち run が新 PR に追い抜かれて step70 で古い baseline と比較する」事態を防止

※ `rollback` は per-PR terminal(その PR 再試行せず、次 PR 進行可)、`needs_manual_recovery` は per-PR terminal かつ全 run の新規 step70 を block(sentinel で durable 化)。

**Orchestrator の起動時 resume 判定**:
1. `processed.jsonl` を tail scan し各 PR の最後の event を取得
2. terminal なら skip(既処理)
3. non-terminal なら **同じ run_id で cycle を resume**。最新の `started / step_done / interrupted` の `step` 値を起点に、各 step の completion marker を確認しながら進める

**Cycle resume ロジック** (`cycle.Run()`):
- 各 step は冪等(idempotent)。既に completion marker がある step は **skip** し次の step へ
- step 内途中まで進んでいた場合は **step 自身が partial state を認識して resume**

**各 step の idempotent resume 契約**:

| step | completion marker | partial resume 判定 |
|---|---|---|
| 10 | `task-package.json` | marker あり → skip。無い場合は worktree 既存チェックし既存ならそのまま再利用 |
| 20/50 | 全 agent の `manifest.json` 揃い | agent ごとの manifest.json 単位で skip/run 判定。既存 agent は skip、不在 agent のみ再起動 |
| 30 | `<run>/30/done.marker` (rev8 導入) | marker あり → skip。無い場合は `CollapseByKey (agent, judge_role, dimension)` で reduce し未完了 role のみ再実行。全 `(agent, dimension)` × panel 解決完了で marker atomic write |
| 60 | `<run>/60/done.marker` (rev8 導入) | step30 と同様、加えて `pairwise.jsonl` の全 pair 揃い条件も cardinality check |
| 40 | `candidates.json` | marker あり → skip |
| 70 | `decision.json` または `intention.json` | step70 独自 recovery state machine(本ドキュメント step70 節) |

**scoring 成果物の正本分離** (rev11 明文化、Codex rev10 R2 対応):

scoring は 2 層:
1. **Raw layer** (`scores-A-raw.jsonl`, `compliance-A-raw.jsonl`): panel 内各 judge_role の出力を append-only で記録。reducer rule:
   - primary / secondary: `CollapseByKey(agent, judge_role, dimension)` で最新 entry を選択
   - **arbiter**: 追加で `primary_ref.sha256 / secondary_ref.sha256` が現在の最新 primary/secondary raw entries の sha256 と一致するもののみ valid(不一致は stale とみなし無視)
   - provenance-based invalidation のみ(旧 tombstone 方式は使用しない)
2. **Final layer** (`scores-A.jsonl`, `compliance-A.jsonl`): panel 解決後の最終 verdict のみ append-only。`CollapseByKey(agent, dimension)` で最新勝ち(judge_role は final には持たない)

**done.marker** の `expected_counts` と `content_hashes` は **final layer の CollapseByKey 結果**に対して計算(raw ではない)。

**arbiter rerun worked example** (upstream 再実行時の正しい流れ):
1. primary の `output_sha256` が変わった(再実行で `P1 → P2`)
2. secondary も再実行されて `S1 → S2`
3. raw: `<primary P2> append`, `<secondary S2> append`(最新 valid は `CollapseByKey(agent, judge_role, dimension)` で自動選択、追加 marker 不要)
4. 旧 arbiter A1 は `primary_ref.sha256 = sha256(P1)` を持っているので invalidated
5. 新 arbiter A2 を起動、`primary_ref.sha256 = sha256(P2)`, `secondary_ref.sha256 = sha256(S2)` で run
6. A2 を raw append。CollapseByKey reducer は `primary_ref / secondary_ref` が最新 raw entries 一致する arbiter のみ選択 → A2 が選ばれる
7. panel 解決完了 → final verdict を scores-A.jsonl に append (`CollapseByKey(agent, dimension)` で最新勝ち)
8. cardinality 揃いで done.marker 書込(final の hash で verify)

**cardinality-based 完了判定 + durability protocol** (rev9, Codex rev8 R2 対応):
- step30/60 は scores/compliance jsonl ファイルの存在だけでは完了扱いしない
- **書込み ordering 厳守**:
  1. 各 `(agent, dimension)` final verdict を scores-*.jsonl / compliance-*.jsonl / pairwise.jsonl に append
  2. 各 append 後 `fsync(fd)` + `fsync(parent dir)`
  3. 全 cardinality 揃い確認
  4. done.marker を atomic write:
   - `<run>/30/done.marker`: `{completed_agents, dimensions, expected_counts: {scores, compliance}, content_hashes: {scores_final, compliance_final}, raw_hashes: {scores_raw, compliance_raw}, resolved_at}`(step30 は pairwise 無し)
   - `<run>/60/done.marker`: `{completed_agents, dimensions, expected_counts: {scores, compliance, pairwise}, content_hashes: {scores_final, compliance_final, pairwise_final}, raw_hashes: {scores_raw, compliance_raw}, resolved_at}`(step60 は pairwise 含む)
   - **raw_hashes 記録**(rev14、Codex rev13 R1 対応): raw layer の CollapseByKey 後の sha256。resume 時に現在 raw の hash と照合し不一致なら final を破棄して再計算(crash-after-final-write & raw 再実行で stale final 残留を防止)
- **resume 時 verify**: marker 存在時も skip 前に counts / content_hashes / **raw_hashes** を current file state と照合。不一致なら:
  1. marker ファイル削除
  2. cardinality 再計算 + final layer の該当 `(agent, dimension)` を **再採点(新 final entry を append、`CollapseByKey(agent, dimension)` で latest-wins により旧 entry は無視される= append-only 維持しつつ invalidate 相当)**
  3. 全 (agent, dimension) 揃ったら marker 再生成

**Hash algorithm 具体化** (rev15、Codex rev14 R2 対応):
- `content_hashes.scores_final` = `sha256(SORT_BY_KEY(CollapseByKey(scores-A.jsonl, (agent, dimension))) | map canonical JSON serialize | concat with 0x00 separator)`
- `content_hashes.compliance_final` / `content_hashes.pairwise_final`: 同様
- `raw_hashes.scores_raw` = raw layer の reducer 適用後の valid entries(arbiter は primary_ref/secondary_ref 一致のみ)を key `(agent, judge_role, dimension)` で sort → canonical JSON → sha256
- `raw_hashes.compliance_raw`: 同様
- canonical JSON(rev21、Claude rev20 H3 対応で number/string 正規化 明示):
  - **再帰的 canonicalize**(rev20、Claude rev19 High #6 対応、rev22 int64 保持修正 Claude rev21 H2 対応):
    1. struct → `json.Marshal` → **`json.Decoder.UseNumber()` で Decode into `any`**(`UseNumber()` 無しだと stdlib が数値を全て float64 に潰す既知挙動が発生し、int64 field が hash 不安定化する)
    2. `any` ツリー (map[string]any / []any / `json.Number` / string) を再帰的に walk
    3. 各 `map[string]any` node の keys を `sort.Strings()` で alphabetical 整列し `json.Encoder` で Marshal
    4. `json.Number` は **integer として parse 検証**(`.Int64()` が成功するか確認)、失敗(= decimal / 指数表記 or int64 範囲外)なら error
    5. nested object も recursive に同処理適用(1 階層 sort では不十分)

  **contracts の numeric type 制約**(rev23、rev24 audit scope 拡張、Claude rev23 C2 + rev24 H1 対応): 全 schema 定義で uint64 / float32 / float64 は **使用禁止**、`int64` または `int` (= int64-safe range) のみ使用。`version_seq` / `registry_offset` / `count` 等は全て int64 で表現、Go 実装者は struct field に `uint64` を使わない。Phase 0-bootstrap-1 の schema 凍結時に以下の scope で grep audit し uint64/float が無いこと確認(完了条件):
  - `internal/contracts/**`
  - `internal/contracts/stepio/**`
  - `internal/io/**`(特に `jsonl.go` の reader が返す offset、`run_layout.go` 等)
  - `internal/archive/**`(sunset entry の counts / transition 等)
  - **Number 正規化** (rev21):
    - integer のみ許可(`%d` 書出、負値も正数化せずそのまま)
    - **float64 field は禁止**(±ULP 差で hash 不安定化)。必要なら string (固定 decimal) 形式で持つ
    - NaN / Inf を検出したら error を返す(ハッシュ不能)
  - **String 正規化** (rev21):
    - `json.Encoder.SetEscapeHTML(false)` 固定(`<` `>` `&` を `\u003c` 等にしない)
    - unicode escape は lowercase `\uXXXX`(RFC 8259 準拠)
  - helper `internal/io/canonicaljson.go#Marshal(v) ([]byte, error)` を用意(Phase 0-bootstrap で実装)
  - 同 helper を全 hash 生成箇所で使う(struct field 定義順依存を禁止)
  - **fixture test** (rev21 拡張):
    (a) field 順 struct 変更で hash 不変
    (b) nested object field 順変更で hash 不変
    (c) integer 値(Go int / int64、値範囲 `int64` 全域)の hash 安定 — `UseNumber()` 未使用時にこの test が落ちることが既知、実装者が気付けるよう fixture に明記
    (d) **float64 フィールド → error を返す**(Decode 時 `json.Number.Int64()` 失敗で error)
    (e) NaN / Inf → error
    (f) HTML 特殊文字を含む string の escape 形式 安定

**Raw file retention mandatory** (rev15、Codex rev14 R3 対応): `scores-*-raw.jsonl` / `compliance-*-raw.jsonl` は **削除禁止**(raw_hashes verify の source of truth)。retention policy は将来課題、Phase 0 では無期限保持。
- resume 時 marker 不在なら必ず cardinality 再計算し未完了分だけ launch

**Signal handling (orchestrator)**:
- SIGTERM / SIGINT 受信時: 現在実行中の step を graceful stop → `processed.jsonl` に `interrupted { reason: 'signal', step }` append → flock release → exit 0
- claude / codex CLI が rate limit (429, "quota") を返した場合: `interrupted { reason: 'rate_limit' }` append → exit 0。次 tick で resume
- budget depletion: `interrupted { reason: 'budget' }` append → exit 0
- 予期せぬ panic: defer で `interrupted { reason: 'unknown', step }` append を試みる(best effort)

**Resume 失敗時の escape hatch**:
- `auto-improve run --pr <n> --from-scratch` で既存 run を捨てて新規 run_id で再実行(個人運用用)。**rev34 必須処理** (Codex rev33 #1 対応): 旧 run の worktree 6 個を `git worktree remove --force` で prune + **旧 run_id の processed.jsonl 末尾 event が non-terminal なら `skipped { detail: 'superseded_by_from_scratch' }` terminal append** で旧 run を terminal 化(resume で再拾い防止)。atomic に: state.lock 下で terminal append → worktree prune → 新 run_id 発行
- 全 PR reset は `processed.jsonl` を手動編集(documented)

### 7. Path segment 安全性
`runId` と `agent` は `assertSafeSegment()` / `assertAgentId()` で `../` や `/` 混入を早期 reject。path helper は内部で全て validate 済みなので agent は心配しなくてよい。

---

## Prompt embedding safety

外部入力(PR body, linked issues, agent 生成 reasons / rationale 等)を judge/extractor prompt に埋める場合は:

```go
import autoio "github.com/nishimoto265/auto-improve/internal/io"

safe, err := autoio.SanitizeForPromptEmbedding(pkg.ReconstructedTaskPrompt, autoio.SafeTextOptions{
    Label: "task_prompt",
})
if err != nil { return err }
prompt := fmt.Sprintf(`
You are a code judge. Consider this task description:
%s
...
`, safe)
```

※ `internal/io` は flat package(package name は `autoio` で Go 標準 `io` との衝突回避、全 helper を同パッケージに配置)。実装時に sub-package 分割が必要になれば Phase 0-bootstrap で再検討。

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
- timeout/error も manifest に記録(`kind`: `success` | `error` | `timeout` の discriminated union)

**出力**:
- `<run>/{20-pass1|50-pass2}/<agent>/manifest.json`
- `<run>/{20-pass1|50-pass2}/<agent>/session.jsonl`
- `<run>/{20-pass1|50-pass2}/<agent>/diff.patch`
- `<run>/{20-pass1|50-pass2}/<agent>/checklist-result.json`(`ChecklistResultSchema`)

---

### step 30: score pass1 / step 60: score pass2 + pairwise

**入力**:
- `task-package.json`
- pass 成果物(`LoadScorableManifest()` で成功のみ、error/timeout は除外)
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

step70 の全 flow は **`<runs_base>/promotion.lock` flock exclusive 下**で実行する(global lock、best_branch + rules-registry 共有保護)。planning 開始から finalize / rollback まで lock を保持。recovery も必ず lock 取得後に state 再読込。

**SIGTERM / SIGINT / SIGKILL 等による mid-stage 中断は crash 等価扱い**(rev21、Claude rev20 H2 対応): signal handler は `interrupted` append を best-effort で試行後、`defer unlock()` により flock を確実に release。append 失敗でも unlock は必ず走る(FD close で OS が flock 解放)。中間 state(intention が未遷移 stage のまま)は step70 recovery tree がそのまま吸収する(crash 時と同じ recovery 経路)。

**flock fd の child 継承に関する正しい理解**(rev25 再修正、Claude rev25 C1 対応: rev24 の `per-fd` 記述を `per-OFD` に訂正): darwin `flock(2)` は **per-open-file-description (OFD)** で、同一 process の fd dup / child exec 経由で継承された fd も同一 lock を共有する(継承された fd で再 `flock` を呼ばなくても lock 効力は child に引き継がれる)。実運用の対策:
- `os/exec.Cmd.ExtraFiles` に flock fd を渡さなければ child には継承されない → 実装で **ExtraFiles を使わない規約**で十分
- Go 1.20+ は `os.OpenFile` の fd に内部 CLOEXEC を付与するため、通常の `os/exec.Cmd` spawn (stdin/stdout/stderr のみ)では flock fd は child に継承されない
- verification test: step70 実装時に **child (= git push subprocess simulation) 内で `flock(LOCK_EX|LOCK_NB)` を同 lock file に試行し `EWOULDBLOCK` を観測**(親が lock 保持中)。完了後に child で flock 試行成功 → release 確認。per-fd と per-OFD を区別できる形のテスト

step70 の **live 排他は flock**、**needs_manual_recovery の block は durable sentinel** で実現する(flock は process 終了で解放されるため block 手段に使わない)。

- `action = 'adopt' | 'reject' | 'noop' | 'rollback'` 判定(rollback は失敗時の書込み variant)
- adopt の場合、**staged transaction + idempotent recovery**:

**Stage 遷移(normal path)**:
1. **planning**: `intention.json` atomic write `{stage: 'planning', idempotency_key, run_id, best_sha_before, target_sha, candidates_hash, registry_head_before, started_at, registry_append_result: null}`。
   - `idempotency_key` = `sha256(run_id || target_sha || best_sha_before || candidates_hash)`。planning で1回だけ生成し以降 reuse。
   - 失敗時は intention が無い状態 → 通常 rollback は不要。
2. `processed.jsonl` に `promoting` append(**step70 自身が promotion.lock 保持中に append**。orchestrator は書かない。§state.append の責務 参照)
3. **branch_pushed**: `git push --force-with-lease=<best_branch>:<best_sha_before>` 実行。
   - 失敗(non-ff or lease mismatch)→ **rollback path**
   - 成功 → intention.json を `stage: 'branch_pushed'` に atomic overwrite
4. **registry_appended**: 以下の順で判定(CAS 前に idempotency 確認):
   - a. **idempotency_key tail scan**(末尾 N=2000 件、詳細は rules-registry.jsonl 節)。一致 entry 発見 → 既実行と判断、`registry_append_result = {offset, sha256}` を intention に記録 → **step 5 へ skip**
   - b. `current_registry_head == registry_head_before` の場合 → `rules-registry.jsonl` に append (CAS with prev_hash)。CAS 失敗は一時的と判断し1回 retry、再失敗 → **rollback path**
   - c. `current_registry_head != registry_head_before` の場合 → **rollback path**(他者が registry を進めている、idempotency_key も無い = 本当に未実行)
   - 成功 → `registry_append_result = {offset, sha256}` を記録、intention を `stage: 'registry_appended'` に overwrite
5. **decision_written**: `decision.json` atomic write (`action: 'adopt'`)。intention を `stage: 'decision_written'` に overwrite
6. **finalized**: intention.json を delete (committed marker)
7. `processed.jsonl` に `promoted` append

**Recovery state machine** (起動時、lock 取得後、state 再読込):

実際の永続化順序から導出した到達可能状態のみ。全 6 状態を列挙:

| intention | decision | stage | 状態 | recovery 動作 |
|---|---|---|---|---|
| absent | absent | - | 通常起動 | 通常 flow 開始 |
| present | absent | `planning` | push 前/後境界 crash(post-push stage 更新前も含む) | 下記 decision tree(鉄則: **HEAD 判定先行、registry check は branch=best_sha_before ブランチでのみ適用**)参照 |

**planning recovery decision tree** (rev20、Codex rev19 Claude Critical #2 対応):
```
1. remote <best_branch> HEAD を取得
2. if HEAD == target_sha:
     push 済と判定 → stage=branch_pushed 相当として Stage 4 判定へ進む
     (registry check は Stage 4 内で実行される、ここでは不要)
3. else if HEAD == best_sha_before:
     push 未到達(side-effect ゼロ)と判定 — **registry が動いていても harmless、intention snapshot を refresh して re-planning**(rev31、Codex rev29 遅延指摘対応: global stop 回避)
     registry freshness check: if current_registry_head == intention.registry_head_before:
       intention.json 保持 + interrupted { step: 70, reason: 'pre_push_crash' } append (non-terminal、同 run 再 tick で planning から resume)
     else:
       **intention.json 削除** + interrupted { step: 70, reason: 'pre_push_crash', detail: 'registry advanced during planning crash, snapshot refresh required' } append (non-terminal、同 run 再 tick で **新 planning から開始**、fresh snapshot 取得し直し)
4. else (HEAD が想定外):
     needs_manual_recovery { reason: 'remote_divergence', failed_step: '70' } + sentinel atomic write
```
| present | absent | `branch_pushed` | registry append 前 crash | 上記 Stage 4 の判定(a→b→c)を最初から再実行。a 命中なら step5 へ、b OK なら append、c なら rollback path |
| present | absent | `registry_appended` | decision.json 書込み前 crash | step 5-7 を再実行(decision.json 書込み → intention 削除 → promoted) |
| present | present | `decision_written` | intention 削除前 crash | intention 削除 + `promoted` append(decision.json は不変) |
| absent | present | - | finalize 完了済み | `processed.jsonl` に `promoted` event が既にあるか確認、無ければ append、あれば noop |

**needs_manual_recovery 状態**(別系統、durable sentinel ベース):
| intention | decision | sentinel | 状態 | recovery 動作 |
|---|---|---|---|---|
| present | absent | `<runs_base>/needs-recovery/<run_id>.json` or `.aborted.json` 存在 | remote divergence / lease failure / manual abort | **自動進行せず alert log + exit**。operator は `auto-improve recover --run <id> [--rollback \| --adopt-anyway \| --inspect \| --mark-manual-abort \| --finalize-cleanup \| --clear-sentinel]` で解決 |

**`rollback_reason` / `needs_manual_recovery.reason` 共通 enum** (rev20 統合、Claude rev19 High #3 対応):

以下の enum を decision rollback variant / intention.recovery_reason(rollback path)/ registry rolled_back entry / processed `needs_manual_recovery.reason` の **全箇所で共有**(同 validator tag の oneof 列挙を使用):

**`needs_manual_recovery.reason` enum** (rev10 固定、rev20 拡張):
- `lease_failure` — `git push --force-with-lease` の lease mismatch
- `remote_divergence` — remote HEAD が target_sha / best_sha_before のいずれでもない
- `registry_divergence` — registry head 変化 + idempotency miss
- `worktree_rescue_loop` — step20/50 の rescue 上限到達(retry_count ≥ 3)
- `manual_abort_pending_cleanup` — `--mark-manual-abort` 実行時
- `transactional_failure` — rollback path 汎用(stage 遷移途中失敗等、上記以外)

**`needs_manual_recovery.failed_step` enum**: `"10" | "20" | "30" | "40" | "50" | "60" | "70"`

**Block 機構**:
- `<runs_base>/needs-recovery/<run_id>.json` は durable な sentinel file(`{run_id, pr, reason, failed_step, created_at}` を持つ)
- **全 CLI gate 統一規約** (rev26、Claude rev26 H2 対応: **deny-by-default allowlist**): 起動時最初のアクションで `<runs_base>/needs-recovery/` ディレクトリ全体を scan(`*.json` と `*.aborted.json` 両方対象、suffix filter で `.aborted.json` も必ず拾う)。1 件でも存在する run_id があれば **non-terminal だが block** として扱い即 exit 0(side-effect なし)。**唯一の allowlist**: `recover` サブコマンドのみ(将来 `status` / `doctor` 等の read-only 診断系を追加する場合は明示的に allowlist 拡張が必要、default は全 block)
- **全 step block**(rev19、Codex rev18 R2 対応): sentinel 存在中は step10/20/30/40/50/60/70 と sunset の **全て** を pre-flight gate で reject。**唯一の例外は `auto-improve recover --run <id>`** のみ(operator 介入専用)
- 全 PR レベル block(本プロジェクトは PR 間直列): any sentinel 存在 → **全 run の全 step + sunset を block**(poisoned baseline / registry / best_branch 汚染保護)
- 上記 3 規約は一貫: CLI 層で gate、step 層で gate、PR 層で gate、三重 check で TOCTOU race も含めて block 保証
- sentinel 解除は `auto-improve recover` CLI のみが行う
- `processed.jsonl` にも `needs_manual_recovery { run_id, reason, failed_step }` event を append(`IsTerminal = true` として扱う、detect が再 queue しない)

**Rollback path (adopt 途中失敗)**:

以下の順で判定し、`best_branch` を安全に revert できる場合のみ terminal rollback を書込む:

1. remote `<best_branch>` HEAD 確認:
   - `HEAD == target_sha` (自分の push 到達済み):
     - `git push --force-with-lease=<branch>:<target_sha>` で `best_sha_before` に reset
     - lease 成功 → 下記 step 2 (terminal rollback) へ
     - lease 失敗(race で誰か更に push) → **needs_manual_recovery** 分岐(下記 step 3)
   - `HEAD == best_sha_before` → push 未到達、何もする必要なし → step 2 へ
   - それ以外 → **needs_manual_recovery** 分岐

2. **Terminal rollback (staged transaction)** — branch 状態が確定した場合のみ。**rollback 自体も crash-safe な stage 遷移**(rev19、Codex rev18 R1 critical 対応):
   - **stage `rolling_back_branch_reverted` に intention overwrite**(branch revert 完了 mark)
   - 以下、idempotent steps:
     1. **registry rollback entry append**(`intention.registry_append_result` が non-null の場合のみ): `rules-registry.jsonl` に `{kind: 'rolled_back', target_op_id, target_offset, target_sha256, by_run_id, rollback_reason, failed_step, version_seq, prev_hash, at}` を append。append 前に末尾 **N=2000** 件(promotion idempotency と同定数、`RegistryTailScanN` 定数で共通化)を tail scan し同 `target_op_id` の `rolled_back` entry が既存なら skip(idempotent)。1500+ で idempotency-index.jsonl も併用。reader は CollapseByKey 的に `rolled_back` 付き op を無効扱い。
     2. intention を `rolling_back_registry_appended` に overwrite
     3. `decision.json` atomic write (`action: 'rollback'`, `rollback_reason`, `failed_step`)
     4. intention を `rolling_back_decision_written` に overwrite
     5. `processed.jsonl` に `rollback` append (terminal)
     6. intention.json 削除(committed marker)
     7. lock release
   - **rollback recovery**: 起動時 intention.stage が `rolling_back_*` のいずれかなら、そこから resume(idempotency_key 確認 + 該当 step を re-execute、append 系は tail scan で重複 skip)

3. **needs_manual_recovery**(branch 状態が未確定):
   - intention.json を `stage: 'needs_manual_recovery'`, `recovery_reason`, `failed_step` に atomic overwrite(削除しない)
   - `decision.json` は書かない
   - **`<runs_base>/needs-recovery/<run_id>.json` durable sentinel を atomic write**(flock と独立の block 手段)
   - `processed.jsonl` に `needs_manual_recovery { run_id, reason, failed_step }` event append(**terminal event 集合に含める**、detect/requeue 側で non-retryable)
   - flock release(process exit で自動解放)
   - operator alert: slog error + stderr 必須
   - **次 tick は sentinel 存在検知で新規 step70 を block**(本 run でも他 run でも)

**出力**:
- `<run>/70/decision.json` (`DecisionSchema`, variant: `adopt | reject | noop | rollback`)
- `<run>/70/intention.json` (一時、finalize または rollback で削除。needs_manual_recovery のときのみ保持)
- `<runs_base>/promotion.lock` (flock ファイル、step70/recover/sunset_tick 共有)
- `<runs_base>/needs-recovery/<run_id>.json` (durable sentinel、needs_manual_recovery 時のみ)
- `rules-registry.jsonl` 追記(採用時、idempotency_key 付き)

### Recover safety matrix (rev8, Codex rev7 R2/R3 対応)

`auto-improve recover --run <id>` は `promotion.lock` 取得 → state 再読込 → 下記 matrix の cell に該当する動作のみ許可。それ以外は refuse + stderr に matrix の expected state を print。

| remote HEAD | registry idempotency | registry head vs before | intention stage | `--rollback` | `--adopt-anyway` |
|---|---|---|---|---|---|
| `best_sha_before` | miss | unchanged | planning / branch_pushed | ✅ no-op rollback (decision write + sentinel 削除、branch 未変更のまま) | refuse |
| `target_sha` | miss | unchanged | branch_pushed | ✅ revert via `--force-with-lease=<branch>:<target_sha>` | refuse |
| `target_sha` | miss | changed | branch_pushed | refuse(他者 registry 追加、要人間判断) | refuse |
| `target_sha` | hit | any | branch_pushed / registry_appended | refuse | ✅ finalize (decision_written → promoted) |
| `target_sha` | hit | any | decision_written | refuse | ✅ intention 削除 + promoted append |
| `other` | any | any | any | refuse | refuse |
| `best_sha_before` | hit | any | any | 本 spec 下で想定外、refuse + alert(fail-safe) | refuse |

refuse cell はすべて stderr に safe matrix を print し operator が手動判断。operator 向け追加オプション(rev9、Codex rev8 R2 対応):
- `auto-improve recover --run <id> --inspect`: promotion.lock 取得後 read-only で全 state(intention / decision / sentinel / remote HEAD / registry head)を print、終了時 lock release、副作用無し
- `auto-improve recover --run <id> --mark-manual-abort`: decision.json を `action: 'rollback', rollback_reason: 'manual_abort_pending_cleanup'` で書込 + sentinel を **`<runs_base>/needs-recovery/<run_id>.aborted.json` に rename**(block 継続) + processed `needs_manual_recovery { reason: 'manual_abort_pending_cleanup' }` append。branch/registry 修復は operator 手動で、**sentinel は削除しない**(未修復 state で次 promotion が走るのを防止)。
- `auto-improve recover --run <id> --finalize-cleanup --remote-head <sha> --registry-head <sha>`: operator が branch/registry を手動で整合させた後のみ使う。**両方 SHA が必須**(rev11、Codex rev10 R2 対応)。promotion.lock 下で remote HEAD と registry head の両方を再確認、両方一致で `.aborted.json` 削除、pipeline 復旧。片方でも不一致なら refuse
- `--clear-sentinel`: sentinel(`<run_id>.json` または `<run_id>.aborted.json`)削除(intention は手動で整合を取った後の最終手段、上記 `--finalize-cleanup` を推奨)

**safe matrix refuse cell の contract** (rev10 明文化):
- すべての refuse cell で output は `{stage, remote_head, registry_head, idempotency_hit, suggested_action}` JSON + human readable。
- 勝手に state を変更しない。operator の明示的な `--rollback / --adopt-anyway / --mark-manual-abort / --finalize-cleanup / --clear-sentinel` を待つ。

### Recover CLI 6-flag state-transition table (rev11 追加、Codex rev10 R3 対応)

| flag | 前提条件 | sentinel 変化 | intention.json 変化 | decision.json 変化 | processed.jsonl append | 結果 pipeline 状態 |
|---|---|---|---|---|---|---|
| `--inspect` | なし(sentinel 存在 or not) | 不変 | 不変 | 不変 | なし | 不変(read-only dump) |
| `--rollback` | safe matrix allowed cell(§Recover safety matrix) | `.json` 削除 | 削除 | `action: 'rollback'` 書込 | `rollback` append | block 解除、次 PR 進行可 |
| `--adopt-anyway` | safe matrix allowed cell(HEAD==target_sha, idempotency hit) | `.json` 削除 | 削除(stage=decision_written 相当から resume 実行後) | `action: 'adopt'` 書込 | `promoted` append | block 解除、次 PR 進行可 |
| `--mark-manual-abort` | 任意(inspect 後の最終手段) | `.json` → `.aborted.json` rename(**削除せず**) | 保持(stage=needs_manual_recovery、recovery_reason=`manual_abort_pending_cleanup`、failed_step=`70` 上書き) | `action: 'rollback', rollback_reason: 'manual_abort_pending_cleanup'` 書込 | `needs_manual_recovery { reason: 'manual_abort_pending_cleanup' }` append | block 継続(operator が手動修復を行う状態) |
| `--finalize-cleanup --remote-head <sha> --registry-head <sha>` | `.aborted.json` 存在 AND operator が渡した両 SHA が現実と一致 | `.aborted.json` 削除 | 削除 | 不変(既に rollback) | `completed { detail: 'manual_cleanup_finalized' }` append | block 解除、次 PR 進行可 |
| `--clear-sentinel` | 最終手段、operator 全責任 | `.json` or `.aborted.json` 削除 | 不変 | 不変 | **条件付き append**: `processed.jsonl` の run_id 末尾 event が terminal (`needs_manual_recovery` 等) なら append しない(二重 terminal 禁止)。末尾が non-terminal なら `completed { detail: 'sentinel_manually_cleared' }` を append。いずれも resume は blocked(sentinel 削除で detect 再 queue もされない) | block 解除、整合性は operator 保証 |

refuse cell は全て「stderr に safe matrix + suggested_action を print、state 変更なし」で戻る。

### Rollback / retry semantics 明確化

- `action: rollback` は **per-PR terminal**: その run_id は再試行されない(detect が同 PR を再 queue しない)
- scheduler は **次 PR へ通常進行** する(pipeline 全体の停止ではない)
- 手動で失敗 PR を再試行したい場合は `auto-improve run --pr <n> --from-scratch` で新 run_id を払い出して再実行
- `needs_manual_recovery` は per-PR terminal **かつ** pipeline 全体停止(sentinel 存在中は全 run の step70 を block)— `recover` でのみ解除

---

## エラー時の挙動(共通)

| 状況 | 記録 | 次 step |
|---|---|---|
| step 20/50 agent timeout | `manifest.json` に `kind: "timeout"` で書かれる(なければ orchestrator が `timeout` event append) | その agent は採点対象外 |
| step 20/50 全agent 失敗 | 全 manifest 不在 → `failed` event(orchestrator) | step 30 以降 skip |
| step 30 ジャッジ失敗 | リトライ2回、それでもダメなら `failed` event | step 40 skip |
| step 40 候補0件 | `candidates.json.candidates: []` | step 50-60 skip → best維持 → `completed` event |
| step 70 transactional失敗 (安全 revert 可) | `decision.json.action: "rollback"` + best_branch を `best_sha_before` に force-with-lease revert + `rollback` event | 次 run から再試行可 |
| step 70 transactional失敗 (remote divergence / lease failure) | intention.json を `stage: 'needs_manual_recovery'` 保持 + `<runs_base>/needs-recovery/<run_id>.json` durable sentinel + `processed.jsonl` に `needs_manual_recovery` event、flock は process exit で release(durable block は sentinel) | **次 run ブロック (sentinel 存在中)**、operator の `auto-improve recover` 要 |
| step 70 crash (intention 残存) | 起動時 lock 取得 + recovery state machine (6 状態) で stage から再開 | 詳細は step 70 節 |

---

## 並列実装時の約束(agent向け)

1. **Schemas は `internal/contracts/` だけ編集可**。他 step の schema を勝手に変えない。schema 変更時は `docs/design/io-contracts.md` も同一 commit で更新(pre-commit hook で強制、contracts 変更時のみ)。
2. **io helper を使う**。生 `os.WriteFile` を直接呼ばない(atomic 保証が消える)。
3. **run_layout で与えられたパスを使う**。自前で path 組み立てない。
4. **schema 違反は即 error return**。下流の壊れたファイルを作らない。
5. **採点時は `LoadScorableManifest` を使う**(success-only 契約)、completion 判定のみなら `LoadFinalizedManifest`。生 `os.Stat` で判定しない。
6. **外部テキストは `SanitizeForPromptEmbedding`**(生 string を prompt に入れない)。
7. **`CollapseByKey` を使う**(append-only の後勝ち reduce を自前で書かない)。
8. **`processed.jsonl` への append は orchestrator のみ**。例外: (a) step 10 の `started`、(b) step 70 が `promotion.lock` 保持中に限り `promoting / promoted / rollback / needs_manual_recovery / registry_size_high / registry_size_critical` を自己 append 可。他の全 step は typed result を orchestrator に返却する契約。
9. **既存テストを壊さない**。

---

## schema 変更時のプロトコル

- `internal/contracts/` を変える commit は独立させる(並列実装中の step コードと混ぜない)。
- `DisallowUnknownFields` 付きの reader で動くため field 削除 / discriminator 変更は breaking。既存 runs/ を読む `ReadJSONL` が error を返す可能性あり、migration コメント必須。
- 互換を保った拡張(pointer optional field 追加、enum に要素追加)は安全。
- contracts 変更時は **`docs/design/io-contracts.md` を同一 commit で更新**(pre-commit hook `scripts/check-contracts-sync.sh` で強制、一方向 contracts→docs のみ)。

schema 変更後:
1. 影響を受ける step のテストを追加/更新
2. `go build ./...` で型チェック
3. `go test ./...` で全テスト pass
4. union 変更なら 失敗系テスト(unknown key / wrong kind / trailing token)追加
