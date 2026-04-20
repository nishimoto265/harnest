package stepio

import (
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
)

// Step30Request is the input envelope for step 30 (score pass1).
// io-contracts.md §step 30 / 60.
type Step30Request struct {
	TaskPackage contracts.TaskPackage `json:"task_package"`
	// ScorableAgents: success manifest を持ち採点対象となる agent 一覧。
	// LoadScorableManifest で filter 済みを前提 (error/timeout は除外)。
	ScorableAgents  []contracts.AgentID `json:"scorable_agents"`
	RubricVersion   string              `json:"rubric_version"`
	PromptVersion   string              `json:"prompt_version"`
}

// Step30Response is the output envelope for step 30.
// 完了マーカー `<run>/30/done.marker` が書かれている前提で返す。
type Step30Response struct {
	RunID        contracts.RunID `json:"run_id"`
	ScoresCount      int         `json:"scores_count"`
	ComplianceCount  int         `json:"compliance_count"`
	ResolvedAt       time.Time   `json:"resolved_at"`
}
