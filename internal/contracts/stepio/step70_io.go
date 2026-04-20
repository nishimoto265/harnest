package stepio

import "github.com/nishimoto265/auto-improve/internal/contracts"

// Step70Request is the input envelope for step 70 (decide + apply).
// io-contracts.md §step 70。`<runs_base>/promotion.lock` は step 実装側で
// acquire/release する契約で、orchestrator は lock 状態を意識しない。
type Step70Request struct {
	TaskPackage contracts.TaskPackage `json:"task_package"`
	// Candidates: step40 の出力を読み込み済みで渡す。
	Candidates contracts.Candidates `json:"candidates"`
	// CandidatesHash: intention.IdempotencyKey 計算に使用。
	CandidatesHash string `json:"candidates_hash"`
	// BestShaBefore: planning 開始時点の remote best_branch HEAD。
	BestShaBefore string `json:"best_sha_before"`
	// RegistryPath: `<runs_base>/rules-registry.jsonl` の絶対 path。
	RegistryPath string `json:"registry_path"`
}

// Step70Response is the output envelope for step 70.
// Decision は `<run>/70/decision.json` に atomic write 済みで渡す前提。
type Step70Response struct {
	RunID    contracts.RunID   `json:"run_id"`
	Decision contracts.Decision `json:"decision"`
	// Promoted: Decision.Action == adopt かつ best_branch push + registry append
	// まで完走した場合のみ true。orchestrator は true のときに promoted event を
	// state に append 可 (step70 自身が既に append 済みなので重複 append しない)。
	Promoted bool `json:"promoted"`
}
