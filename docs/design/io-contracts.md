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
│   │           ├── ignored/             # git clean -fdx で消える ignored files
│   │           ├── ignored-skipped.txt  # ignored の symlink / non-regular / too-large 記録
│   │           ├── ignored.txt          # ignored ファイル list
│   │           └── state.json           # 各 artifact の sha256 + dirty_fingerprint
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
    └── intention.json                   # rev11: staged transaction (planning→finalized。needs_manual_recovery 中は保持)
```

**外部(run ディレクトリの外)**:
- `<runs_base>/processed.jsonl` — 全 run 共通の state log
- `<runs_base>/rules-registry.jsonl` — 全 PR 横断の rule ライフサイクル正本
- `<runs_base>/rules-idempotency-index.jsonl` — rev11: 閾値超過時 自動生成、rebuildable cache
- `<runs_base>/rules/<rule_id>.md` — rev11: rule 本体 sidecar(registry は path + sha256 のみ)
- `<runs_base>/promotion.lock` — rev11: step70/recover/sunset_tick 共有 flock
- `<runs_base>/needs-recovery/<run_id>.json` or `.aborted.json` — rev11: durable sentinel
- `<runs_base>/sunset-running.marker` / `last-sunset-at` — rev11: sunset tick state(rev18: lock は `promotion.lock` を共有、`.sunset-lock` 廃止)

`rescued/<rescue_id>/state.json` の `dirty_fingerprint` は、rescue capture 時点の
tracked diff、staged diff、untracked files、ignored files を要約した採用ガードである。
通常ファイルは内容 sha256、symlink / non-regular / size limit 超過ファイルは capture の
skip 記録と同等の metadata を fingerprint に含める。`git reset --hard` と
`git clean -fdx` が破棄しうる file class すべてを対象にし、既存 rescue dir を
再利用する場合は現在 worktree の fingerprint と一致しなければならない。
空文字は旧形式/unknown と扱い、採用してはならない。

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
  - 1500 件超 → alert log + **`<runs_base>/rules-idempotency-index.jsonl` 自動生成有効化(以降の step70/sunset append で同 transaction に index 行を append)**。`processed.jsonl` に `registry_size_high` event を append:
    - step70 emit: `{ kind: 'registry_size_high', source: 'step70', pr, run_id, step: '70', count, ... }`
    - sunset_tick emit: `{ kind: 'registry_size_high', source: 'sunset_tick', count, ... }`
  - 1800 件超 → 上記 + index lookup を mandatory(tail scan は fallback のみ)
  - 2000 件超 → hard alert(`processed.jsonl` に `registry_size_critical` event を append + stderr 強制、operator は retention/compaction 検討):
    - step70 emit: `{ kind: 'registry_size_critical', source: 'step70', pr, run_id, step: '70', count, ... }`
    - sunset_tick emit: `{ kind: 'registry_size_critical', source: 'sunset_tick', count, ... }`
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

**`rules-registry.jsonl` row schemas** (strict decode は以下の closed union のみ受理):
- `RuleRegistryAdded`
  - required: `kind='added'`, `schema_version`, `rule_id`, `rule_path`, `sha256`, `idempotency_key`, `version_seq`, `by_run_id`, `at`
  - `prev_hash`: `version_seq==1` のときのみ empty string、`version_seq>1` のときは必須かつ直前 row の hash
- `RuleRegistryUpdated`
  - required: `kind='updated'`, `schema_version`, `rule_id`, `rule_path`, `sha256`, `prev_sha256`, `idempotency_key`, `version_seq`, `by_run_id`, `at`
  - `prev_hash`: `version_seq==1` のときのみ empty string、`version_seq>1` のときは必須かつ直前 row の hash
- `RuleRegistryRolledBack`
  - required: `kind='rolled_back'`, `schema_version`, `target_op_id`, `target_offset`, `target_sha256`, `by_run_id`, `rollback_reason`, `failed_step`, `version_seq`, `at`
  - `prev_hash`: `version_seq==1` のときのみ empty string、`version_seq>1` のときは必須かつ直前 row の hash
- `RuleRegistryStatusChanged`
  - required: `kind='status_changed'`, `schema_version`, `rule_id`, `prev_status`, `new_status`, `transition`, `op_id`, `version_seq`, `by_sunset_run_id`, `at`
  - `prev_hash`: `version_seq==1` のときのみ empty string、`version_seq>1` のときは必須かつ直前 row の hash
  - valid transitions: `active -> deprecated` with `transition='deprecate'`, `deprecated -> active` with `transition='activate'`
- `RuleRegistryArchived`
  - required: `kind='archived'`, `schema_version`, `rule_id`, `prev_status`, `new_status`, `op_id`, `version_seq`, `by_sunset_run_id`, `at`
  - `prev_hash`: `version_seq==1` のときのみ empty string、`version_seq>1` のときは必須かつ直前 row の hash
  - valid transition: `prev_status in {active, deprecated}` and `new_status='archived'`
- `RuleRegistryRestored`
  - required: `kind='restored'`, `schema_version`, `rule_id`, `prev_status`, `new_status`, `op_id`, `version_seq`, `by_sunset_run_id`, `at`
  - `prev_hash`: `version_seq==1` のときのみ empty string、`version_seq>1` のときは必須かつ直前 row の hash
  - valid transition: `prev_status='archived'` and `new_status in {active, deprecated}`

`contracts.RuleIdempotencyIndexEntry`

| field | type | required | constraints / notes | Go xref |
|---|---|---|---|---|
| `idempotency_key` | `string` | yes | `sha256_hex` | `contracts.RuleIdempotencyIndexEntry.IdempotencyKey` |
| `registry_offset` | `int64` | yes | `>= 0`。field presence 必須 | `contracts.RuleIdempotencyIndexEntry.RegistryOffset` |
| `registry_sha256` | `string` | yes | `sha256_hex` | `contracts.RuleIdempotencyIndexEntry.RegistrySha256` |
| `kind` | `string` | yes | `added | updated | rolled_back | status_changed | archived | restored` | `contracts.RuleIdempotencyIndexEntry.Kind` |
| `at` | `time.Time` | yes | RFC3339 timestamp | `contracts.RuleIdempotencyIndexEntry.At` |

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

**Event vocabulary 拡張** (rev7: non-terminal event は原則 `step` required、resume cursor に使用。`source: 'sunset_tick'` の global telemetry warning のみ `step` forbidden):
- `started { pr, run_id, step: 10, at }` — 起動記録、step は暗黙 10(step10 開始前の意味)
- `step_done { pr, run_id, step, at }` — step 完了、step は完了した step 番号
- `interrupted { pr, run_id, step, reason: 'rate_limit' | 'budget' | 'context' | 'signal' | 'unknown' | 'pre_push_crash', detail?, detail_overflow_ref?, at }` — **non-terminal**、resume 対象、step は中断時に実行していた step。`pre_push_crash` は step70 planning 中 push 未到達 crash 専用(recovery で同 intention を保持して再開)

`detail` は 300 字 cap、超過時は `<run>/processed-details/<sha256>.txt` に sidecar、`detail_overflow_ref: { path, sha256 }` を付与。panic stack trace / long CLI stderr を安全に記録するため。

**Terminal events (resume対象外、detect で再 queue しない)**: `completed / failed / promoted / rollback / skipped / timeout / needs_manual_recovery`
**Non-terminal events (resume対象)**: `started / step_done / interrupted / promoting / registry_size_high / registry_size_critical / rescue_retry`

- `promoting { pr, run_id, step: 70, at }` — step70 の intention planning 完了時 append。resume 時は intention.json から stage を読んで判断。
- 運用 alert 3 event(`registry_size_high` / `registry_size_critical` / `rescue_retry`): rev33 で warning sub-kind を廃止し event kind として直接昇格(Codex rev32 drift 修正)。共通 schema:
  - `registry_size_high` / `registry_size_critical`: `{ kind, source, pr?, run_id?, step?, count, detail?, detail_overflow_ref?, at }`
    - `source: 'step70'` のときのみ `step: '70'` 必須、かつ `pr` / `run_id` 必須
    - `source: 'sunset_tick'` のとき `step` は **省略必須**、`pr` / `run_id` も **省略必須**(global telemetry)
  - `rescue_retry`: `{ kind, pr, run_id, step, detail?, detail_overflow_ref?, at }`
    - `step` は `'20' | '50'`
    - `source` field は持たない
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
  - helper `internal/contracts/canonical.go#CanonicalMarshal(v) ([]byte, error)` を用意(Phase 0-bootstrap で実装)
  - 同 helper を全 hash 生成箇所で使う(struct field 定義順依存を禁止)
  - **fixture test** (rev21 拡張):
    (a) field 順 struct 変更で hash 不変
    (b) nested object field 順変更で hash 不変
    (c) integer 値(Go int / int64、値範囲 `int64` 全域)の hash 安定 — `UseNumber()` 未使用時にこの test が落ちることが既知、実装者が気付けるよう fixture に明記
    (d) **float64 フィールド → error を返す**(Decode 時 `json.Number.Int64()` 失敗で error)
    (e) NaN / Inf → error
    (f) HTML 特殊文字を含む string の escape 形式 安定

**Raw row schema (step30 / step60 共通、Go 契約に同期)**:
- `scores-A-raw.jsonl` / `scores-B-raw.jsonl` の各 row = `RawScoreEntry`:
  - required: `schema_version`, `run_id`, `pass`, `agent`, `judge_role`, `dimension`, `score`, `output_sha256`, `rubric_version`, `prompt_version`, `resolved_at`
  - optional: `reasons`, `reasons_overflow_ref`, `primary_ref`, `secondary_ref`
  - `primary_ref` / `secondary_ref` object shape: `{ role, sha256 }`
  - `judge_role: 'arbiter'` row は `primary_ref` と `secondary_ref` の **両方必須**
  - `judge_role: 'primary' | 'secondary'` row は `primary_ref` / `secondary_ref` の **両方禁止**
  - arbiter row の `primary_ref.role` は `primary`、`secondary_ref.role` は `secondary` 固定
- `compliance-A-raw.jsonl` / `compliance-B-raw.jsonl` の各 row = `RawComplianceEntry`:
  - required: `schema_version`, `run_id`, `pass`, `agent`, `judge_role`, `rule_id`, `verdict`, `output_sha256`, `rubric_version`, `prompt_version`, `resolved_at`
  - optional: `rationale`, `rationale_overflow_ref`, `primary_ref`, `secondary_ref`
  - `primary_ref` / `secondary_ref` object shape と arbiter / non-arbiter 制約は `RawScoreEntry` と同一

`contracts.RawScoreEntry`

| field | type | required | constraints / notes | Go xref |
|---|---|---|---|---|
| `schema_version` | `string` | yes | `"1"` | `contracts.RawScoreEntry.SchemaVersion` |
| `run_id` | `RunID` | yes | `run_id_fmt` | `contracts.RawScoreEntry.RunID` |
| `pass` | `int` | yes | `1 | 2` | `contracts.RawScoreEntry.Pass` |
| `agent` | `AgentID` | yes | `agent_id_fmt` | `contracts.RawScoreEntry.Agent` |
| `judge_role` | `string` | yes | `primary | secondary | arbiter` | `contracts.RawScoreEntry.JudgeRole` |
| `dimension` | `string` | yes | `fidelity | correctness | maintainability | discipline | communication` | `contracts.RawScoreEntry.Dimension` |
| `score` | `int` | yes | `0..100`; integer only | `contracts.RawScoreEntry.Score` |
| `reasons` | `string` | no | `max=1000` | `contracts.RawScoreEntry.Reasons` |
| `reasons_overflow_ref` | `OverflowRef` | no | overflow 時、`30/` or `60/` 配下 | `contracts.RawScoreEntry.ReasonsOverflowRef` |
| `output_sha256` | `string` | yes | `sha256_hex` | `contracts.RawScoreEntry.OutputSha256` |
| `primary_ref` | `RawJudgeRef` | conditional | `judge_role='arbiter'` で必須、それ以外は禁止 | `contracts.RawScoreEntry.PrimaryRef` |
| `secondary_ref` | `RawJudgeRef` | conditional | `judge_role='arbiter'` で必須、それ以外は禁止 | `contracts.RawScoreEntry.SecondaryRef` |
| `rubric_version` | `string` | yes | non-empty | `contracts.RawScoreEntry.RubricVersion` |
| `prompt_version` | `string` | yes | non-empty | `contracts.RawScoreEntry.PromptVersion` |
| `resolved_at` | `time.Time` | yes | RFC3339 timestamp | `contracts.RawScoreEntry.ResolvedAt` |

`contracts.RawComplianceEntry`

| field | type | required | constraints / notes | Go xref |
|---|---|---|---|---|
| `schema_version` | `string` | yes | `"1"` | `contracts.RawComplianceEntry.SchemaVersion` |
| `run_id` | `RunID` | yes | `run_id_fmt` | `contracts.RawComplianceEntry.RunID` |
| `pass` | `int` | yes | `1 | 2` | `contracts.RawComplianceEntry.Pass` |
| `agent` | `AgentID` | yes | `agent_id_fmt` | `contracts.RawComplianceEntry.Agent` |
| `judge_role` | `string` | yes | `primary | secondary | arbiter` | `contracts.RawComplianceEntry.JudgeRole` |
| `rule_id` | `string` | yes | non-empty | `contracts.RawComplianceEntry.RuleID` |
| `verdict` | `string` | yes | `compliant | violated | valid_exception | invalid_exception | missed | n_a` | `contracts.RawComplianceEntry.Verdict` |
| `rationale` | `string` | no | `max=500` | `contracts.RawComplianceEntry.Rationale` |
| `rationale_overflow_ref` | `OverflowRef` | no | overflow 時、`30/` or `60/` 配下 | `contracts.RawComplianceEntry.RationaleOverflowRef` |
| `output_sha256` | `string` | yes | `sha256_hex` | `contracts.RawComplianceEntry.OutputSha256` |
| `primary_ref` | `RawJudgeRef` | conditional | `judge_role='arbiter'` で必須、それ以外は禁止 | `contracts.RawComplianceEntry.PrimaryRef` |
| `secondary_ref` | `RawJudgeRef` | conditional | `judge_role='arbiter'` で必須、それ以外は禁止 | `contracts.RawComplianceEntry.SecondaryRef` |
| `rubric_version` | `string` | yes | non-empty | `contracts.RawComplianceEntry.RubricVersion` |
| `prompt_version` | `string` | yes | non-empty | `contracts.RawComplianceEntry.PromptVersion` |
| `resolved_at` | `time.Time` | yes | RFC3339 timestamp | `contracts.RawComplianceEntry.ResolvedAt` |

**Raw file retention mandatory** (rev15、Codex rev14 R3 対応): `scores-*-raw.jsonl` / `compliance-*-raw.jsonl` は **削除禁止**(raw_hashes verify の source of truth)。retention policy は将来課題、Phase 0 では無期限保持。
- resume 時 marker 不在なら必ず cardinality 再計算し未完了分だけ launch

**Signal handling (orchestrator)**:
- SIGTERM / SIGINT 受信時: 現在実行中の step を graceful stop → `processed.jsonl` に `interrupted { pr, run_id, step, reason: 'signal', at }` append → flock release → exit 0
- claude / codex CLI が rate limit (429, "quota") を返した場合: `interrupted { pr, run_id, step, reason: 'rate_limit', at }` append → exit 0。次 tick で resume
- budget depletion: `interrupted { pr, run_id, step, reason: 'budget', at }` append → exit 0
- 予期せぬ panic: defer で `interrupted { pr, run_id, step, reason: 'unknown', detail?, at }` append を試みる(best effort)

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

safe := autoio.SanitizeForPromptEmbedding(pkg.ReconstructedTaskPrompt, autoio.SafeTextOptions{
    Label: "task_prompt",
    Fence: true,
})
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
- サイズ上限は caller 側の artifact / prompt builder で管理する

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
  - required: `schema_version`, `run_id`, `candidate_id`, `kind`, `similarity_score`, `classified_at`
  - optional: `matched_rule_id`, `rationale`, `rationale_overflow_ref`
  - `kind`: `new | update | duplicate`
  - `similarity_score`: `0..100` の integer
  - `rationale` は 500 字 cap、超過時 `rationale_overflow_ref`
  - `problem` field は存在しない

`contracts.ClassificationEntry`

| field | type | required | constraints / notes | Go xref |
|---|---|---|---|---|
| `schema_version` | `string` | yes | `"1"` | `contracts.ClassificationEntry.SchemaVersion` |
| `run_id` | `RunID` | yes | `run_id_fmt` | `contracts.ClassificationEntry.RunID` |
| `candidate_id` | `string` | yes | non-empty | `contracts.ClassificationEntry.CandidateID` |
| `kind` | `string` | yes | `new | update | duplicate` | `contracts.ClassificationEntry.Kind` |
| `similarity_score` | `int` | yes | `0..100`; integer only | `contracts.ClassificationEntry.SimilarityScore` |
| `matched_rule_id` | `string` | no | `kind=update|duplicate` で参照可 | `contracts.ClassificationEntry.MatchedRuleID` |
| `rationale` | `string` | no | `max=500` | `contracts.ClassificationEntry.Rationale` |
| `rationale_overflow_ref` | `OverflowRef` | no | overflow 時、`40/` 配下 | `contracts.ClassificationEntry.RationaleOverflowRef` |
| `classified_at` | `time.Time` | yes | RFC3339 timestamp | `contracts.ClassificationEntry.ClassifiedAt` |

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
- boundary invariants (reject 必須):
  - `task_package.run_id == candidates.run_id`
  - `candidates_hash` は `candidates[]` の **canonical JSON**(object key lexicographical sort / HTML escaping 無効 / deterministic number rendering)から再計算して一致しなければ reject
  - `decision.json` は outer `action` == inner variant type == inner `action` field を満たさなければ reject
  - adopt variant の `idempotency_key` は `sha256(run_id + target_sha + best_sha_before + candidates_hash)` と一致しなければ reject (`||` は separator 無しの文字列連結)
- adopt の場合、**staged transaction + idempotent recovery**:

**Stage 遷移(normal path)**:
1. **planning**: `intention.json` atomic write `{schema_version, stage: 'planning', idempotency_key, run_id, best_sha_before, target_sha, candidates_hash, registry_head_before, started_at}`。
  - `idempotency_key` = `sha256(run_id + target_sha + best_sha_before + candidates_hash)`。planning で1回だけ生成し以降 reuse。`||` は doc 上の concat 記法であり separator byte は入れない。
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
5. **policy_publishing**: `repo.policy_branch` 設定時のみ、policy branch への rule sidecar publish 開始前に `policy_branch`, `policy_head_before`, `policy_head_after`(publish plan 作成後)を記録し、intention を `stage: 'policy_publishing'` に overwrite。未設定時は skip。
6. **policy_published**: policy branch への publish 完了後、`policy_head_after` を remote HEAD として確認し、intention を `stage: 'policy_published'` に overwrite。既に publish 済みで remote HEAD が `policy_head_after` の場合は idempotent に再開。
7. **decision_written**: `decision.json` atomic write (`action: 'adopt'`)。intention を `stage: 'decision_written'` に overwrite
8. **finalized**: intention.json を delete (committed marker)
9. `processed.jsonl` に `promoted` append

**Recovery state machine** (起動時、lock 取得後、state 再読込):

startup 時の基準は `intention.json.stage`。contract として許可される persisted stage は `internal/contracts/intention.go` の enum と一致し、`planning` / `branch_pushed` / `registry_appended` / `policy_publishing` / `policy_published` / `decision_written` / `rolling_back_branch_reverted` / `rolling_back_registry_appended` / `rolling_back_decision_written` / `needs_manual_recovery` の 10 個に限る。加えて、`finalized` は intention 削除後の終端 pseudo-stage として扱う。

`intention` absent + `decision` absent は persisted stage ではなく通常起動(step70 を最初から開始)である。

| stage | entry condition | allowed next transitions | required fields | startup / recovery 動作 |
|---|---|---|---|---|
| `planning` | planning snapshot を atomic write した直後。branch push / registry append / decision write は未確定 | `branch_pushed`; `needs_manual_recovery`; intention 削除後の fresh planning | `idempotency_key`, `run_id`, `best_sha_before`, `target_sha`, `candidates_hash`, `registry_head_before`, `started_at`。`registry_head_before` は **physical field presence 必須**。値 `""` は planning 時に registry が空だった genesis case のみを表す。`registry_append_result` / `recovery_reason` / `failed_step` は不要 | 下記 planning decision tree を実行 |
| `branch_pushed` | `git push --force-with-lease` 成功後、registry append 前 | `registry_appended`; `rolling_back_branch_reverted`; `needs_manual_recovery` | `planning` と同じ | Stage 4 の registry append 判定(a: idempotency hit, b: CAS append, c: divergence→rollback path)を最初から再実行 |
| `registry_appended` | registry append 完了後、`registry_append_result` を保持している | `policy_publishing`; `policy_published`; `decision_written`; `rolling_back_branch_reverted`; `needs_manual_recovery` | `branch_pushed` の必須項目 + `registry_append_result` 必須 | rule sidecar publish → policy branch publish(`repo.policy_branch` 設定時) → decision write → promoted append → intention delete を再実行 |
| `policy_publishing` | `repo.policy_branch` 設定時、policy branch publish の事前 snapshot / publish plan を intention に記録済み | `policy_published`; `needs_manual_recovery` | `registry_appended` の必須項目 + `policy_branch` + `policy_head_before` 必須。`policy_head_after` は publish plan 作成後に populate され、既 publish retry 判定に使う | policy branch の current HEAD を確認し、未 publish なら同じ plan を push、既に `policy_head_after` なら `policy_published` へ進む。不一致は `needs_manual_recovery` |
| `policy_published` | policy branch publish 完了後、`policy_head_after` を保持している | `decision_written`; `needs_manual_recovery` | `policy_publishing` の必須項目 + `policy_head_after` 必須 | policy branch remote HEAD が `policy_head_after` と一致することを確認し、decision write → promoted append → intention delete を再実行 |
| `decision_written` | `decision.json`(adopt) 書込み済み、intention 削除前 | `finalized` | `registry_appended` と同じ(`registry_append_result` 必須)。policy publish を実行した run では `policy_branch`, `policy_head_before`, `policy_head_after` も保持 | intention 削除 + `processed.jsonl` の `promoted` append を exactly-once で完了 |
| `rolling_back_branch_reverted` | rollback path で `best_branch` を `best_sha_before` に戻せた直後 | `rolling_back_registry_appended`; `needs_manual_recovery`; または `finalized` に向けた rollback 完了処理 | `planning` の必須項目 + `recovery_reason` + `failed_step` 必須。`registry_append_result` は code 上 optional | rollback を再開。`registry_append_result` がある場合のみ次の persisted rollback stage に進める。契約上この stage だけは rollback metadata を持ちながら `registry_append_result` を要求しない |
| `rolling_back_registry_appended` | rollback entry append 済み、rollback decision write 前 | `rolling_back_decision_written`; `needs_manual_recovery` | `rolling_back_branch_reverted` の必須項目 + `registry_append_result` 必須 | rollback decision write を再実行し、その後 rollback terminal append と intention delete へ進む |
| `rolling_back_decision_written` | rollback decision.json 書込み済み、terminal rollback append / intention delete 前 | `finalized`; `needs_manual_recovery` | `rolling_back_registry_appended` と同じ(`registry_append_result` + `recovery_reason` + `failed_step` 必須) | `processed.jsonl` の `rollback` append を exactly-once で完了し、intention を delete |
| `needs_manual_recovery` | 自動 rollback 不能。sentinel と一緒に intention を保持して operator 介入待ち | `finalized`(recover CLI による cleanup 完了後) のみ | `planning` の必須項目 + `recovery_reason` + `failed_step` 必須。`registry_append_result` は failure point に応じて optional | 自動進行しない。alert log + exit。`auto-improve recover` のみが進める |
| `finalized` | intention 削除済み。decision / registry / branch は terminal 状態にある | なし | `intention.json` 不在。terminal event は persisted `decision.json` に応じて `promoted` または `rollback` を append 済み、未appendなら startup 時に補完 | terminal event が既にあれば noop、無ければ exactly-once append |

**planning recovery decision tree** (rev20、Codex rev19 Claude Critical #2 対応):
```
1. remote <best_branch> HEAD を取得
2. if HEAD == target_sha:
     push 済と判定 → stage=branch_pushed 相当として Stage 4 判定へ進む
     (registry check は Stage 4 内で実行される、ここでは不要)
3. else if HEAD == best_sha_before:
     push 未到達(side-effect ゼロ)と判定 — **registry が動いていても harmless、intention snapshot を refresh して re-planning**(rev31、Codex rev29 遅延指摘対応: global stop 回避)
     registry freshness check: if current_registry_head == intention.registry_head_before:
       intention.json 保持 + interrupted { pr, run_id, step: 70, reason: 'pre_push_crash', at } append (non-terminal、同 run 再 tick で planning から resume)
     else:
       **intention.json 削除** + interrupted { pr, run_id, step: 70, reason: 'pre_push_crash', detail: 'registry advanced during planning crash, snapshot refresh required', at } append (non-terminal、同 run 再 tick で **新 planning から開始**、fresh snapshot 取得し直し)
4. else (HEAD が想定外):
     needs_manual_recovery { pr, run_id, step: '70', reason: 'remote_divergence', failed_step: '70', at } + sentinel atomic write
```

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

**reason × failed_step pairing**:
- `lease_failure | remote_divergence | registry_divergence | manual_abort_pending_cleanup | transactional_failure` → `failed_step == '70'`
- `worktree_rescue_loop` → `failed_step == '20' | '50'`

**Block 機構**:
- `<runs_base>/needs-recovery/<run_id>.json` は durable な sentinel file(`{run_id, pr, reason, failed_step, created_at}` を持つ)
- sentinel schema:
  - `{ run_id, pr, reason, failed_step, created_at }`
  - `run_id`: `run_id_fmt`
  - `pr`: `> 0`
  - `reason`: `lease_failure | remote_divergence | registry_divergence | worktree_rescue_loop | manual_abort_pending_cleanup | transactional_failure`
  - `failed_step`: `"10" | "20" | "30" | "40" | "50" | "60" | "70"`
  - `created_at`: RFC3339 timestamp
- **全 CLI gate 統一規約** (rev26、Claude rev26 H2 対応: **deny-by-default allowlist**): 起動時最初のアクションで `<runs_base>/needs-recovery/` ディレクトリ全体を scan(`*.json` と `*.aborted.json` 両方対象、suffix filter で `.aborted.json` も必ず拾う)。1 件でも存在する run_id があれば **non-terminal だが block** として扱い即 exit 0(side-effect なし)。**唯一の allowlist**: `recover` サブコマンドのみ(将来 `status` / `doctor` 等の read-only 診断系を追加する場合は明示的に allowlist 拡張が必要、default は全 block)
- **全 step block**(rev19、Codex rev18 R2 対応): sentinel 存在中は step10/20/30/40/50/60/70 と sunset の **全て** を pre-flight gate で reject。**唯一の例外は `auto-improve recover --run <id>`** のみ(operator 介入専用)
- 全 PR レベル block(本プロジェクトは PR 間直列): any sentinel 存在 → **全 run の全 step + sunset を block**(poisoned baseline / registry / best_branch 汚染保護)
- 上記 3 規約は一貫: CLI 層で gate、step 層で gate、PR 層で gate、三重 check で TOCTOU race も含めて block 保証
- sentinel 解除は `auto-improve recover` CLI のみが行う
- `processed.jsonl` にも `needs_manual_recovery { pr, run_id, step, reason, failed_step, at }` event を append(`IsTerminal = true` として扱う、detect が再 queue しない)

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
     1. **registry rollback entry append**(`intention.registry_append_result` が non-null の場合のみ): `rules-registry.jsonl` に `{kind: 'rolled_back', schema_version, target_op_id, target_offset, target_sha256, by_run_id, rollback_reason, failed_step, version_seq, prev_hash, at}` を append。append 前に末尾 **N=2000** 件(promotion idempotency と同定数、`RegistryTailScanN` 定数で共通化)を tail scan し同 `target_op_id` の `rolled_back` entry が既存なら skip(idempotent)。1500+ で idempotency-index.jsonl も併用。reader は CollapseByKey 的に `rolled_back` 付き op を無効扱い。
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
   - `processed.jsonl` に `needs_manual_recovery { pr, run_id, step, reason, failed_step, at }` event append(**terminal event 集合に含める**、detect/requeue 側で non-retryable)
   - flock release(process exit で自動解放)
   - operator alert: slog error + stderr 必須
   - **次 tick は sentinel 存在検知で新規 step70 を block**(本 run でも他 run でも)

**出力**:
- `<run>/70/decision.json` (`DecisionSchema`, variant: `adopt | reject | noop | rollback`)
- `<run>/70/intention.json` (一時、finalize または rollback で削除。needs_manual_recovery のときのみ保持)
- `<runs_base>/promotion.lock` (flock ファイル、step70/recover/sunset_tick 共有)
- `<runs_base>/needs-recovery/<run_id>.json` (durable sentinel、needs_manual_recovery 時のみ)
- `rules-registry.jsonl` 追記(採用時、idempotency_key 付き)

## Go契約フィールドリファレンス

source of truth は `internal/contracts/**` と `internal/contracts/stepio/**`。以下は Go 実装と同期させた field table で、required 列は **Go validator / custom unmarshal が物理存在を要求するか** を示す。`bool` / `int` の zero value が合法で presence を見ていない field は `no` と明記する。

### Step10

`stepio.Step10Request`

| field | type | required | constraints / notes | Go xref |
|---|---|---|---|---|
| `pr` | `int` | yes | `> 0` | `stepio.Step10Request.PR` |
| `best_branch` | `string` | yes | non-empty | `stepio.Step10Request.BestBranch` |
| `expected_run_id` | `RunID` | no | `run_id_fmt`; stale replay guard、指定時は `response.run_id` と一致必須 | `stepio.Step10Request.ExpectedRunID` |
| `harness_files` | `bool` | no | zero value `false` も decode 可 | `stepio.Step10Request.HarnessFiles` |

`stepio.Step10Response`

| field | type | required | constraints / notes | Go xref |
|---|---|---|---|---|
| `run_id` | `RunID` | yes | `run_id_fmt`; `task_package.run_id` と一致 | `stepio.Step10Response.RunID` |
| `task_package` | `TaskPackage` | yes | 下表参照 | `stepio.Step10Response.TaskPackage` |
| `base_sha` | `string` | yes | `sha1_hex`; `task_package.base_sha` と一致 | `stepio.Step10Response.BaseSHA` |
| `worktrees_created` | `int` | no | `0..6` | `stepio.Step10Response.WorktreesCreated` |

`DecodeAndValidateStep10Response` の request-bound invariant:
- `response.task_package.pr == request.pr`
- `response.task_package.best_branch == request.best_branch`
- `request.expected_run_id != ""` の場合は `response.run_id == request.expected_run_id`
- `Step10Request` 自体は `run_id` / `base_sha` を持たないため、そこは response-local invariant (`response.run_id == response.task_package.run_id`, `response.base_sha == response.task_package.base_sha`) として `Step10Response.Validate()` 側で担保する

bootstrap-1 では stale replay guard は `expected_run_id` を採用し、`nonce` 方式は follow-up 比較案として残す。

### Step20 / Step50

`stepio.Step20Request`

| field | type | required | constraints / notes | Go xref |
|---|---|---|---|---|
| `task_package` | `TaskPackage` | yes | `worktrees` の pass=1 側と `agents` が集合一致 | `stepio.Step20Request.TaskPackage` |
| `agents` | `[]AgentID` | yes | `unique`, 各要素 `agent_id_fmt` | `stepio.Step20Request.Agents` |
| `timeout_seconds` | `int` | yes | `> 0` | `stepio.Step20Request.TimeoutSeconds` |

`stepio.Step20Response`

public API は opaque wrapper。writer は `NewStep20Response(...)`、reader は `DecodeAndValidateStep20Response(...)` を使う。`json.Unmarshal` 直呼びで strict decode はできるが request-bound にはならず、getter / `MarshalJSON` / `Validate` は `ErrStep20ResponseNotBound` を返す。

| field | type | required | constraints / notes | Go xref |
|---|---|---|---|---|
| `run_id` | `RunID` | yes | `run_id_fmt` | `stepio.Step20Response.RunID()` |
| `pass` | `int` | yes | `== 1` | `stepio.Step20Response.Pass()` |
| `results` | `[]Step20AgentResult` | no | 各 `manifest` は下表の `Manifest` strict union | `stepio.Step20Response.Results()` |
| `rescue_exhausted` | `[]RescueExhausted` | no | omitted 可 | `stepio.Step20Response.RescueExhausted()` |

`stepio.Step50Request`

| field | type | required | constraints / notes | Go xref |
|---|---|---|---|---|
| `task_package` | `TaskPackage` | yes | `worktrees` の pass=2 側と `agents` が集合一致 | `stepio.Step50Request.TaskPackage` |
| `agents` | `[]AgentID` | yes | `unique`, 各要素 `agent_id_fmt` | `stepio.Step50Request.Agents` |
| `timeout_seconds` | `int` | yes | `> 0` | `stepio.Step50Request.TimeoutSeconds` |
| `candidate_rule_ids` | `[]string` | yes | `min=1`, 各要素 non-empty | `stepio.Step50Request.CandidateRuleIDs` |

`stepio.Step50Response`

public API は opaque wrapper。writer は `NewStep50Response(...)`、reader は `DecodeAndValidateStep50Response(...)` を使う。`json.Unmarshal` 直呼びは strict decode のみで request-bound ではなく、getter / `MarshalJSON` / `Validate` は `ErrStep50ResponseNotBound` を返す。

| field | type | required | constraints / notes | Go xref |
|---|---|---|---|---|
| `run_id` | `RunID` | yes | `run_id_fmt` | `stepio.Step50Response.RunID()` |
| `pass` | `int` | yes | `== 2` | `stepio.Step50Response.Pass()` |
| `results` | `[]Step20AgentResult` | no | 各 `manifest` は下表の `Manifest` strict union | `stepio.Step50Response.Results()` |
| `rescue_exhausted` | `[]RescueExhausted` | no | omitted 可 | `stepio.Step50Response.RescueExhausted()` |

`DecodeAndValidateStep20Response` / `DecodeAndValidateStep50Response` の request-bound invariant:
- request-bound: `response.run_id` MUST equal `request.task_package.run_id`; otherwise decoder rejects with `ErrResponseRunIDMismatch`
- `results ∩ rescue_exhausted == ∅`
- `results ∪ rescue_exhausted == req.agents`
- overlap は `ErrAgentResultOverlap`
- missing / injected agent は `ErrAgentCoverageMismatch`

`stepio.Step20AgentResult`

| field | type | required | constraints / notes | Go xref |
|---|---|---|---|---|
| `agent` | `AgentID` | yes | `agent_id_fmt`; `manifest.agent` と一致 | `stepio.Step20AgentResult.Agent` |
| `manifest` | `Manifest` | yes | strict tagged union | `stepio.Step20AgentResult.Manifest` |

`stepio.RescueExhausted`

| field | type | required | constraints / notes | Go xref |
|---|---|---|---|---|
| `agent` | `AgentID` | yes | `agent_id_fmt` | `stepio.RescueExhausted.Agent` |
| `retry_count` | `int` | no | `>= 3`; zero-value omissionは通らず、JSON omit 時は `0` で validator failure | `stepio.RescueExhausted.RetryCount` |

### Step30 / Step60

`stepio.Step30Request`

| field | type | required | constraints / notes | Go xref |
|---|---|---|---|---|
| `task_package` | `TaskPackage` | yes | `scorable_agents` は pass=1 の worktree agent subset | `stepio.Step30Request.TaskPackage` |
| `scorable_agents` | `[]AgentID` | yes | `min=1`, `unique`, 各要素 `agent_id_fmt` | `stepio.Step30Request.ScorableAgents` |
| `rubric_version` | `string` | yes | non-empty | `stepio.Step30Request.RubricVersion` |
| `prompt_version` | `string` | yes | non-empty | `stepio.Step30Request.PromptVersion` |

`stepio.Step30Response`

| field | type | required | constraints / notes | Go xref |
|---|---|---|---|---|
| `run_id` | `RunID` | yes | `run_id_fmt` | `stepio.Step30Response.RunID` |
| `scores_count` | `int` | no | `>= 0` | `stepio.Step30Response.ScoresCount` |
| `compliance_count` | `int` | no | `>= 0` | `stepio.Step30Response.ComplianceCount` |
| `resolved_at` | `time.Time` | yes | RFC3339 timestamp | `stepio.Step30Response.ResolvedAt` |

`DecodeAndValidateStep30Response` の request-bound invariant:
- `response.run_id == request.task_package.run_id`
- `response.scores_count == len(request.scorable_agents) * 5` (5 rubric dimensions 固定)
- `json.Unmarshal` 直呼びは response-local validation までしか見ないため、cross-run replay 拒否は decoder 経由を前提とする

`stepio.Step60Request`

| field | type | required | constraints / notes | Go xref |
|---|---|---|---|---|
| `task_package` | `TaskPackage` | yes | `scorable_agents` は pass=2 の worktree agent subset | `stepio.Step60Request.TaskPackage` |
| `scorable_agents` | `[]AgentID` | yes | `min=1`, `unique`, 各要素 `agent_id_fmt` | `stepio.Step60Request.ScorableAgents` |
| `rubric_version` | `string` | yes | non-empty | `stepio.Step60Request.RubricVersion` |
| `prompt_version` | `string` | yes | non-empty | `stepio.Step60Request.PromptVersion` |

`stepio.Step60Response`

| field | type | required | constraints / notes | Go xref |
|---|---|---|---|---|
| `run_id` | `RunID` | yes | `run_id_fmt` | `stepio.Step60Response.RunID` |
| `scores_count` | `int` | no | `>= 0` | `stepio.Step60Response.ScoresCount` |
| `compliance_count` | `int` | no | `>= 0` | `stepio.Step60Response.ComplianceCount` |
| `pairwise_count` | `int` | no | `>= 0` | `stepio.Step60Response.PairwiseCount` |
| `resolved_at` | `time.Time` | yes | RFC3339 timestamp | `stepio.Step60Response.ResolvedAt` |

`DecodeAndValidateStep60Response` の request-bound invariant:
- `response.run_id == request.task_package.run_id`
- `response.scores_count == len(request.scorable_agents) * 5`
- `response.pairwise_count == len(request.scorable_agents)` (same-agent pairwise のみ)
- `json.Unmarshal` 直呼びは response-local validation までしか見ないため、cross-run replay 拒否は decoder 経由を前提とする

### Step40

`stepio.Step40Request`

| field | type | required | constraints / notes | Go xref |
|---|---|---|---|---|
| `task_package` | `TaskPackage` | yes | 下表参照 | `stepio.Step40Request.TaskPackage` |
| `registry_path` | `string` | yes | clean absolute path + basename 必須 `rules-registry.jsonl`。full root containment は orchestrator 側 | `stepio.Step40Request.RegistryPath` |

`stepio.Step40Response`

| field | type | required | constraints / notes | Go xref |
|---|---|---|---|---|
| `run_id` | `RunID` | yes | `candidates.run_id` と一致 | `stepio.Step40Response.RunID` |
| `candidates` | `Candidates` | yes | 下表参照 | `stepio.Step40Response.Candidates` |
| `candidates_count` | `int` | no | `>= 0`; `len(candidates.candidates)` と一致 | `stepio.Step40Response.CandidatesCount` |

`DecodeAndValidateStep40Response` の request-bound invariant:
- `response.run_id == request.task_package.run_id`
- `response.candidates.run_id == request.task_package.run_id` (`response.run_id == response.candidates.run_id` は response-local invariant)

### Step70

`stepio.Step70Request`

| field | type | required | constraints / notes | Go xref |
|---|---|---|---|---|
| `task_package` | `TaskPackage` | yes | `candidates.run_id` と一致 | `stepio.Step70Request.TaskPackage` |
| `candidates` | `Candidates` | yes | `candidates_hash` は canonical hash と一致 | `stepio.Step70Request.Candidates` |
| `registry_path` | `string` | yes | clean absolute path + basename 必須 `rules-registry.jsonl`。full root containment は orchestrator 側 | `stepio.Step70Request.RegistryPath` |

`stepio.Step70Response`

| field | type | required | constraints / notes | Go xref |
|---|---|---|---|---|
| `run_id` | `RunID` | yes | `decision.run_id` と一致 | `stepio.Step70Response.RunID()` |
| `decision` | `Decision` | yes | strict tagged union | `stepio.Step70Response.Decision()` |
| `promoted` | `bool` | no | `adopt` のときのみ `true`、他 action は `false` | `stepio.Step70Response.Promoted()` |

`Step70Response` は Go 実装では opaque wrapper とし、public access は getter 経由のみ:
- `RunID()`, `Decision()`, `Promoted()`, `MarshalJSON()`, `RequestBound()` が公開 API
- plain `json.Unmarshal` は strict JSON + response-local invariant のみ通すが request bind はしない。`RequestBound() == false` かつ `Validate()` は `ErrStep70ResponseNotBound`
- `RunID()`, `Decision()`, `Promoted()`, `MarshalJSON()` は `RequestBound() == false` の値に対して `ErrStep70ResponseNotBound` を返す
- `DecodeAndValidateStep70Response(data, req)` は request-bound invariant まで通した値だけ返し、`RequestBound() == true` かつ `Validate() == nil`
- producer は `NewStep70Response(runID, decision, promoted, req)` で request-bound 済みの response を生成する

request-bound invariant:
- `req.task_package.run_id == req.candidates.run_id`
- `response.run_id == req.task_package.run_id`
- `response.run_id == decision.run_id`
- `DecisionAdopt.candidates_hash == req.candidates.candidates_hash`
- `DecisionAdopt.idempotency_key == sha256(run_id + target_sha + best_sha_before + candidates_hash)`
- `decision.action == adopt` のときのみ `promoted == true`; `reject | noop | rollback` は `promoted == false`

### Persisted artifacts

`contracts.WorktreeAllocation` (`task-package.json.worktrees[]`)

| field | type | required | constraints / notes | Go xref |
|---|---|---|---|---|
| `agent` | `AgentID` | yes | `agent_id_fmt` | `contracts.WorktreeAllocation.Agent` |
| `pass` | `int` | yes | `1 | 2` | `contracts.WorktreeAllocation.Pass` |
| `path` | `string` | yes | non-empty clean absolute path (`filepath.Clean(path)==path`; `.` / `..` segment 禁止) | `contracts.WorktreeAllocation.Path` |
| `branch` | `string` | yes | non-empty | `contracts.WorktreeAllocation.Branch` |
| `base_sha` | `string` | yes | `sha1_hex` | `contracts.WorktreeAllocation.BaseSHA` |
| `head_sha` | `string` | yes | `sha1_hex` | `contracts.WorktreeAllocation.HeadSHA` |

`contracts.TaskPackage` (`<run>/task-package.json`)

| field | type | required | constraints / notes | Go xref |
|---|---|---|---|---|
| `schema_version` | `string` | yes | closed to `"1"` | `contracts.TaskPackage.SchemaVersion` |
| `run_id` | `RunID` | yes | `run_id_fmt` | `contracts.TaskPackage.RunID` |
| `pr` | `int` | yes | `> 0` | `contracts.TaskPackage.PR` |
| `title` | `string` | yes | non-empty | `contracts.TaskPackage.Title` |
| `base_sha` | `string` | yes | `sha1_hex` | `contracts.TaskPackage.BaseSHA` |
| `best_branch` | `string` | yes | non-empty | `contracts.TaskPackage.BestBranch` |
| `reconstructed_task_prompt` | `string` | yes | non-empty; prompt 埋込前に sanitize 必須 | `contracts.TaskPackage.ReconstructedTaskPrompt` |
| `worktrees` | `[]WorktreeAllocation` | yes | `len=6`; pass1=3件, pass2=3件, pass内 agent unique, 2 pass の agent 集合一致, path/branch 全体一意 | `contracts.TaskPackage.Worktrees` |
| `created_at` | `time.Time` | yes | RFC3339 timestamp | `contracts.TaskPackage.CreatedAt` |

`contracts.Manifest` (`<run>/{20-pass1|50-pass2}/<agent>/manifest.json`)

closed variant set: `success | error | timeout`

`ManifestSuccess`

| field | type | required | constraints / notes | Go xref |
|---|---|---|---|---|
| `kind` | `string` | yes | `success` 固定 | `contracts.ManifestSuccess.Kind` |
| `schema_version` | `string` | yes | `"1"` | `contracts.ManifestSuccess.SchemaVersion` |
| `run_id` | `RunID` | yes | `run_id_fmt` | `contracts.ManifestSuccess.RunID` |
| `pass` | `int` | yes | `1 | 2` | `contracts.ManifestSuccess.Pass` |
| `agent` | `AgentID` | yes | `agent_id_fmt` | `contracts.ManifestSuccess.Agent` |
| `branch_name` | `string` | yes | non-empty | `contracts.ManifestSuccess.BranchName` |
| `head_sha` | `string` | yes | `sha1_hex` | `contracts.ManifestSuccess.HeadSHA` |
| `base_sha` | `string` | yes | `sha1_hex` | `contracts.ManifestSuccess.BaseSHA` |
| `diff_path` | `string` | yes | clean run-relative path。prefix は `20-pass1/<agent>/` or `50-pass2/<agent>/` | `contracts.ManifestSuccess.DiffPath` |
| `session_path` | `string` | yes | clean run-relative path。prefix は `20-pass1/<agent>/` or `50-pass2/<agent>/` | `contracts.ManifestSuccess.SessionPath` |
| `checklist_path` | `string` | yes | clean run-relative path。prefix は `20-pass1/<agent>/` or `50-pass2/<agent>/` | `contracts.ManifestSuccess.ChecklistPath` |
| `prompt_version` | `string` | yes | non-empty | `contracts.ManifestSuccess.PromptVersion` |
| `started_at` | `time.Time` | yes | RFC3339 timestamp | `contracts.ManifestSuccess.StartedAt` |
| `finished_at` | `time.Time` | yes | RFC3339 timestamp | `contracts.ManifestSuccess.FinishedAt` |

`ManifestError`

non-success manifest は **commit / diff / session / checklist artifact を確定する前** に書かれる。したがって success variant にある commit metadata / artifact path 群は error variant には存在しない。

| field | type | required | constraints / notes | Go xref |
|---|---|---|---|---|
| `kind` | `string` | yes | `error` 固定 | `contracts.ManifestError.Kind` |
| `schema_version` | `string` | yes | `"1"` | `contracts.ManifestError.SchemaVersion` |
| `run_id` | `RunID` | yes | `run_id_fmt` | `contracts.ManifestError.RunID` |
| `pass` | `int` | yes | `1 | 2` | `contracts.ManifestError.Pass` |
| `agent` | `AgentID` | yes | `agent_id_fmt` | `contracts.ManifestError.Agent` |
| `exit_code` | `int` | yes | zero-value `0` も合法 | `contracts.ManifestError.ExitCode` |
| `reason` | `string` | yes | `rate_limit | budget | context | signal | unknown` | `contracts.ManifestError.Reason` |
| `detail` | `string` | no | `max=300` | `contracts.ManifestError.Detail` |
| `started_at` | `time.Time` | yes | RFC3339 timestamp | `contracts.ManifestError.StartedAt` |
| `finished_at` | `time.Time` | yes | RFC3339 timestamp | `contracts.ManifestError.FinishedAt` |

`ManifestTimeout`

timeout も error と同様に **commit / diff / session / checklist artifact 確定前** の terminal marker であり、success variant の artifact path 群は持たない。

| field | type | required | constraints / notes | Go xref |
|---|---|---|---|---|
| `kind` | `string` | yes | `timeout` 固定 | `contracts.ManifestTimeout.Kind` |
| `schema_version` | `string` | yes | `"1"` | `contracts.ManifestTimeout.SchemaVersion` |
| `run_id` | `RunID` | yes | `run_id_fmt` | `contracts.ManifestTimeout.RunID` |
| `pass` | `int` | yes | `1 | 2` | `contracts.ManifestTimeout.Pass` |
| `agent` | `AgentID` | yes | `agent_id_fmt` | `contracts.ManifestTimeout.Agent` |
| `timeout_seconds` | `int` | yes | `> 0` | `contracts.ManifestTimeout.TimeoutSeconds` |
| `started_at` | `time.Time` | yes | RFC3339 timestamp | `contracts.ManifestTimeout.StartedAt` |
| `finished_at` | `time.Time` | yes | RFC3339 timestamp | `contracts.ManifestTimeout.FinishedAt` |

`contracts.ChecklistResult` (`<run>/{20-pass1|50-pass2}/<agent>/checklist-result.json`)

| field | type | required | constraints / notes | Go xref |
|---|---|---|---|---|
| `schema_version` | `string` | yes | `"1"` | `contracts.ChecklistResult.SchemaVersion` |
| `run_id` | `RunID` | yes | `run_id_fmt` | `contracts.ChecklistResult.RunID` |
| `pass` | `int` | yes | `1 | 2` | `contracts.ChecklistResult.Pass` |
| `agent` | `AgentID` | yes | `agent_id_fmt` | `contracts.ChecklistResult.Agent` |
| `items` | `[]ChecklistItem` | yes | `dive` | `contracts.ChecklistResult.Items` |

`contracts.ChecklistItem`

| field | type | required | constraints / notes | Go xref |
|---|---|---|---|---|
| `rule_id` | `string` | yes | non-empty | `contracts.ChecklistItem.RuleID` |
| `verdict` | `string` | yes | `compliant | n_a | exception` | `contracts.ChecklistItem.Verdict` |
| `rationale` | `string` | conditional | `max=500`; `verdict='exception'` のときは trim 後 non-empty 必須 | `contracts.ChecklistItem.Rationale` |
| `exception_reason` | `string` | no | `max=300`; `verdict=exception` の補足 | `contracts.ChecklistItem.ExceptionReason` |

`contracts.Candidates` (`<run>/40/candidates.json`)

| field | type | required | constraints / notes | Go xref |
|---|---|---|---|---|
| `schema_version` | `string` | yes | `"1"` | `contracts.Candidates.SchemaVersion` |
| `run_id` | `RunID` | yes | `run_id_fmt` | `contracts.Candidates.RunID` |
| `candidates` | `[]Candidate` | no | 0 件可、各要素は下表 | `contracts.Candidates.Candidates` |
| `candidates_hash` | `string` | yes | `sha256_hex`; `CanonicalCandidatesHash(candidates)` と一致 | `contracts.Candidates.CandidatesHash` |
| `created_at` | `time.Time` | yes | RFC3339 timestamp | `contracts.Candidates.CreatedAt` |

Persisted tagged union は **wrapper type を正本とする**。`ManifestSuccess` / `DecisionAdopt` / `RuleRegistryAdded` / `StateEntryStarted` のような concrete variant は decode target / in-memory helper であり、永続化時は必ず `Manifest` / `Decision` / `RuleRegistryEntry` / `StateEntry` 経由で書く。

`contracts.Candidate`

| field | type | required | constraints / notes | Go xref |
|---|---|---|---|---|
| `candidate_id` | `string` | yes | non-empty | `contracts.Candidate.CandidateID` |
| `kind` | `string` | yes | closed set `new | update | duplicate` | `contracts.Candidate.Kind` |
| `target_rule_id` | `string` | conditional | `update | duplicate` では required、`new` では forbidden | `contracts.Candidate.TargetRuleID` |
| `title` | `string` | yes | `max=200` | `contracts.Candidate.Title` |
| `problem` | `string` | no | `max=500` | `contracts.Candidate.Problem` |
| `problem_overflow_ref` | `OverflowRef` | no | `problem` overflow 時 | `contracts.Candidate.ProblemOverflowRef` |
| `rationale` | `string` | no | `max=500` | `contracts.Candidate.Rationale` |
| `rationale_overflow_ref` | `OverflowRef` | no | `rationale` overflow 時 | `contracts.Candidate.RationaleOverflowRef` |
| `proposed_body_path` | `string` | yes | clean run-relative path (absolute / `.` / `..` / empty segment 禁止) | `contracts.Candidate.ProposedBodyPath` |
| `proposed_body_sha256` | `string` | yes | `sha256_hex` | `contracts.Candidate.ProposedBodySha256` |

`contracts.OverflowRef`

| field | type | required | constraints / notes | Go xref |
|---|---|---|---|---|
| `path` | `string` | yes | non-empty path | `contracts.OverflowRef.Path` |
| `sha256` | `string` | yes | `sha256_hex` | `contracts.OverflowRef.Sha256` |

`contracts.ScoreEntry` (`30/scores-A.jsonl`, `60/scores-B.jsonl` final row)

| field | type | required | constraints / notes | Go xref |
|---|---|---|---|---|
| `schema_version` | `string` | yes | `"1"` | `contracts.ScoreEntry.SchemaVersion` |
| `run_id` | `RunID` | yes | `run_id_fmt` | `contracts.ScoreEntry.RunID` |
| `pass` | `int` | yes | `1 | 2` | `contracts.ScoreEntry.Pass` |
| `agent` | `AgentID` | yes | `agent_id_fmt` | `contracts.ScoreEntry.Agent` |
| `dimension` | `string` | yes | `fidelity | correctness | maintainability | discipline | communication` | `contracts.ScoreEntry.Dimension` |
| `score` | `int` | no | `0..100`; integer only | `contracts.ScoreEntry.Score` |
| `reasons` | `string` | no | `max=1000` | `contracts.ScoreEntry.Reasons` |
| `reasons_overflow_ref` | `OverflowRef` | no | overflow 時 | `contracts.ScoreEntry.ReasonsOverflowRef` |
| `verdict_path` | `string` | yes | `single | agreement | arbitrated | arbiter_overruled` | `contracts.ScoreEntry.VerdictPath` |
| `rubric_version` | `string` | yes | non-empty | `contracts.ScoreEntry.RubricVersion` |
| `prompt_version` | `string` | yes | non-empty | `contracts.ScoreEntry.PromptVersion` |
| `resolved_at` | `time.Time` | yes | RFC3339 timestamp | `contracts.ScoreEntry.ResolvedAt` |

`contracts.ComplianceEntry` (`30/compliance-A.jsonl`, `60/compliance-B.jsonl` final row)

| field | type | required | constraints / notes | Go xref |
|---|---|---|---|---|
| `schema_version` | `string` | yes | `"1"` | `contracts.ComplianceEntry.SchemaVersion` |
| `run_id` | `RunID` | yes | `run_id_fmt` | `contracts.ComplianceEntry.RunID` |
| `pass` | `int` | yes | `1 | 2` | `contracts.ComplianceEntry.Pass` |
| `agent` | `AgentID` | yes | `agent_id_fmt` | `contracts.ComplianceEntry.Agent` |
| `rule_id` | `string` | yes | non-empty | `contracts.ComplianceEntry.RuleID` |
| `verdict` | `string` | yes | `compliant | violated | valid_exception | invalid_exception | missed | n_a` | `contracts.ComplianceEntry.Verdict` |
| `rationale` | `string` | no | `max=500` | `contracts.ComplianceEntry.Rationale` |
| `rationale_overflow_ref` | `OverflowRef` | no | overflow 時 | `contracts.ComplianceEntry.RationaleOverflowRef` |
| `verdict_path` | `string` | yes | `single | agreement | arbitrated | arbiter_overruled` | `contracts.ComplianceEntry.VerdictPath` |
| `rubric_version` | `string` | yes | non-empty | `contracts.ComplianceEntry.RubricVersion` |
| `prompt_version` | `string` | yes | non-empty | `contracts.ComplianceEntry.PromptVersion` |
| `resolved_at` | `time.Time` | yes | RFC3339 timestamp | `contracts.ComplianceEntry.ResolvedAt` |

`contracts.PairwiseEntry` (`60/pairwise.jsonl`)

| field | type | required | constraints / notes | Go xref |
|---|---|---|---|---|
| `schema_version` | `string` | yes | `"1"` | `contracts.PairwiseEntry.SchemaVersion` |
| `run_id` | `RunID` | yes | `run_id_fmt` | `contracts.PairwiseEntry.RunID` |
| `agent_a` | `AgentID` | yes | `agent_id_fmt`; pass1 側 agent | `contracts.PairwiseEntry.AgentA` |
| `agent_b` | `AgentID` | yes | `agent_id_fmt`; **same-agent only** のため `agent_a == agent_b` | `contracts.PairwiseEntry.AgentB` |
| `winner` | `string` | yes | `A | B | tie` | `contracts.PairwiseEntry.Winner` |
| `margin` | `string` | yes | `decisive | clear | slight` | `contracts.PairwiseEntry.Margin` |
| `justification` | `string` | no | `max=500` | `contracts.PairwiseEntry.Justification` |
| `justification_overflow_ref` | `OverflowRef` | no | overflow 時 | `contracts.PairwiseEntry.JustificationOverflowRef` |
| `verdict_path` | `string` | yes | `single | agreement | arbitrated | arbiter_overruled` | `contracts.PairwiseEntry.VerdictPath` |
| `rubric_version` | `string` | yes | non-empty | `contracts.PairwiseEntry.RubricVersion` |
| `prompt_version` | `string` | yes | non-empty | `contracts.PairwiseEntry.PromptVersion` |
| `resolved_at` | `time.Time` | yes | RFC3339 timestamp | `contracts.PairwiseEntry.ResolvedAt` |

`contracts.Decision` (`<run>/70/decision.json`)

closed variant set: `adopt | reject | noop | rollback`

`DecisionAdopt`

| field | type | required | constraints / notes | Go xref |
|---|---|---|---|---|
| `action` | `string` | yes | `adopt` 固定 | `contracts.DecisionAdopt.Action` |
| `schema_version` | `string` | yes | `"1"` | `contracts.DecisionAdopt.SchemaVersion` |
| `run_id` | `RunID` | yes | `run_id_fmt` | `contracts.DecisionAdopt.RunID` |
| `idempotency_key` | `string` | yes | `sha256_hex`; `sha256(run_id + target_sha + best_sha_before + candidates_hash)` と一致 | `contracts.DecisionAdopt.IdempotencyKey` |
| `best_sha_before` | `string` | yes | `sha1_hex` | `contracts.DecisionAdopt.BestShaBefore` |
| `target_sha` | `string` | yes | `sha1_hex` | `contracts.DecisionAdopt.TargetSha` |
| `candidates_hash` | `string` | yes | `sha256_hex` | `contracts.DecisionAdopt.CandidatesHash` |
| `registry_append_result` | `RegistryAppendResult` | yes | 下表参照 | `contracts.DecisionAdopt.RegistryAppendResult` |
| `decided_at` | `time.Time` | yes | RFC3339 timestamp | `contracts.DecisionAdopt.DecidedAt` |

`DecisionReject`

| field | type | required | constraints / notes | Go xref |
|---|---|---|---|---|
| `action` | `string` | yes | `reject` 固定 | `contracts.DecisionReject.Action` |
| `schema_version` | `string` | yes | `"1"` | `contracts.DecisionReject.SchemaVersion` |
| `run_id` | `RunID` | yes | `run_id_fmt` | `contracts.DecisionReject.RunID` |
| `reason` | `string` | yes | `max=300` | `contracts.DecisionReject.Reason` |
| `decided_at` | `time.Time` | yes | RFC3339 timestamp | `contracts.DecisionReject.DecidedAt` |

`DecisionNoop`

| field | type | required | constraints / notes | Go xref |
|---|---|---|---|---|
| `action` | `string` | yes | `noop` 固定 | `contracts.DecisionNoop.Action` |
| `schema_version` | `string` | yes | `"1"` | `contracts.DecisionNoop.SchemaVersion` |
| `run_id` | `RunID` | yes | `run_id_fmt` | `contracts.DecisionNoop.RunID` |
| `reason` | `string` | yes | `max=200` | `contracts.DecisionNoop.Reason` |
| `decided_at` | `time.Time` | yes | RFC3339 timestamp | `contracts.DecisionNoop.DecidedAt` |

`DecisionRollback`

| field | type | required | constraints / notes | Go xref |
|---|---|---|---|---|
| `action` | `string` | yes | `rollback` 固定 | `contracts.DecisionRollback.Action` |
| `schema_version` | `string` | yes | `"1"` | `contracts.DecisionRollback.SchemaVersion` |
| `run_id` | `RunID` | yes | `run_id_fmt` | `contracts.DecisionRollback.RunID` |
| `idempotency_key` | `string` | no | `sha256_hex`; rollback 対象 promotion がある場合のみ | `contracts.DecisionRollback.IdempotencyKey` |
| `rollback_reason` | `string` | yes | `lease_failure | remote_divergence | registry_divergence | worktree_rescue_loop | manual_abort_pending_cleanup | transactional_failure` | `contracts.DecisionRollback.RollbackReason` |
| `failed_step` | `string` | yes | `"10" | "20" | "30" | "40" | "50" | "60" | "70"` | `contracts.DecisionRollback.FailedStep` |
| `best_sha_before` | `string` | no | `sha1_hex` | `contracts.DecisionRollback.BestShaBefore` |
| `target_sha` | `string` | no | `sha1_hex` | `contracts.DecisionRollback.TargetSha` |
| `detail` | `string` | no | `max=300` | `contracts.DecisionRollback.Detail` |
| `decided_at` | `time.Time` | yes | RFC3339 timestamp | `contracts.DecisionRollback.DecidedAt` |

`contracts.RegistryAppendResult`

| field | type | required | constraints / notes | Go xref |
|---|---|---|---|---|
| `offset` | `int64` | yes | `>= 0`; JSON field presence を custom unmarshal で物理確認 | `contracts.RegistryAppendResult.Offset` |
| `sha256` | `string` | yes | `sha256_hex` | `contracts.RegistryAppendResult.Sha256` |

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
- `auto-improve recover --run <id> --mark-manual-abort`: decision.json を `action: 'rollback', rollback_reason: 'manual_abort_pending_cleanup'` で書込 + sentinel を **`<runs_base>/needs-recovery/<run_id>.aborted.json` に rename**(block 継続) + processed `needs_manual_recovery { pr, run_id, step: '70', reason: 'manual_abort_pending_cleanup', failed_step: '70', at }` append。branch/registry 修復は operator 手動で、**sentinel は削除しない**(未修復 state で次 promotion が走るのを防止)。
- `auto-improve recover --run <id> --finalize-cleanup --remote-head <sha> --registry-head <sha> [--policy-head <sha>]`: operator が branch/registry/policy を手動で整合させた後のみ使う。**remote / registry SHA は必須**(rev11、Codex rev10 R2 対応)。`repo.policy_branch` 設定時は **policy SHA も必須**。promotion.lock 下で remote HEAD / registry head / policy_branch HEAD を再確認、全て一致で `.aborted.json` 削除、pipeline 復旧。片方でも不一致なら refuse
- `--clear-sentinel`: 通常 sentinel(`<run_id>.json`)削除の最終手段。`.aborted.json` は
  manual cleanup 未検証状態なので拒否し、上記 `--finalize-cleanup` のみで解除する

**safe matrix refuse cell の contract** (rev10 明文化):
- すべての refuse cell で output は `{stage, remote_head, registry_head, idempotency_hit, suggested_action}` JSON + human readable。
- 勝手に state を変更しない。operator の明示的な `--rollback / --adopt-anyway / --mark-manual-abort / --finalize-cleanup / --clear-sentinel` を待つ。

### Recover CLI 6-flag state-transition table (rev11 追加、Codex rev10 R3 対応)

| flag | 前提条件 | sentinel 変化 | intention.json 変化 | decision.json 変化 | processed.jsonl append | 結果 pipeline 状態 |
|---|---|---|---|---|---|---|
| `--inspect` | なし(sentinel 存在 or not) | 不変 | 不変 | 不変 | なし | 不変(read-only dump) |
| `--rollback` | safe matrix allowed cell(§Recover safety matrix) | `.json` 削除 | 削除 | `action: 'rollback'` 書込 | `rollback` append | block 解除、次 PR 進行可 |
| `--adopt-anyway` | safe matrix allowed cell(HEAD==target_sha, idempotency hit) | `.json` 削除 | 削除(stage=decision_written 相当から resume 実行後) | `action: 'adopt'` 書込 | `promoted` append | block 解除、次 PR 進行可 |
| `--mark-manual-abort` | 任意(inspect 後の最終手段) | `.json` → `.aborted.json` rename(**削除せず**) | 保持(stage=needs_manual_recovery、recovery_reason=`manual_abort_pending_cleanup`、failed_step=`70` 上書き) | `action: 'rollback', rollback_reason: 'manual_abort_pending_cleanup'` 書込 | `needs_manual_recovery { pr, run_id, step: '70', reason: 'manual_abort_pending_cleanup', failed_step: '70', at }` append | block 継続(operator が手動修復を行う状態) |
| `--finalize-cleanup --remote-head <sha> --registry-head <sha> [--policy-head <sha>]` | `.aborted.json` 存在 AND operator が渡した SHA が現実と一致(`repo.policy_branch` 設定時は policy SHA も一致) | `.aborted.json` 削除 | 削除 | 不変(既に rollback) | `completed { pr, run_id, step: '70', detail: 'manual_cleanup_finalized', at }` append | block 解除、次 PR 進行可 |
| `--clear-sentinel` | 最終手段、operator 全責任。`.aborted.json` は拒否し `--finalize-cleanup` を要求 | `.json` 削除 | 不変 | 不変 | **条件付き append**: `processed.jsonl` の run_id 末尾 event が terminal (`needs_manual_recovery` 等) なら append しない(二重 terminal 禁止)。末尾が non-terminal なら `completed { pr, run_id, step, detail: 'sentinel_manually_cleared', at }` を append。いずれも resume は blocked(sentinel 削除で detect 再 queue もされない) | block 解除、整合性は operator 保証 |

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
