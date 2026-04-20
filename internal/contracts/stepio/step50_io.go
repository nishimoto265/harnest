package stepio

import "github.com/nishimoto265/auto-improve/internal/contracts"

// Step50Request is the input envelope for step 50 (implement pass2).
// io-contracts.md §step 20 / 50。step20 と同形だが候補ルール適用が加わる。
type Step50Request struct {
	TaskPackage     contracts.TaskPackage `json:"task_package"`
	Agents          []contracts.AgentID   `json:"agents"`
	TimeoutSeconds  int                   `json:"timeout_seconds"`
	// CandidateRuleIDs: best 設定に加えて pass2 で適用する候補 rule_id 群。
	CandidateRuleIDs []string `json:"candidate_rule_ids"`
}

// Step50Response is the output envelope for step 50 (mirrors Step20Response).
type Step50Response struct {
	RunID           contracts.RunID     `json:"run_id"`
	Pass            int                 `json:"pass"` // 固定 2
	Results         []Step20AgentResult `json:"results"`
	RescueExhausted []RescueExhausted   `json:"rescue_exhausted,omitempty"`
}
