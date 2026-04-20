# Go実装計画 (rev34 / Codex 5体 + Claude 1体 × 33ラウンドレビュー精査反映)

前提:
- 言語: Go 1.22+ (tool版は `go.mod` で pinned)
- スタック: cobra / go-playground/validator/v10 / slog / yaml.v3 / testify
- 対応 OS: **darwin/arm64, darwin/amd64, linux/amd64 のみ**(atomic rename / flock 前提)
- 既存 TS 実装は **全削除** してゼロから書き直す(M1含む)
- 本計画書の契約は `docs/design/io-contracts.md` と同期済み(rev11 で改訂完了)
- **Codex rev1〜rev11 指摘(延べ 95+ 件)精査・採用判断済み**
- **rate limit / budget / context / signal 等途中停止からの resume 機構を明示(rev6 新規、rev7 以降精密化)**
- **`docs/design/全体設計.md` は rev6 以降未同期**。Phase 4 で rev11 に合わせて rewrite 予定。それまで本ドキュメントと io-contracts.md を正本とする(全体設計.md 冒頭に注意書き追加)

---

## ディレクトリ構造 (Go)

```
cmd/auto-improve/
  main.go                       # Phase 0-A 独占
  preflight.go                  # Phase 0-D
  detect.go                     # Phase 0-D
  run.go                        # Phase 0-E (骨格 + --with-preflight/--from-scratch/--pr flag) → Phase 2 (本体)
  sunset.go                     # Phase 1-F (wiring only、business logic は internal/archive)
  recover.go                    # Phase 1-F (rev6 追加、needs_manual_recovery 解除)
internal/
  contracts/                    # Phase 0-bootstrap
    *.go                        # run_id / agent_id / task_package / manifest / checklist / score / compliance / candidate / classification / pairwise / decision / rules_registry / state
    stepio/                     # step I/O contract 凍結 (Phase 0-bootstrap)
      step{10,20,30,40,50,60,70}_io.go + errors.go
  io/                           # Phase 0-bootstrap
    atomic.go / json.go / jsonl.go / run_layout.go / safe_text.go
  validation/                   # Phase 0-bootstrap (rev5 新規)
    validator.go                # sync.Once singleton + custom registrations
  config/                       # Phase 0-B
  state/                        # Phase 0-C
  logger/                       # Phase 0-B
  preflight/                    # Phase 0-D
  detect/                       # Phase 0-D
  orchestrator/                 # Phase 0-E (骨格) + Phase 2 (cycle)
    queue.go / budget.go / intention.go / sunset_tick.go / cycle.go
  steps/
    step10_restore_base/        # Phase 1-A
    step20_implement/           # Phase 1-B (pass1)
    step50_implement/           # Phase 1-C (pass2, step20 からコピペ)
    step30_score/               # Phase 1-D (pass1)
    step60_score/               # Phase 1-E (pass2, step30 からコピペ)
    step40_extract_rules/       # Phase 1-F
    step70_decide/              # Phase 1-F
    agentrunner/                # Phase 1.5 で抽出 (step20/50 共通)
    scorecore/                  # Phase 1.5 で抽出 (step30/60 共通)
  judges/                       # Phase 0-F
  archive/                      # Phase 1-F (business logic 集約、sunset_tick と sunset cmd 両方から call)
  prompt/                       # Phase 0-F
rubrics/default.md              # Phase 0-F
prompts/*.tmpl                  # Phase 0-F
scripts/
  install-launchd.sh            # Phase 3-B
  uninstall-launchd.sh          # Phase 3-B
  install.sh                    # Phase 3-C (staged transaction: temp → preflight → launchd → swap)
  check-contracts-sync.sh       # Phase 0-bootstrap (一方向: contracts 変更 → io-contracts.md 要求)
.github/workflows/
  auto-improve.yml              # Phase 3-A (workflow_dispatch only)
  release.yml                   # Phase 3-C
  ci.yml                        # Phase 0-bootstrap (build/test/lint + sync check)
.githooks/pre-commit            # Phase 0-bootstrap
.goreleaser.yml                 # Phase 3-C
Makefile                         # Phase 0-bootstrap scaffold / Phase 3-C append
docs/
  design/
    io-contracts.md             # canonical (rev11 同期済み)
    全体設計.md                  # Phase 4 で Go 版更新
    Go実装計画.md                # this file (Phase 4 で archive)
testdata/
  golden_run/                   # Phase 2 owner (conformance fixture)
```

---

## 所有権マトリクス

| Owner | 書き換えて良い場所 |
|---|---|
| **phase0-bootstrap (A)** | `internal/contracts/**`, `internal/contracts/stepio/**`, `internal/io/**`, `internal/validation/**`, `cmd/auto-improve/main.go`, `go.mod/.sum`, `Makefile` (scaffold), `scripts/check-contracts-sync.sh`, `.githooks/pre-commit`, `.github/workflows/ci.yml`, `docs/design/io-contracts.md` (rev11 準拠への Go 固有書き換え) |
| phase0-B | `internal/config/**`, `internal/logger/**`, `config.yaml.example` |
| phase0-C | `internal/state/**` |
| phase0-D | `internal/preflight/**`, `internal/detect/**`, `cmd/auto-improve/preflight.go`, `cmd/auto-improve/detect.go` |
| phase0-E | `internal/orchestrator/queue.go / budget.go / intention.go / sunset_tick.go`, `cmd/auto-improve/run.go` (**`--with-preflight` flag 実装含む**) |
| phase0-F | `internal/judges/**`, `internal/prompt/**`, `internal/interruption/**`, `testdata/interruption/**`, `rubrics/**`, `prompts/**` |
| step10 | `internal/steps/step10_restore_base/**` |
| step20 | `internal/steps/step20_implement/**` |
| step50 | `internal/steps/step50_implement/**` |
| step30 | `internal/steps/step30_score/**` |
| step60 | `internal/steps/step60_score/**` |
| step40 | `internal/steps/step40_extract_rules/**` |
| **step70+archive+recover (Phase 1-F)** | `internal/steps/step70_decide/**`, `internal/archive/**`, `cmd/auto-improve/sunset.go` (wiring のみ), `cmd/auto-improve/recover.go`, `internal/recover/**` (sentinel 管理) |
| phase1.5 | `internal/steps/agentrunner/**`, `internal/steps/scorecore/**` (既存 step を書き換えて helper 呼び出しに) |
| phase2 | `internal/orchestrator/cycle.go`, `internal/orchestrator/*_test.go`, `testdata/golden_run/**` |
| infra | `.github/workflows/auto-improve.yml`, `release.yml`, `scripts/install*.sh`, `.goreleaser.yml`, `README.md`, `Makefile` (append のみ、既存行変更禁止) |

### Phase 分割ポリシー(Codex R2 指摘反映)

- Phase 0 は **bootstrap(A 単独)** → **parallel(B-F 5並列)** の2段。理由: 0-A が go.mod / cobra root / stepio を凍結しないと他 0 agent は compile 不可。
- stepio 凍結後の bug hotfix protocol: Phase 1/2 agent を全停止 → 0-A resume で stepio + io-contracts.md を同一 commit 修正 → Phase 1/2 rebase → 再開。
- Phase 1 中は **DRY 禁止**(step20→step50, step30→step60 はコピペ)。Phase 1.5 で equivalence check 付き抽出。

### sync hook (一方向化、Codex R3 #2 対応)

`scripts/check-contracts-sync.sh`:
- `git diff --cached --name-only` で `internal/contracts/` または `internal/contracts/stepio/` 下の `.go` 変更を検出したら、`docs/design/io-contracts.md` の同時変更を **要求**(reject if missing)
- **逆方向は要求しない**(docs-only typo 修正は通す)
- 適用先: `.githooks/pre-commit` + `.github/workflows/ci.yml`

---

## Phase -1: TS実装撤去 (1 agent, ~10min)

**削除**: `src/**`, `package.json`, `pnpm-*.yaml`, `tsconfig*.json`, `vitest.config.ts`, `.npmrc`, `node_modules/`, `.eslintrc*`, `.prettierrc*`

**保持**: `docs/**`, `.git/`, `.gitignore`, `README.md`, `runs/`

**追加**: `go.mod` (空), `.gitignore` に Go用 (`*.exe`, `/dist/`, `/bin/`, `*.test`, `coverage.out`)

---

## Phase 0-bootstrap: 契約 + 統合 (1 agent, ~120min / 3段チェックポイント, rev10 再見積もり)

**rev10 再見積もり** (Codex rev9 R3 対応): 90min → 120min に拡大。review gate 時間も別枠化。

### 0-bootstrap-1 (契約・docs 凍結ゲート, ~50min)
1. `go.mod` / `go.sum` 依存確定 (cobra / validator / yaml.v3 / testify)
2. `internal/contracts/*.go` 全 schema 実装(validator tag + tagged union の custom UnmarshalJSON)
3. `internal/contracts/stepio/*.go` step API 凍結 (`step{10,20,30,40,50,60,70}_io.go` + `errors.go`)
4. `docs/design/io-contracts.md` が rev11 準拠(既に本 commit 前に更新済み)。Go 表現最終確認
5. **ゲート check**: ここで一度 Codex レビュー(schema 最終ゲート)

### 0-bootstrap-2 (統合基盤, ~50min)
6. `internal/io/*.go` 全 helper
7. `cmd/auto-improve/main.go` cobra root + 全サブコマンド stub (`preflight / detect-merged / run / sunset / recover`)
8. `Makefile` scaffold (`build / test / lint / tidy`)
9. `scripts/check-contracts-sync.sh` + `.githooks/pre-commit`
10. `.github/workflows/ci.yml` (`go build/test/lint` + sync check)
11. 失敗系テスト全追加

### 0-bootstrap-gate (Codex 3体 review, ~20min 別枠)

rev10 追記: bootstrap-2 完了後、Codex 3体 adversarial review を別枠で走らせる(同時並列、完了待ち)。critical/high 発見時は 0-bootstrap-1 or 0-bootstrap-2 に戻って修正。

### Validator 初期化 (rev5 新規、Codex R2 #3 対応)

`internal/validation/validator.go`:
```go
package validation

import (
    "sync"
    "github.com/go-playground/validator/v10"
)

var (
    once     sync.Once
    instance *validator.Validate
)

func Instance() *validator.Validate {
    once.Do(func() {
        instance = validator.New(validator.WithRequiredStructEnabled())
        // カスタム validator / tag name func 登録を全てここに集約
        // 例: instance.RegisterValidation("run_id_fmt", validateRunID)
    })
    return instance
}
```

全 reader / UnmarshalJSON は `validation.Instance().Struct(v)` 経由でのみ呼ぶ。ad hoc package globals を作らない。

### Strict JSON 実装ガイド (Codex R2 #1 修正)

```go
func ReadJSON[T any](r io.Reader) (T, error) {
    var v T
    dec := json.NewDecoder(r)
    dec.DisallowUnknownFields()
    if err := dec.Decode(&v); err != nil { return v, err }
    // EOF check: 2回目 Decode で io.EOF 要求 (More() は top-level EOF 判定不可)
    var rest any
    if err := dec.Decode(&rest); err != io.EOF { return v, ErrTrailingJSON }
    if err := validation.Instance().Struct(v); err != nil { return v, err }
    return v, nil
}
```

Tagged union (Manifest, RuleRegistryEntry) の `UnmarshalJSON`:
```go
func (m *Manifest) UnmarshalJSON(data []byte) error {
    // Go の json.Unmarshal は data を mutate しない(stdlib 契約)ので copy 不要
    var env struct{ Kind string `json:"kind"` }
    if err := json.Unmarshal(data, &env); err != nil { return err }
    dec := json.NewDecoder(bytes.NewReader(data))
    dec.DisallowUnknownFields()
    switch env.Kind {
    case "success": var v ManifestSuccess; if err := dec.Decode(&v); err != nil { return err }; m.Value = v
    case "error":   var v ManifestError;   if err := dec.Decode(&v); err != nil { return err }; m.Value = v
    case "timeout": var v ManifestTimeout; if err := dec.Decode(&v); err != nil { return err }; m.Value = v
    default: return ErrUnknownManifestKind
    }
    var rest any
    if err := dec.Decode(&rest); err != io.EOF { return ErrTrailingJSON }
    return validation.Instance().Struct(m.Value)
}
```

失敗系テスト必須(各 union について 6 ケース): unknown top-level key / unknown variant-level key / missing required / wrong kind / trailing JSON / trailing non-JSON bytes

### Decision variant 確定

`action: adopt | reject | noop | rollback` のみ。`error` は廃止し `rollback` に統合(rollback variant が `rollback_reason`, `failed_step` 保持)。

### Manifest loader 2種

- `LoadFinalizedManifest(ctx, pass, agent) (*Manifest, error)` — 全種 (success/error/timeout)
- `LoadScorableManifest(ctx, pass, agent) (*ManifestSuccess, error)` — success のみ、他は `ErrNotScorable`

step30/60 は必ず後者を使う。

### 4KB JSONL overflow 棚卸し

- `scores-*.jsonl`: reasons 1000字/次元 + 超過時 `30/reasons/<sha256>.txt` sidecar + `reasons_overflow_ref`
- `compliance-*.jsonl`: rationale 500字 cap
- `classification.jsonl`: problem/rationale 各 500字 cap
- `processed.jsonl`: reason 300字 cap + 超過時 sidecar
- `pairwise.jsonl`: justification 500字 cap
- `rules-registry.jsonl`: 本体は `<runs_base>/rules/<rule_id>.md` sidecar、registry entry は **tagged union**:
  - **promotion entries** (step70 から): `{kind: 'added' | 'updated', rule_id, rule_path, sha256, idempotency_key, version_seq, prev_hash, by_run_id, at}`
  - **rollback entries** (step70 から、rev18): `{kind: 'rolled_back', target_op_id, target_offset, target_sha256, by_run_id, rollback_reason, failed_step, version_seq, prev_hash, at}` — **`target_op_id` は rollback 対象 promotion entry の `idempotency_key` と同値**(rev20 明示、Codex rev19 #2 H1 対応)。reader は同 target_op_id (= 旧 adoption の idempotency_key) を持つ promotion entry の adoption を無効扱い
  - **sunset entries** (archive/cycle③ から): `{kind: 'status_changed' | 'archived' | 'restored', rule_id, prev_status, new_status, op_id (=sha256(sunset_run_id||rule_id||transition)), version_seq, prev_hash, by_sunset_run_id, at}`(rev16 明示、Codex rev16 R3 対応)
  - 全 kind とも 4KB cap、長文は sidecar 参照

### Intention の atomic overwrite と tmp cleanup (rev5 新規)

- 各 stage 遷移は `intention.json.tmp-<pid>-<ms>-<rand>` に write → `rename()` で overwrite
- 前 stage の tmp ファイルが残存していた場合は `WriteAtomic` 側で cleanup(atomic.go の共通実装)
- **concurrent reader への対策**: step70 は `<runs_base>/promotion.lock` (global) flock で全 flow を排他。recovery は lock 取得後に state を re-read することで stale fd を回避

### Staged intention schema (Phase 0-E 実装用、ここでは schema のみ)

```go
type IntentionRecord struct {
    Stage                string    // planning | branch_pushed | registry_appended | decision_written | rolling_back_branch_reverted | rolling_back_registry_appended | rolling_back_decision_written | needs_manual_recovery
    IdempotencyKey       string    // sha256(run_id || target_sha || best_sha_before || candidates_hash)
    RunID                string
    BestShaBefore        string
    TargetSha            string
    CandidatesHash       string
    RegistryHeadBefore   string    // registry last-entry sha256 at planning time
    StartedAt            time.Time
    RegistryAppendResult *RegistryAppendResult // {Offset int64, Sha256 string}, step4 で記録
    RecoveryReason       string    // needs_manual_recovery 時のみ
    FailedStep           string    // needs_manual_recovery 時のみ
}
```

**idempotency_key の生成・永続化ルール (Codex R1 #3 対応)**:
- planning stage で1回だけ生成
- 以降の retry / recovery では intention.json から読み出して reuse
- collision domain: (run_id, target_sha) で既に十分一意。複数要素を含むのは target_sha 変化(push 間)や candidates 変化に対する明示的識別のため

**完了条件**:
- `go build ./...` pass
- `go test ./...` pass(union 失敗系 6 ケース × 2 union = 12 + 通常 schema test)
- `auto-improve --help` 全サブコマンド列挙
- pre-commit hook 動作確認

### Bootstrap-1 レビューゲート基準 (rev5 新規、Codex R2 #5 対応)

Phase 0-bootstrap-1 終了後、Codex adversarial review 3体でレビュー:

| verdict 組合せ | 判定 |
|---|---|
| 全体で critical 0件 AND high 0件 | **pass** → bootstrap-2 進行 |
| critical 1件以上 | **block** → 0-bootstrap-1 に戻って修正 → 再レビュー |
| high 1件以上 | **block** → 0-bootstrap-1 に戻って修正 → 再レビュー |
| medium 以下のみ | **conditional pass** → bootstrap-2 進行しつつ medium は bootstrap-2 内で fix |

対象: `internal/contracts/**`, `internal/contracts/stepio/**`, `docs/design/io-contracts.md`
レビュー観点: schema 漏れ、union 不変条件、io-contracts.md との一致、strict JSON の網羅性

---

## Phase 0-parallel: 基盤並列 (5 agent, ~1.5h)

0-bootstrap 完了後起動。

### 0-B: config + logger
yaml KnownFields(true) + validator / slog JSON handler / config.yaml.example

### 0-C: state (rev9: vocabulary 完全同期)
- Append/Read/Summarize/IsTerminal/UnprocessedPRs/MaxRecordedPR
- **Terminal events**: `completed / failed / promoted / rollback / skipped / timeout / needs_manual_recovery`
- **Non-terminal events (resume 対象)**: `started / step_done / interrupted / promoting / warning`
- Schema(全 event に `step` required):
  - `started { pr, run_id, step: 10 }`
  - `step_done { pr, run_id, step }`
  - `interrupted { pr, run_id, step, reason: 'rate_limit' | 'budget' | 'context' | 'signal' | 'unknown' | 'pre_push_crash', detail?, detail_overflow_ref? }`
  - `promoting { pr, run_id, step: 70 }` (rev9 non-terminal)
  - `warning { pr?, run_id?, step, kind, count?, detail?, detail_overflow_ref? }` (rev22 `pr/run_id` optional 化、rev26 全箇所同期、Codex rev25 指摘の warning drift 対応)
  - `needs_manual_recovery { pr, run_id, step, reason, failed_step, detail?, detail_overflow_ref? }`
  - `promoted / rollback / failed / timeout / completed / skipped` は既存踏襲 + `step` required
- `detail` 300字 cap、超過時 sidecar `<run>/processed-details/<sha256>.txt`、`detail_overflow_ref` に path + sha256
- `ResumeTarget(entries) []ResumeRequest`: non-terminal PR の run_id と最後の step を列挙(`promoting/warning` も含む、Codex rev8 R1/R3 対応)
- **processed-index.jsonl は Phase 0 では実装しない**(rev10 決定、Codex rev9 R1/R2 対応): 個人運用 1 PR/日 × 10年 = 3650 entry 程度なら起動時 full scan で十分。index 実装は future work として記載、ただし validation/rebuild 契約が未詰めなため rev10 ではスコープ外
- **unit test**: vocabulary 全種 / overflow / resume target 算出 / IsTerminal 判定(needs_manual_recovery=true)
- **atomic migration**: Phase 0-bootstrap で一括実装

### 0-D: preflight + detect
- git/gh/jq/yq/claude/codex + `gh auth status` (切れ → exit 10) + config.yaml + best_branch 到達性
- preflight 失敗時 state 未更新
- detect: gh pr list merged + state diff + since + max_lookback

### 0-E: orchestrator 骨格

- `queue.go`: FIFO + **二層 lock** (rev33、Claude rev32 対応: 短命 state.lock + 長命 promotion.lock 分離):
  - `<runs_base>/state.lock` flock: processed.jsonl append ごとに acquire→write→release(保持 ms 級)
  - `<runs_base>/promotion.lock` flock: step70 / recover mutation / sunset_tick が保持(promotion 中のみ、数秒〜数十秒)
  - orchestrator main loop: claude/codex 呼出中は lock を持たず、state append 時のみ state.lock 短時間取得
  - step70 内部: promotion.lock 保持中に state.lock 取得して append(lock order: state ⊂ promotion、deadlock 防止)
  - 結果: 数時間オーダーの claude 呼出が lock 外なので sunset_tick / recover --inspect が block されない
  - **sentinel pre-flight gate** (rev20 明示、rev21 TOCTOU 強化、Codex rev19 #3 H1/H2 + Claude rev20 H4 対応): 起動時の最初のアクション(state mutation / worktree allocation / claude 呼出の前)で `<runs_base>/needs-recovery/` を scan。`*.json` または `*.aborted.json` が 1件でもあれば、**全 CLI / 全 step / sunset** を即 exit 0 (`auto-improve recover` のみ例外)。`run --detect-loop` は次 tick で再試行、launchd は単純 interval retry
  - **TOCTOU 対策**(rev21、rev22 step70 runtime 追加): 単発 scan では race が残る。以下 3 点で re-scan 義務化:
    1. queue.go の promotion.lock 取得**直後**に sentinel 再 scan
    2. 各 step (10/20/30/40/50/60/70) の launch 直前に sentinel 再 scan
    3. **step70 の long-running path 内**(Claude rev21 H1 対応): stage 遷移ごと(planning→branch_pushed、branch_pushed→registry_appended、registry_appended→decision_written、decision_written→finalized)の **直前**に sentinel 再 scan。自 run のものは除外(自 run が scan 中に自 sentinel 書く race は無い)、**他 run の sentinel が検出されたら step70 は即 abort**(branch push 前なら intention 削除で side-effect ゼロ rollback、push 済みなら rollback path へ、具体的分岐は io-contracts §step70 参照)
  - いずれかで sentinel 発見 → 現 work を abort(side-effect なしで exit 0、次 tick 再評価)
  - **resume precedence** (rev8, Codex rev7 R2 対応): 上記 sentinel gate を通過した後、`processed.jsonl` から non-terminal PR (`started / step_done / interrupted / promoting / warning`) を列挙 → 既存 run_id で先行 resume → 全 resolve 完了後に `detect` で新規 PR を enqueue。fresh detect が resume 待ち run を追い抜かないことを保証
- `budget.go`: file counter + TTL + daily reset
- `intention.go`: staged transaction primitives (Write/ReadIntention, atomic overwrite per stage, recovery decoder)
- `sunset_tick.go`: **lock → stale marker reconcile → 24h gate → run → finalize** の順 (rev5 修正、Codex R1 #3, R3 #4 対応)。**lock 取得・解放・stale marker reconcile・archive 呼出のロジックは `internal/archive/sunset.go` 側に集約**(`auto-improve sunset` CLI と `sunset_tick.go` 両方が同 lock 経路 `archive.RunSunsetWithLock(opts)` を call する。Phase 1-F の archive owner が responsible)、sunset_tick.go と cmd/sunset.go は wiring のみ。これにより locking ownership 一本化(rev16、Codex rev16 R2 対応):
  ```
  1. **flock <runs_base>/promotion.lock 取得** (rev18: step70/recover と同一 lock。registry を 単一 writer 化、`.sunset-lock` 廃止。Codex rev17 R1/R2/R3 対応)。**sunset_tick auto は lock 取得に 30秒 timeout**、取得失敗なら **lock 取得失敗そのものを stderr + slog で log のみ**(processed.jsonl には append しない、rev28、Codex rev26 遅延指摘対応: lock 外で state log 書くと single-writer 契約違反)。exit 0 で次 tick 再試行。`sunset --force` 手動は無制限 wait
  2. stale marker reconcile: <runs_base>/sunset-running.marker が存在する場合:
     - 前回 crash。marker の run_id_fingerprint で registry を tail scan
     - 該当 sunset_run_id の archive entry が全て appended 済み → 完了扱い
       → last-sunset-at を marker の started_at に atomic update → sunset-running.marker 削除
     - 未完 → in-process で in-progress だった archive を idempotency 経由で resume 完了
       → last-sunset-at update → marker 削除
  3. 24h gate check: last-sunset-at 読み、24h 未経過なら lock release して exit (auto tick のみ、manual は --force で bypass 可)
  4. 実行: marker write (atomic, started_at + run_id_fingerprint) → internal/archive.RunSunset() → last-sunset-at atomic update → marker delete
  5. lock release
  ```
  archive 各操作に `sunset_run_id = sha256(date || run_fingerprint)` を付与、registry tail で重複検出 skip
- `cmd/auto-improve/run.go`: `run --pr <n>` / `run --detect-loop` / **`run --with-preflight`** flag (preflight → 成功のみ state mutation) / **`run --from-scratch`** (既存 run を捨てて新 run_id、**旧 run の 6 worktree を `git worktree remove` で prune してから新 run_id 発行**、Codex rev6 R2 #5 対応)
- **Resume ロジック**(rev6、rev11 で vocabulary 拡張、io-contracts.md §6.1 準拠):
  - 起動時に `processed.jsonl` を読み、各 PR の最後 event を取得
  - terminal (`completed/failed/promoted/rollback/skipped/timeout/needs_manual_recovery`) → skip
  - non-terminal (`started/step_done/interrupted/promoting/registry_size_high/registry_size_critical/rescue_retry`) → **同じ run_id で cycle resume**
  - **warning 系 event の特殊扱い** (rev28、Codex rev27 #2 対応): PR-scoped (`pr` + `run_id` 両方 present) の場合のみ resume target。**pr 不在の global telemetry event**(例: `source: 'sunset_tick'` の `registry_size_critical`)は **resume queue に乗らない、telemetry 専用**。起動時 scan は `pr` field 存在でフィルタ
  - `<runs_base>/needs-recovery/*.json` と `<runs_base>/needs-recovery/*.aborted.json` 両方 sentinel scan: 1件でもあれば 全新規 step70 を block
- **Signal handling**: SIGTERM/SIGINT → 現 step graceful stop → state.lock 取得 → `processed interrupted { step, reason: 'signal' }` append → state.lock release → **保持中の lock を全て defer unlock**(step70 実行中なら promotion.lock も保持、外なら state.lock のみ短期間保持) → exit 0
- **Rate limit 検出**: `internal/interruption.Classify()` 経由で判定 → `interrupted { step, reason: 'rate_limit' }` append → defer unlock → exit 0
- **Budget depletion**: budget lease 取得失敗で `interrupted { step, reason: 'budget' }` append → defer unlock → exit 0
- **Panic catch** (rev7 強化, Codex rev6 R2 #3、rev33 lock 2 層対応): `defer` の 1段目で `recover()` + **保持中の全 lock を defer unlock**(state.lock / promotion.lock 両方対象、step70 外なら state.lock のみ)を必ず実行、`interrupted { step, reason: 'unknown', detail: stack }` を best-effort append、その後 `os.Exit(1)`(main loop 継続禁止、panic 後は process 再起動に委ねる)
- lock は全て `acquire → defer release → work` の pattern で実装、lock 忘れ禁止

### 0-F: judges 骨格 + prompt + interruption classifier (rev8 拡張)
- PanelReview interface / claude/codex CLI wrapper stub / rubric loader / prompt templates
- **`internal/interruption/classify.go`** (rev8 追加、Codex rev7 対応):
  - `Classify(exitCode int, stdout, stderr []byte) InterruptionKind` (`None | RateLimit | Budget | Context | Signal | Unknown`)
  - provider 別 pattern: claude (exit 429 / stderr "rate_limit" / "overloaded") / codex (exit 429 / "quota")
  - fixture: `testdata/interruption/{claude-rate-limit,codex-quota,...}.txt`
  - **update 責務**: 新 provider 追加や pattern 変更時は fixture 追加 + test 必須、Phase 0-F owner が維持
  - **unknown pattern は default alert + `Unknown` 返却**(silently resumable 扱いは禁止、Codex rev7 R2 対応)

---

## Phase 1: step 実装 (6 agent 並列, ~3h)

### 1-A: step10 (restore-base)
gh pr view / worktree 6個 / reconstructed_task_prompt / task-package.json / base.sha / `started` append

### 1-B: step20 (pass1 implement)
goroutine 3体 / claude CLI / atomic manifest / session.jsonl / diff.patch / checklist-result.json

**agent worktree resume state (rev9, heartbeat-based lease + orchestrator-owned terminal, Codex rev8 R1/R2 対応)**:
- 各 agent の `<run>/20-pass1/<agent>/.resume-state.json` に `{expected_base_sha, started_at, pid, retry_count, last_heartbeat}` を atomic write
- **`expected_base_sha` の source**: `task-package.json.worktrees[agent].base_sha`(step10 で記録、以降 immutable)。live HEAD 再計算禁止
- **heartbeat-based lease (Codex rev8 R2 #4 対応)**:
  - 実行中の agent wrapper は別 goroutine で **60秒ごと** に `.heartbeat` ファイルを更新: **content は空でよい**、**`os.Stat().ModTime()` (mtime) を time source として使用**。write は `WriteAtomic`(tmp → rename)経由のみ許可、`os.WriteFile` 直書きは禁止(部分書込中の mtime 更新を避けるため、rev23 Claude H3 対応)
  - stale 判定は **age = now - ModTime() > 300秒**(5 × heartbeat_interval、LLM 応答 3 分超過考慮) **AND pid 不生存**(両方満たす時のみ rescue)
  - filesystem 前提(rev24 明記、Claude rev24 H2 対応): darwin local (APFS/HFS+) と linux local (ext4/xfs) のみサポート。**NFS / SMB は rename 後の mtime 継承仕様差異により未サポート**(`WriteAtomic` の rename 後 `Stat().ModTime()` がローカル FS では新 inode の書込時刻を反映、NFS では source tmp の mtime が継承されうる)
  - started_at だけの age-based 判定は禁止(cold restart で誤判定)
- **bounded retry + orchestrator-owned terminal (Codex rev8 R1 対応)**:
  - rescue 実行ごとに `retry_count` increment
  - `retry_count >= 3` → step20 は **typed result `RescueExhausted{agent, retry_count}` を orchestrator に返却**(step20 自身が processed.jsonl に append しない、single-writer invariant 維持)
  - orchestrator が受け取り次第 `processed.jsonl` に `needs_manual_recovery { reason: 'worktree_rescue_loop', failed_step: '20' }` append(**PR-level terminal のみ**、global sentinel は作らない)。該当 run は terminal 扱いで detect が再 queue しない、他 PR の step70 は影響なし(rev15、Codex rev14 R1 対応、step70 transactional 失敗時のみ global sentinel)
- rescue 実行(rev21 再設計、Codex rev20 Critical #1/#2 対応: **git stash を使わず worktree-local 完結**):
  - **per-worktree flock**: `<run>/20-pass1/<agent>/.rescue.lock` を取得(concurrent rescue race 防止)
  - **flock 取得後、.heartbeat と pid を再読込**(Claude rev20 H1 対応): re-check で NOT stale なら lock release + abort、agent 作業続行
  - worktree の HEAD が `expected_base_sha` ならそのまま再実行
  - 進んでいる場合(**git stash の repo-shared namespace 問題を回避、worktree 内で完結**):
    1. rescue_id 生成: `<run_id>-<agent>-rescue-<retry_count>-<unix_ts>`
    2. 退避先作成: `<run>/20-pass1/<agent>/rescued/<rescue_id>/`
    3. **pending commits bundle** (rev22 + rev25 reachability 対応、Codex rev21 Critical #1 + Claude rev25 H2): `git rev-list <expected_base_sha>..HEAD` で commit 群を列挙。**`<expected_base_sha>` が reachable でない場合**(shallow clone / partial fetch で missing)は bundle create が失敗、この場合 fallback として `git bundle create <rescued_dir>/commits.bundle HEAD --objects` で HEAD tip の完全 history を退避(base ref 指定せず、bundle は大きくなるが確実に restore 可能)。`state.json` に `{expected_base_sha, rescued_head_sha, commit_count, bundle_mode: 'range' | 'full_head'}` を記録。restore 手順: `git fetch <commits.bundle> <rescued_head_sha>:refs/rescued/<rescue_id> && git reset --hard refs/rescued/<rescue_id>` で HEAD を rescue 時点に戻せる
    4. **tracked 差分**(未 commit の working tree 変更): `git diff HEAD --binary -- . ':!.rescued'` → `.rescued/<rescue_id>/tracked.patch` → fd.Sync() + parent dir fsync
    5. **staged 差分**: `git diff --cached --binary -- . ':!.rescued'` → `.rescued/<rescue_id>/staged.patch` → fd.Sync() + parent dir fsync
    6. **untracked ファイル** (Claude rev21 C2 対応: path traversal 防止):
       - `git ls-files --others --exclude-standard -z -- . ':!.rescued'` で NUL-separated 一覧化
       - 各ファイル path を `filepath.Clean()` → `dst := filepath.Join(rescueBase, "untracked", cleaned)` → **`strings.HasPrefix(dst, rescueBase+"/")` で dst が rescueBase 配下であることを assert、外なら reject + abort**(symlink/.. 経由 escape 防止)
       - symlink は `os.Lstat` で検知し `.rescued/<id>/untracked-symlinks.txt` に記録のみ(target 自動追随で外部書込しない)
       - regular file のみ `cp -a` 相当で dst に配置
       - 全 file 書込後、各 fd.Sync() + parent dir fsync
    7. **ignored ファイル** (rev22、Codex rev21 H1 対応: データ損失防止): 次 step で `git clean -fdx` 相当を使う場合 ignored も消えるので、必要なら退避が必要。**本計画では `git clean -fd` (x 無し) を使い ignored ファイルは残存**させる(agent が `.env` など ignored 設定ファイルを持つ可能性)。`.rescued/<id>/ignored.txt` にリストのみ記録
    8. HEAD SHA / retry_count / 時刻 / 各 patch + commits.bundle の **実測 sha256** を `.rescued/<rescue_id>/state.json` に atomic write (WriteAtomic)
    9. **barrier (Claude rev21 C1 対応: 実測 verify)**: state.json を別 open で読み直し、list された各 file (commits.bundle / tracked.patch / staged.patch / untracked/**) を **再 open して sha256 計算し state.json 記載値と照合**。自己参照ではなく file system 経由 round-trip で partial write / fs corruption を検出。**失敗 → abort**(reset せず、retry_count++)
    10. barrier 通過後のみ `git reset --hard <expected_base_sha>` + `git clean -fd -- . ':!.rescued'`(untracked のみクリア、`-x` 付けず ignored は残す、`.rescued/` は守る)
    11. retry_count++ して再実行
  - worktree ディレクトリ消失なら step10 worktree metadata から再作成
  - `.rescued/` 配下は debug 用に永続保持(operator 復元: `git apply tracked.patch` + `git apply --cached staged.patch` + `cp -r untracked/* .`)
  - **git stash 不使用**のメリット: repo-shared stash ref との race 無し(Codex#3 C1 解決)、barrier 失敗で stash pop 復元不要(worktree 状態不変のまま abort 可、C2 解決)

### 1-C: step50 (pass2 implement)
**step20 からコピペ**(Phase 1.5 で抽出)。候補ルール適用ロジックだけ追加。

### 1-D: step30 (pass1 score)
PanelReview / 5次元 / reasons cap + sidecar / compliance / `LoadScorableManifest`

**panel review per-role resume (rev14/15 最新版、io-contracts.md と 1:1)**:
- 中間スコア書出しは `(agent, judge_role, dimension)` 単位で `<run>/30/scores-A-raw.jsonl` と `compliance-A-raw.jsonl` に append(**raw retention mandatory、削除禁止**)
- raw entry に provenance:
  - primary/secondary: `{agent, judge_role, dimension, score, reasons, output_sha256}`
  - **arbiter**: 加えて `primary_ref: {role: 'primary', sha256}, secondary_ref: {role: 'secondary', sha256}`
- 最終 verdict は panel 解決後に `<run>/30/scores-A.jsonl` / `compliance-A.jsonl` に append。再採点時は新 entry を append し `CollapseByKey(agent, dimension)` で latest-wins(append-only 維持で invalidate 相当)
- **done.marker** (rev14 追加、rev15 hash 具体化):
  - step30 (`30/done.marker`): `{completed_agents, dimensions, expected_counts: {scores, compliance}, content_hashes: {scores_final, compliance_final}, raw_hashes: {scores_raw, compliance_raw}, resolved_at}`(step30 に pairwise は無い)
  - step60 (`60/done.marker`): `{completed_agents, dimensions, expected_counts: {scores, compliance, pairwise}, content_hashes: {scores_final, compliance_final, pairwise_final}, raw_hashes: {scores_raw, compliance_raw}, resolved_at}`(pairwise 含む)
  - Hash algo: io-contracts.md §cardinality 参照。canonical JSON は `internal/contracts/canonical.go#CanonicalMarshal()`(map+sort.Strings 経由で field 順を確定、`encoding/json` の struct 定義順依存を排除)を使う
  - `(agent, dimension)` 全組合せ final verdict 揃った時 atomic write
  - resume 時 marker 存在 + counts/hashes 現 file 一致なら skip、不一致なら marker 削除 → raw から再 reduce → 不足 role launch → 揃ったら marker 再生成
- arbiter resume: `primary_ref / secondary_ref` の sha256 が最新 primary/secondary raw と一致するもののみ valid(CollapseByKey reducer の arbiter 用拡張 rule)。追加 marker 不要、provenance-only
- raw ファイルは **retention mandatory、削除禁止**(rev15、Codex rev14 R3 対応): done.marker の raw_hashes verify の source of truth。削除すると resume 時 raw hash 照合が不可能になるため Phase 0 では無期限保持

### 1-E: step60 (pass2 score + pairwise)
**step30 からコピペ**。pairwise.jsonl のみ追加。

### 1-F: step40 + step70 + archive

**step40**: 候補ルール生成 + registry 類似度 → new/update/duplicate

**step70** (`io-contracts.md` step70 節と 1:1 対応、rev6):
- **`<runs_base>/promotion.lock`** flock 下で全 flow 排他(global lock、step70/recover/sunset_tick 共有。best_branch + rules-registry を 1 critical section に)
- 起動時: `<runs_base>/needs-recovery/*.json` と `<runs_base>/needs-recovery/*.aborted.json` 両方 sentinel scan → 1件でもあれば新規 step70 を block(sentinel 残存中は全 run 停止)
- stage 遷移: planning → branch_pushed → registry_appended → decision_written → finalized
- recovery は lock 取得後に state 再読込
- **Stage 4 (registry_appended) の判定順序** (io-contracts.md step70 と一致):
  - a. **idempotency_key 検索**: N=2000 bounded tail scan または rules-idempotency-index.jsonl(閾値超過時) による O(1) lookup。一致 → registry_append_result 記録して skip
  - b. `current_registry_head == registry_head_before` → CAS append + 1回 retry
  - c. `!=` かつ a 不一致 → rollback path
- **registry size check を append 時に実施**(rev6、24h gate と独立):
  - **閾値(io-contracts と統一、rev28 sync)**: 1500 超 → slog warning + `registry_size_high` processed append
    - step70 emit: `{ kind: 'registry_size_high', source: 'step70', pr, run_id, step: '70', count }`
    - sunset_tick emit: `{ kind: 'registry_size_high', source: 'sunset_tick', count }`
    - 以降 `rules-idempotency-index.jsonl` 自動生成有効化
    - 1800 超 → 上記 + index lookup mandatory
    - 2000 超 → 上記 + `registry_size_critical` を同 shape で append + stderr 強制
- **Rollback path**: remote HEAD 確認 → safe revert 可なら terminal rollback(decision.json + processed rollback、rollback は per-PR terminal で scheduler は次 PR 進行) / 不可なら **needs_manual_recovery** (intention 保持 + **durable sentinel `<runs_base>/needs-recovery/<run_id>.json` atomic write** + processed terminal event + flock release)
- **needs_manual_recovery 状態は `auto-improve recover --run <id>` コマンドで operator が解除**(sentinel 削除 + intention 処理)
- flock は live 排他のみ、durable block は sentinel が担う

**archive(cycle③)**:
- `internal/archive/sunset.go` に business logic 集約 (Codex R2 #6 対応)
- `cmd/auto-improve/sunset.go` と `internal/orchestrator/sunset_tick.go` は **両方とも `archive.RunSunsetWithLock(ctx, opts)` を call するだけの wiring**(rev31 統一、Codex rev29 #2 対応。直接 `archive.RunSunset` を呼ばない、lock 経由版のみ。`RunSunsetWithLock` 内部で promotion.lock 取得 → stale marker reconcile → 24h gate → archive.RunSunset → finalize → release を一貫実行)
- manual invoke:
  - `auto-improve sunset` → 24h gate 遵守 (default)
  - `auto-improve sunset --force` → gate bypass
- **per-op idempotency** (rev6 強化、Codex rev5 R2 #4 対応):
  - 各 archive operation に `op_id = sha256(sunset_run_id || rule_id || transition)` 付与
  - registry には entry ごとに `op_id` を含める
  - reconcile は per-op で registry tail scan し、op_id 一致 entry の有無で個別判断(run 単位 batch 判定ではない)
  - stale marker 発見時: marker の sunset_run_id に紐付く全 `{rule_id, transition}` pair を business logic 側で列挙 → 各 op_id を individually check → 未 append のものだけ resume

**recover コマンド** (rev6 追加, rev10 完全版、io-contracts.md §recover と 1:1):
- `auto-improve recover --run <id> [--rollback | --adopt-anyway | --inspect | --mark-manual-abort | --finalize-cleanup | --clear-sentinel]`
- **全 subcommand**: `<runs_base>/promotion.lock` を `defer unlock()` でラップして取得、lock 取得後 state 再読込(intention / decision / sentinel / remote HEAD / registry head / idempotency)
- `--inspect`: read-only state dump(JSON + human readable)。副作用無し
- `--rollback`: safe matrix 該当 cell のみ rollback path 実行(decision.json rollback + processed rollback append + sentinel 削除 + intention 削除)
- `--adopt-anyway`: safe matrix の cell(HEAD == target_sha AND idempotency hit)のみ許可、stage=decision_written 相当から resume
- `--mark-manual-abort`: **intention.json を `stage: 'needs_manual_recovery'`, `recovery_reason: 'manual_abort_pending_cleanup'`, `failed_step: '70'` で overwrite** + decision rollback 書込 + sentinel を `.aborted.json` に rename(block 継続)+ processed `needs_manual_recovery { reason: 'manual_abort_pending_cleanup' }` append。**sentinel は削除しない**(branch/registry 修復完了まで次 promotion block)
- `--finalize-cleanup --remote-head <sha> --registry-head <sha>`: operator が手動で branch/registry 整合済みの宣言。**両 SHA 必須**。promotion.lock 下で remote HEAD と registry head を再確認、両方一致で `.aborted.json` 削除、pipeline 復旧。片方でも不一致なら refuse
- `--clear-sentinel`: sentinel(`.json` or `.aborted.json`)削除 + **条件付き processed.jsonl append**(rev22、io-contracts §recover table と統一): 当該 run_id の末尾 event が既 terminal (`needs_manual_recovery` 等) なら append しない(terminal 二重禁止)、末尾が non-terminal の場合のみ `completed { detail: 'sentinel_manually_cleared' }` terminal append。最終手段
- panic/signal 時は defer unlock() + os.Exit(1)、lock は OS release に依存しない

**interruption classifier** (rev7 追加, Codex rev6 R2 #4):
- `internal/interruption/classify.go` に provider 別 classifier 集約
- 入力: exit_code, stdout tail, stderr tail
- 出力: `InterruptionKind { None | RateLimit | Budget | Context | Signal | Unknown }`
- claude CLI patterns: exit 429 / stderr に "rate_limit" or "overloaded" / JSON エラー種別
- codex CLI patterns: exit 429 / stderr に "quota" / HTTP response code
- fixture-based test 必須(各 CLI の代表 error 出力を testdata に置く)
- step20/50/30/60 は全てこの classifier を call して `interrupted` event を生成

---

## Phase 1.5: helper 抽出 (1 agent, ~60min; drift 発見時は stop-the-line)

**Codex R2 #5, R3 #5 対応**: semantic reconciliation リスクあり + stop-the-line path 明示。

**normal path (drift ゼロ)**:
1. **pre-refactor equivalence audit**: step20 ↔ step50 / step30 ↔ step60 の diff を取り、pass 固有差分(候補ルール適用、pairwise 生成)以外の変更がゼロか機械的に確認
2. `internal/steps/agentrunner/` (step20/50 共通) と `internal/steps/scorecore/` (step30/60 共通) 作成
3. 各 step を helper 呼出に書き換え
4. **Phase 1 ユニットテスト + Phase 2 conformance test + step70 recovery test 全 rerun** (Codex R1 #5 対応、1.5 も main gate)

**stop-the-line path (drift 発見時、rev5 新規)**:
1. audit phase で pass 固有差分以外の変更を検出したら **即座に Phase 1.5 停止**
2. Phase 2 の golden fixture 作業も一時凍結(fixture が drifted 変種を参照していないか確認)
3. main thread が drift 内容を精査し、どちらが正しい挙動か **source-of-truth を決定**(bug 混入か意図的差分か)
4. 誤変種の step を 1 agent で修正 → unit test rerun
5. 影響 fixture を再生成
6. Phase 1.5 から再開(normal path 1 に戻る)
7. drift 修正コストは見積もり外。最悪 +60min 〜 数時間

---

## Phase 2: orchestrator 本体 + conformance gate (1 agent, Phase 1 並走可, ~1h)

- `internal/orchestrator/cycle.go`: step10→20→30→40→50→60→70
- step 完了ごと `step_done` append / 失敗は `failed` + skip / step 70 intention 経由 recovery
- `run --detect-loop` 末尾で `sunset_tick.Maybe()`

**`testdata/golden_run/**` owner は Phase 2** (Codex R3 #5 対応):
- 事前作成の task-package.json / 各 step 成果物 fixture
- `cycle.Run()` の決定論的 output 期待値
- 初期 seed は Phase 2 agent が作成、step 契約変更時の fixture 更新も Phase 2 owner

**Crash 模擬 fixtures (rev11 更新、io-contracts.md step70 recovery 表の各到達可能状態に 1:1 対応、planning は 3 分割)**:

| # | fixture 初期状態 | 期待 recovery 動作 |
|---|---|---|
| 1a | intention 有(stage=planning)/ decision 無 / remote HEAD == best_sha_before | **non-terminal**: intention 保持 + `interrupted { reason: 'pre_push_crash' }` append、次 tick で同 intention を planning から resume |
| 1b | intention 有(stage=planning)/ decision 無 / remote HEAD == target_sha | push 済判定、stage=branch_pushed 相当として Stage 4 判定へ進む |
| 1c | intention 有(stage=planning)/ decision 無 / remote HEAD == その他 | needs_manual_recovery (reason: `remote_divergence`, failed_step: `70`) + sentinel atomic write |
| 2 | intention 有(stage=branch_pushed)/ decision 無 / registry_head 一致 / idempotency 未 append | Stage 4 判定(b)で append → Stage 5-7 完了 |
| 3 | intention 有(stage=branch_pushed)/ decision 無 / idempotency 既 append | Stage 4 判定(a)ヒット → registry_append_result 記録 → Stage 5-7 完了 |
| 4 | intention 有(stage=registry_appended)/ decision 無 | Stage 5-7 完了 |
| 5 | intention 有(stage=decision_written)/ decision 有 | intention 削除 + promoted append |
| 6 | intention 無 / decision 有(promoted 済み) | `promoted` event の有無確認、無ければ append、有れば noop |

**needs_manual_recovery + recover 6-flag fixtures** (rev11 拡張、Codex rev10 R2 対応):
| # | 初期状態 | 操作 | 期待結果 |
|---|---|---|---|
| 7 | sentinel(.json) 有 | `run --detect-loop` | 全 step70 block、通常 exit |
| 8 | sentinel(.json) 有 + safe cell (HEAD==target, idempotency hit) | `recover --adopt-anyway` | finalize + sentinel 削除 + promoted |
| 9 | sentinel(.json) 有 + safe cell (HEAD==best_sha_before) | `recover --rollback` | rollback + sentinel 削除 |
| 10 | sentinel(.json) 有 + refuse cell | `recover --rollback` | refuse 出力(state 変更なし) |
| 11 | sentinel(.json) 有 | `recover --inspect` | state dump(変更なし) |
| 12 | sentinel(.json) 有 | `recover --mark-manual-abort` | sentinel → `.aborted.json` rename + decision rollback + processed needs_manual_recovery append(**block 継続**) |
| 13 | sentinel(.aborted.json) 有 | `run --detect-loop` | 同様に block |
| 14 | sentinel(.aborted.json) 有 + 正しい両 SHA | `recover --finalize-cleanup --remote-head X --registry-head Y` | `.aborted.json` 削除 + completed append |
| 15 | sentinel(.aborted.json) 有 + 不一致 SHA | `recover --finalize-cleanup --remote-head Z --registry-head Y` | refuse(state 変更なし) |
| 16 | sentinel 有、末尾 non-terminal | `recover --clear-sentinel` | sentinel 削除 + `completed { detail: 'sentinel_manually_cleared' }` terminal append(該当 run 再 resume 不可) |
| 16b | sentinel 有、末尾 既 terminal (needs_manual_recovery 等) | `recover --clear-sentinel` | sentinel のみ削除(processed.jsonl は不変、terminal 二重禁止)、最終手段 |

Phase 2 owner は全 16 fixture を実装する。

**完了ゲート**:
- mock で conformance test pass
- 実 step 差し替え後 同 test pass
- crash recovery + recover CLI 全 16 パターン pass(step70 recovery 8[1a/1b/1c 分割]+ recover 6-flag × allowed/refuse 8)
- **Resume テスト必須 (rev6)**:
  - 各 step の途中(rate limit 模擬)で interrupted → 再起動で resume → 完了
  - interrupted 後の step 単位 idempotent skip 動作確認
  - agent 1体が timeout/error の state で 残り 2体 manifest が揃っている場合の採点スキップ動作確認

---

## Phase 3: infra (3 agent 並列, ~1h)

### 3-A: GitHub Actions
`workflow_dispatch` only、schedule 無し、concurrency: `auto-improve-prod`

### 3-B: launchd (simplified, Codex R3 #3 対応)
- plist: `StartInterval: 3600` + `ProgramArguments: [auto-improve, run, --detect-loop, --with-preflight]`
- per-exit-code backoff は **廃止**。preflight 失敗 → state 未更新 → launchd 次 tick で単純再試行

### 3-C: release + install (staged transaction, rev5 full rewrite, Codex R2 #1 対応)

- `.goreleaser.yml`: darwin/arm64, darwin/amd64, linux/amd64
- `Makefile` に `release / install` target append(既存行変更禁止)
- `scripts/install.sh` (同一 filesystem staging + writability + plist transactional rollback、rev6 強化):
  ```
  INSTALL_DIR=${INSTALL_DIR:-/usr/local/bin}   # env で override 可 (sudo 回避用)
  TARGET=$INSTALL_DIR/auto-improve
  STAGE=$INSTALL_DIR/.auto-improve.new.$$       # 同一 FS で EXDEV 回避
  BACKUP=$INSTALL_DIR/.auto-improve.bak.$$
  PLIST=~/Library/LaunchAgents/com.nishimoto265.auto-improve.plist
  PLIST_BAK=~/Library/LaunchAgents/com.nishimoto265.auto-improve.plist.bak.$$

  1. writability check: test -w "$INSTALL_DIR" → NG なら stderr に
     "INSTALL_DIR=$INSTALL_DIR not writable. Re-run with sudo, or set INSTALL_DIR=$HOME/.local/bin"
     を print して exit 2
  2. 同一 FS staging: curl -L <release url> -o "$STAGE" + chmod +x
  3. "$STAGE" で preflight 実行(外部 CLI + auth 確認)
     失敗 → rm "$STAGE"; print install URL (gh/claude/codex/jq/yq); exit 1
  4. binary backup: test -f "$TARGET" && cp "$TARGET" "$BACKUP"
  5. plist backup: test -f "$PLIST" && cp "$PLIST" "$PLIST_BAK"
  6. 旧 plist unload (動いている場合): launchctl bootout gui/$UID "$PLIST" || true
  7. atomic swap: mv "$STAGE" "$TARGET" (同一 FS なので atomic)
     失敗 →
       [ -f "$BACKUP" ] && mv "$BACKUP" "$TARGET"
       [ -f "$PLIST_BAK" ] && { mv "$PLIST_BAK" "$PLIST"; launchctl bootstrap gui/$UID "$PLIST"; }
       rm -f "$STAGE"; exit 3
  8. plist 再生成: scripts/install-launchd.sh が INSTALL_DIR を受け取り、ProgramArguments を "$TARGET" 絶対 path で template 生成
  9. launchctl bootstrap gui/$UID "$PLIST"
     失敗 →
       rm "$TARGET"
       [ -f "$BACKUP" ] && mv "$BACKUP" "$TARGET"
       [ -f "$PLIST_BAK" ] && { mv "$PLIST_BAK" "$PLIST"; launchctl bootstrap gui/$UID "$PLIST"; }
       exit 4
  10. 成功: rm -f "$BACKUP" "$PLIST_BAK"
  ```
- **INSTALL_DIR 変更時の挙動**: install.sh が毎回 plist を絶対 path で再生成するため、INSTALL_DIR を変えて再 install すれば plist の ProgramArguments も自動追従
- 特徴:
  - Stage も Backup も INSTALL_DIR と同一 filesystem に置くことで EXDEV 回避
  - writability check を最初に行い、launchd uninstall は swap 成功後まで遅延(部分的停止回避)
  - swap failure と launchd load failure でそれぞれ rollback path を定義
  - INSTALL_DIR env で `$HOME/.local/bin` に install 可(sudo 不要)、README に両方明記
- `README.md` Runtime Dependencies セクション必須(git / gh / jq / yq / claude / codex)、sudo 要否と INSTALL_DIR 例を記載

---

## Phase 4: 仕上げ (1 agent, 半日)

- 実PR 1件で end-to-end
- crash-recovery 手動テスト(stage ごと kill → recovery)
- `docs/design/全体設計.md` Go 版更新
- `Go実装計画.md` を `docs/design/archive/` に移動
- `README.md` 完成
- tag v0.1 + Release

---

## タイムライン

```
Phase -1:              1 agent  ~10min
   ↓
Phase 0-bootstrap-1:   1 agent  ~50min  [contracts + stepio + io-contracts.md 同期]
   ↓ (Codex 3体レビュー = schema 最終ゲート、critical/high 0 で pass)
Phase 0-bootstrap-2:   1 agent  ~50min  [io helpers + cobra root + hooks + CI]
   ↓ (0-bootstrap-gate ~20min 別枠、critical/high 発見時は bootstrap-1/2 に戻る)
Phase 0-parallel:      5 agent  ~1.5h   [config/state/preflight/detect/orch骨格/judges骨格]
   ↓ (merge + 統合テスト)
Phase 1:               6 agent  ~3h     [step10/20/30/40/50/60/70+archive]
Phase 2:               1 agent  ~1h     [cycle + golden fixture + conformance test]  (Phase 1 並走可)
   ↓ (filesystem conformance + crash recovery ゲート)
Phase 1.5:             1 agent  ~60min  [equivalence audit + helper 抽出 + 全 test rerun]
   ↓ (same gate 再通過)
Phase 3:               3 agent  ~1h     [Actions / launchd / release+install]
   ↓
Phase 4:               1 agent  半日    [実PR検証 + docs + tag]
```

---

## 決定事項サマリ (rev1→rev5)

| # | 論点 | 決定 |
|---|---|---|
| 1 | step70 transactional | 5 stage intention + recovery state machine (**6 到達可能状態 + manual recovery、rev9 で更新、#62-74 参照**) |
| 2 | idempotency_key | `sha256(run_id || target_sha || best_sha_before || candidates_hash)`、planning で1回生成・永続化 |
| 3 | registry recovery | registry_head_before 分岐追加、tail scan 2000件 bound、intention に append result 記録で O(1) path 優先 |
| 4 | strict JSON | `json.Decoder + DisallowUnknownFields + 2回目 Decode で io.EOF 要求` (More() 非使用) + custom UnmarshalJSON |
| 5 | Decision variant | adopt/reject/noop/rollback のみ、error 廃止 |
| 6 | sunset | business logic `internal/archive` 一本化、2 entry point は wiring のみ。idempotent transaction (marker + flock + idempotency_key) |
| 7 | sunset 24h gate | auto: 必ず遵守 / manual: 遵守 default、`--force` で bypass |
| 8 | Phase 0 分割 | bootstrap (A 単独 90min 2段) + parallel (B-F) |
| 9 | DRY | Phase 1 中禁止、Phase 1.5 で equivalence audit 付き抽出、gate 再通過 |
| 10 | remote CAS | `--force-with-lease=<branch>:<sha>`、rollback は lease 一致時のみ |
| 11 | Phase 2 owner | `testdata/golden_run/**` + `orchestrator/*_test.go` 明示 |
| 12 | launchd backoff | 廃止、fixed interval + preflight-first fail-soft |
| 13 | install.sh | staged transaction (tmp DL → preflight → atomic swap → launchd → rollback on fail) |
| 14 | sync hook | 一方向 contracts→docs のみ、docs-only は通す |
| 15 | `--with-preflight` | Phase 0-E run.go owner が実装、preflight 成功時のみ state mutation |
| 16 | 4KB overflow | schema 全 6種(scores/compliance/classification/pairwise/processed/rules-registry)に cap + sidecar 方針明記 |
| 17 | atomic rename | darwin/linux 限定、Windows 除外 |
| 18 | helper drift | archive/sunset business logic 1点集約で drift 防止 |
| 19 | step70 排他 | `<runs_base>/promotion.lock` (global) flock で全 flow 排他 + recovery も lock 取得後 state 再読込 |
| 20 | Stage 4 判定順序 | idempotency tail scan 先行 → registry_head 比較 → CAS |
| 21 | rollback 非終端化 | divergence / lease failure 時は `needs_manual_recovery`(intention 保持、`auto-improve recover` で解除) |
| 22 | registry scan bound | 2000件 + 運用 metric 警告(1500超)、閾値到達時は index 併設検討 |
| 23 | sunset recovery 順序 | lock → stale marker reconcile → gate check → run |
| 24 | installer | 同一 FS staging + writability check + swap rollback + INSTALL_DIR env(sudo 回避) |
| 25 | validator singleton | `internal/validation/validator.go` に sync.Once + Instance() 一本化 |
| 26 | intention UnmarshalJSON | data copy 経由(mutation 保護) |
| 27 | intention atomic | tmp cleanup + stage 遷移も rename ベース、concurrent 保護は promotion.lock |
| 28 | crash fixture | **rev11+ で #89 に統合、本行は削除済み** |
| 29 | bootstrap-1 ゲート | critical/high あれば block、medium 以下 conditional pass |
| 30 | Phase 1.5 stop-the-line | drift 発見時 fixture 作業凍結 + source-of-truth 判定 + 再生成 + 再開 |
| 31 | needs_manual_recovery 解除 | `auto-improve recover --run <id> [--rollback|--adopt-anyway]` CLI (Phase 1-F) |
| 32 | global promotion lock | `<runs_base>/promotion.lock` に変更(per-run promotion.lock は廃止)、step70/recover/sunset_tick 共有 |
| 33 | needs_manual_recovery durable sentinel | `<runs_base>/needs-recovery/<run_id>.json` ファイル、flock と独立の block 機構 |
| 34 | rollback terminal 明確化 | per-PR terminal(再試行せず)、scheduler は次 PR 進行、手動再試行は `run --pr <n> --from-scratch` |
| 35 | installer plist transactional | plist も backup/restore、INSTALL_DIR 変更時は絶対 path 再生成 |
| 36 | sunset per-op idempotency | `op_id = sha256(sunset_run_id || rule_id || transition)`、reconcile は per-op |
| 37 | registry size check | step70 append 時(24h gate と独立)、1500/1800/2000 の閾値で warning/index/alert |
| 38 | recover CLI 実装 | cmd/auto-improve/recover.go + internal/recover/**、Phase 1-F owner |
| 39 | Resume 機構 (rev6) | interrupted event, ResumeTarget, 各 step idempotent skip, signal/rate_limit/budget handling |
| 40 | crash fixture | **rev11+ で #89 に統合、本行は削除済み** |
| 41 | state vocabulary 拡張 | interrupted / warning / needs_manual_recovery schema 追加、全 non-terminal event に step required |
| 42 | processed detail overflow | `detail` 300字 + sidecar `<run>/processed-details/<sha256>.txt` + `detail_overflow_ref`(rev7) |
| 43 | panel review per-role resume | `(agent, judge_role, dimension)` 単位の raw jsonl + 最終 verdict の別 marker(rev7) |
| 44 | agent worktree resume | `.resume-state.json { expected_base_sha, started_at, pid }` + resume 時 hard reset or rescue(rev7) |
| 45 | interruption classifier | `internal/interruption/classify.go` provider 別 pattern 集約 + fixture test(rev7) |
| 46 | panic-safe lock | `acquire → defer release` pattern 必須、panic 時は unlock → exit 1(rev7) |
| 47 | --from-scratch worktree cleanup | 新 run_id 発行前に旧 run の 6 worktree を prune(rev7) |
| 48 | recover lock-after-reread | `--adopt-anyway` は lock 取得後 remote HEAD + registry head + idempotency 再確認必須(rev7) |
| 49 | idempotency-index rebuild | rebuildable cache、不整合時 tail scan fallback、registry が単一 source of truth(rev7) |
| 50 | main.go stub に recover 追加 | Phase 0-A bootstrap 責務(rev7) |
| 51 | 全体設計.md obsolete notice | rev6 以降未同期、io-contracts.md + Go実装計画.md 正本、Phase 4 で latest canonical (rev13+/rev11+) に sync 書き直し |
| 52 | step30/60 cardinality-based 完了 | `<run>/30/done.marker` `<run>/60/done.marker` を atomic write、`(agent, dimension)` 全揃い時のみ(rev8) |
| 53 | recover safe matrix | `{remote HEAD, idempotency, registry head, intention stage}` ×4 軸で allowed cell を表形式で明記(io-contracts.md)(rev8) |
| 54 | worktree rescue lease | **rev9 で #67 に統合、本行は削除済み** |
| 55 | arbiter provenance | raw entry に `primary_ref.sha256 / secondary_ref.sha256`、upstream 変更時再実行(rev8) |
| 56 | expected_base_sha source | `task-package.json.worktrees[agent].base_sha` 固定、live HEAD 再計算禁止(rev8) |
| 57 | resume precedence | queue.go で non-terminal PR を先行 resume、fresh detect は resume 完了後(rev8) |
| 58 | promoting/warning vocabulary | non-terminal として schema 追加、resume class 明記(rev8) |
| 59 | interruption owner | Phase 0-F、`internal/interruption/**` + `testdata/interruption/**`、unknown は alert 必須(rev8) |
| 60 | legacy log 非互換 | v0.1 fresh start 前提、rev8 以前 log は reader が警告 + skip、`--from-scratch` でリセット(rev8) |
| 61 | promotion.lock 残存削除 | 全箇所 global `<runs_base>/promotion.lock` に統一、per-run 記述削除(rev8) |
| 62 | step70 planning recovery | post-push crash も検知、remote HEAD == target_sha なら stage=branch_pushed 相当で resume(rev9) |
| 63 | step70 processed.jsonl ownership | promotion.lock 下で step70 は直接 append 可能 (exception)。他 step は typed result 返却 → orchestrator append(rev9) |
| 64 | done.marker durability | jsonl fsync → parent dir fsync → marker write with counts+hashes、resume verify(rev9) |
| 65 | recover inspect / mark-manual-abort | refuse cell の operator failback、read-only inspect + terminal manual abort(rev9) |
| 66 | arbiter invalidation | **rev11 で #86/#87 に統合、本行は削除済み** |
| 67 | heartbeat-based lease | age-based 廃止、60s heartbeat + **5×interval = 300s** stale 判定(rev21: LLM 応答 3分超過考慮)+ pid 不生存 AND 条件 |
| 68 | processed-index.jsonl | **rev10 で #75 に統合、本行は削除済み** |
| 69 | io-contracts completion marker 表 | done.marker に同期(rev9) |
| 70 | Phase 0 前提 closed | module path / Go / agent type / launch strategy 確定(rev9) |
| 71 | non-terminal vocabulary 統一 | io-contracts + Phase 0-C + queue で `promoting / warning` 含む 5種(rev9) |
| 72 | step20 single-writer 維持 | rescue exhausted は typed result 返却、processed.jsonl 直接 append 禁止(rev9) |
| 73 | --mark-manual-abort non-terminal sentinel | `.aborted.json` rename で block 継続、`--finalize-cleanup --remote-head <sha> --registry-head <sha>` 両SHA必須で operator 明示宣言後に解除(rev10/rev11) |
| 74 | planning+HEAD==best_sha_before recovery | terminal rollback → non-terminal `interrupted { reason: 'pre_push_crash' }` に変更(rev10) |
| 75 | processed-index.jsonl | Phase 0 から削除、future work(rev10、個人運用は full scan 十分) |
| 76 | needs_manual_recovery reason enum | lease_failure / remote_divergence / registry_divergence / worktree_rescue_loop / manual_abort_pending_cleanup(rev10)。**pre_push_crash は interrupted.reason 側に移動(rev11)** |
| 77 | done.marker hash 方式 | **rev11 で #86 に統合、本行は削除済み** |
| 78 | recover CLI 全 flag 反映 | `--inspect / --rollback / --adopt-anyway / --mark-manual-abort / --finalize-cleanup / --clear-sentinel` を Phase 1-F で実装(rev10) |
| 79 | 並列実装時の約束 8 | step70 の promotion.lock 保持中 processed append exception を明記(rev10) |
| 80 | "lock 保持" 表記削除 | durable block は sentinel 一択、flock は live 排他のみ(rev10) |
| 81 | README canonical docs pointer | 正本を Go実装計画.md / io-contracts.md に明示、全体設計.md は参考扱い(rev10) |
| 82 | Phase 0-bootstrap 120min + review gate 別枠 | 0-bootstrap-1 (50min) + 0-bootstrap-2 (50min) + review gate (20min 別枠) = 総 120min(rev10) |
| 83 | interrupted.reason に pre_push_crash 追加 | needs_manual_recovery.reason から削除、interrupted 側に一本化(rev11) |
| 84 | pre_push_crash 時 intention 保持 | 削除せず snapshot 再利用、同 intention で planning から resume(rev11) |
| 85 | --finalize-cleanup に --registry-head 必須 | branch だけでなく registry も両方 verify(rev11) |
| 86 | raw/final layer 分離 | scores-A-raw.jsonl で judge_role 単位、scores-A.jsonl で agent,dimension 単位、done.marker hash は final(rev11) |
| 87 | arbiter rerun worked example | io-contracts.md に 7 step の具体 flow 明記(rev11) |
| 88 | recover 6-flag state transition table | 全 flag の precondition/sentinel/intention/decision/processed/結果 を table 化(rev11) |
| 89 | Phase 2 crash fixture 16パターン | 8 step70 recovery(1a/1b/1c 含む 3分割)+ 7 recover flag × refuse/allowed + 1 sentinel block(rev11) |
| 90 | 82-decision 表 stale row 物理削除 | stale 行は `superseded by` 注記から物理削除にアップグレード(rev33、Claude rev32 C2 対応) |

---

## Phase 0-bootstrap 前提(rev9 で closed)

| 項目 | 決定 |
|---|---|
| module path | `github.com/nishimoto265/auto-improve` |
| Go toolchain | `go 1.22`(`go.mod` で pin、最新 patch は自動 tracking) |
| 並列実装 agent type | `back`(Go 得意) |
| Phase 0-parallel 起動 | 1 メッセージに 5 Agent tool call を並べて同時起動 |
| Phase 1 起動 | 1 メッセージに 6 Agent tool call を並べて同時起動 |
| Phase 3 起動 | 1 メッセージに 3 Agent tool call を並べて同時起動 |

ユーザー合意(2026-04-20): これら default で進める、変更要望があれば bootstrap 開始前に指摘。

---

## リスク管理

| リスク | 対策 |
|---|---|
| bootstrap schema 設計ミス | 0-bootstrap-1 後に Codex レビューゲート |
| stepio freeze 後の bug | hotfix protocol(停止→0-A resume→rebase) |
| Phase 2 mock drift | filesystem golden fixture 必須ゲート、Phase 1.5 後 rerun |
| step70 crash | 16 パターン crash 模擬 test(step70 recovery 8 + recover 6-flag × allowed/refuse 8)|
| registry 二重 append | idempotency_key + intention の append_result 直接参照 |
| best_branch 改変 | `--force-with-lease`、divergence 時 abort + alert(single scheduler 前提) |
| sunset 忘却/重複 | auto tick + idempotent transaction + 24h gate |
| docs drift | 一方向 pre-commit hook + CI |
| helper semantic drift | Phase 1.5 equivalence audit、archive は 1点集約 |

---

## 不変の規約 (io-contracts.md 参照)

詳細は `docs/design/io-contracts.md`(rev11 同期済み)に集約。本計画書と契約内容に矛盾があれば io-contracts.md を正とする。
