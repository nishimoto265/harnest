// Package stepio freezes the step-to-step Request/Response contracts exchanged
// between orchestrator and individual step implementations.
//
// Phase 0-bootstrap-1 の時点ではここに型と sentinel error のみを置く (実装は
// Phase 1 agent 群が担当)。import から読み取れる API 境界を固めることで、Phase
// 0-parallel / Phase 1 の並列実装 agent が互いの戻り値契約を破らないよう保証
// する。`internal/contracts` 側の schema と二重定義にならないよう、schema type
// は contracts package から re-use する。
package stepio

import (
	"errors"

	"github.com/nishimoto265/auto-improve/internal/contracts"
)

// Sentinel errors: step I/O boundary failures.
//
// io-contracts.md / Go実装計画.md の仕様に対応する typed failure 群。step 実装
// からこれらを直接 return し、orchestrator 側で `errors.Is` で判定する。new
// sentinel を足す場合は必ず本ファイルに定義して他 step から import 可能にする。
var (
	// ErrAgentTimeout: step20/50 で agent が wall-clock timeout した。
	// manifest は ManifestTimeout variant で書かれる。
	ErrAgentTimeout = errors.New("stepio: agent timed out")

	// ErrAllAgentsFailed: step20/50 で 3 agent 全員が非 success で完了。
	// step30 以降は skip → failed event 。
	ErrAllAgentsFailed = errors.New("stepio: all agents failed")

	// ErrScoringFailed: step30/60 の panel review が retry 上限まで失敗した。
	ErrScoringFailed = errors.New("stepio: scoring failed")

	// ErrNoCandidates: step40 で候補 0 件 (= step70 は noop 判定)。
	ErrNoCandidates = errors.New("stepio: no candidates")

	// ErrPromotionFailed: step70 の promotion が transactional に失敗した
	// (rollback 可能 / 不可いずれも含む)。詳細は decision.json を参照。
	ErrPromotionFailed = errors.New("stepio: promotion failed")

	// ErrBestBranchDiverged: step70 で remote best_branch が target_sha /
	// best_sha_before のいずれでもない → needs_manual_recovery 候補。
	ErrBestBranchDiverged = errors.New("stepio: best_branch diverged on remote")

	// ErrNotScorable: manifest が success variant ではない (error / timeout)。
	// LoadScorableManifest がこれを返し、step30/60 は採点対象外として扱う。
	ErrNotScorable = errors.New("stepio: manifest is not scorable")

	// ErrEntryTooLarge: jsonl の 1 行 4KB cap を超過した (overflow sidecar
	// 未対応 path での検出)。io-contracts.md §3 append-only jsonl。
	ErrEntryTooLarge = errors.New("stepio: jsonl entry exceeds 4KB cap")

	// ErrTrailingJSON: strict JSON reader が単一 top-level value の後に
	// 余分な token / bytes を検出した。
	// (contracts.ErrTrailingJSON を wrap。errors.Is で両方にヒットする)
	ErrTrailingJSON = contracts.ErrTrailingJSON

	// ErrUnknownManifestKind: Manifest envelope の kind が success/error/timeout
	// に該当しない (contracts.ErrUnknownManifestKind の re-export)。
	ErrUnknownManifestKind = contracts.ErrUnknownManifestKind

	// ErrUnknownDecisionAction: Decision envelope の action が
	// adopt/reject/noop/rollback に該当しない。
	ErrUnknownDecisionAction = contracts.ErrUnknownDecisionAction

	// ErrUnknownRegistryKind: rules-registry の kind が 6 種
	// (added/updated/status_changed/archived/restored/rolled_back) に該当しない。
	ErrUnknownRegistryKind = contracts.ErrUnknownRegistryKind

	// ErrUnknownStateKind: processed.jsonl の kind が state enum に該当しない。
	ErrUnknownStateKind = contracts.ErrUnknownStateKind

	// ErrUnknownCandidateKind: candidate の kind が new/update/duplicate に
	// 該当しない。
	ErrUnknownCandidateKind = contracts.ErrUnknownCandidateKind
)
