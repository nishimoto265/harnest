package stepio

import "github.com/nishimoto265/auto-improve/internal/contracts"

// Step10Request is the input envelope for step 10 (restore-base).
// io-contracts.md §step 10: restore-base.
type Step10Request struct {
	// PR: target GitHub PR number (merged).
	PR int `json:"pr"`
	// BestBranch: 現行 best 設定 branch (= remote に push してある promotion 結果).
	BestBranch string `json:"best_branch"`
	// HarnessFiles: best_branch 上の harness rule files を適用するかどうか。
	// Phase 0 では常に true 想定。
	HarnessFiles bool `json:"harness_files"`
}

// Step10Response is the output envelope for step 10.
// TaskPackage は `<run>/task-package.json` に atomic write 済み前提で返す。
type Step10Response struct {
	RunID       contracts.RunID        `json:"run_id"`
	TaskPackage contracts.TaskPackage  `json:"task_package"`
	BaseSHA     string                 `json:"base_sha"`
	// WorktreesCreated: step10 が新規作成した worktree 数 (resume 時は 0 もあり)。
	WorktreesCreated int `json:"worktrees_created"`
}
