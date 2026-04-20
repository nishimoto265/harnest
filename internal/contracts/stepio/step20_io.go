package stepio

import "github.com/nishimoto265/auto-improve/internal/contracts"

// Step20Request is the input envelope for step 20 (implement pass1).
// io-contracts.md §step 20 / 50.
type Step20Request struct {
	TaskPackage contracts.TaskPackage `json:"task_package"`
	// Agents: step20 が並列起動する agent 一覧 (通常 a1 / a2 / a3)。
	Agents []contracts.AgentID `json:"agents"`
	// TimeoutSeconds: agent 1 体あたりの wall-clock timeout。
	TimeoutSeconds int `json:"timeout_seconds"`
}

// Step20AgentResult: 各 agent ごとの実行結果。manifest は Manifest tagged union。
type Step20AgentResult struct {
	Agent    contracts.AgentID  `json:"agent"`
	Manifest contracts.Manifest `json:"manifest"`
}

// Step20Response is the output envelope for step 20.
type Step20Response struct {
	RunID   contracts.RunID     `json:"run_id"`
	Pass    int                 `json:"pass"` // 固定 1 だが共通 field として持つ
	Results []Step20AgentResult `json:"results"`
	// RescueExhausted: worktree rescue が retry 上限 (3) に達した agent 一覧。
	// orchestrator が processed.jsonl に needs_manual_recovery を append する契約
	// (step 自身は append しない、single-writer invariant 維持)。
	RescueExhausted []RescueExhausted `json:"rescue_exhausted,omitempty"`
}

// RescueExhausted: step20/50 agent の rescue 上限到達通知。
type RescueExhausted struct {
	Agent      contracts.AgentID `json:"agent"`
	RetryCount int               `json:"retry_count"`
}
