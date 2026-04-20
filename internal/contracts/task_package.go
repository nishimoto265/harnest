package contracts

import "time"

// WorktreeAllocation describes one of the 6 worktrees (pass1 × 3 + pass2 × 3)
// that step10 carves out of the base repository. `task-package.json.worktrees[]`
// is the **canonical metadata** source; step 20/50/70 must read from there and
// not re-derive from on-disk paths (io-contracts.md §run ディレクトリ構造).
type WorktreeAllocation struct {
	// Agent: a1 / a2 / a3 (positive integer, per pass 3 agents).
	Agent AgentID `json:"agent" validate:"required,agent_id_fmt"`
	// Pass: 1 (step20) または 2 (step50).
	Pass int `json:"pass" validate:"required,oneof=1 2"`
	// Path: absolute filesystem path to the worktree dir
	// (`<worktree_base>/<runId>-pass{1,2}-a{1..N}/`).
	Path string `json:"path" validate:"required"`
	// Branch: git branch name checked out at the worktree.
	Branch string `json:"branch" validate:"required"`
	// BaseSHA: commit at which the worktree was carved
	// (step10 で記録、以降 immutable、rescue の expected_base_sha source).
	BaseSHA string `json:"base_sha" validate:"required,sha1_hex"`
	// HeadSHA: HEAD at allocation time (= BaseSHA for fresh worktrees).
	HeadSHA string `json:"head_sha" validate:"required,sha1_hex"`
}

// TaskPackage is the step10 output artifact, written to `<run>/task-package.json`.
//
// 完了マーカー: task-package.json 存在 (io-contracts.md §completion marker).
// worktrees[] must contain exactly 6 entries (2 passes × 3 agents).
type TaskPackage struct {
	// SchemaVersion: forward-compat knob. "1" for Phase 0 (closed).
	SchemaVersion string `json:"schema_version" validate:"required,oneof=1"`

	RunID RunID  `json:"run_id" validate:"required,run_id_fmt"`
	PR    int    `json:"pr" validate:"required,gt=0"`
	Title string `json:"title" validate:"required"`

	// BaseSHA is the PR merge-base (= `<run>/base.sha` ファイル内容).
	BaseSHA string `json:"base_sha" validate:"required,sha1_hex"`

	// BestBranch is the name of the running best rule-set branch that step10
	// applied before carving worktrees.
	BestBranch string `json:"best_branch" validate:"required"`

	// ReconstructedTaskPrompt: step10 で合成した PR task description.
	// 下流 (step20/50) で prompt に埋める前に必ず
	// `internal/io.SanitizeForPromptEmbedding()` を通す (io-contracts.md §5).
	ReconstructedTaskPrompt string `json:"reconstructed_task_prompt" validate:"required"`

	// Worktrees: 2 pass × 3 agent = 6 件の allocation metadata (正本).
	Worktrees []WorktreeAllocation `json:"worktrees" validate:"required,len=6,dive"`

	// CreatedAt: step10 が task-package を書いた時刻.
	CreatedAt time.Time `json:"created_at" validate:"required"`
}
