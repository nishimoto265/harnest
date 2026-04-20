package stepio

import (
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
)

// Step60Request is the input envelope for step 60 (score pass2 + pairwise).
type Step60Request struct {
	TaskPackage    contracts.TaskPackage `json:"task_package"`
	ScorableAgents []contracts.AgentID   `json:"scorable_agents"`
	RubricVersion  string                `json:"rubric_version"`
	PromptVersion  string                `json:"prompt_version"`
}

// Step60Response is the output envelope for step 60.
// 完了マーカー `<run>/60/done.marker` が書かれている前提。pairwise も含む。
type Step60Response struct {
	RunID           contracts.RunID `json:"run_id"`
	ScoresCount     int             `json:"scores_count"`
	ComplianceCount int             `json:"compliance_count"`
	PairwiseCount   int             `json:"pairwise_count"`
	ResolvedAt      time.Time       `json:"resolved_at"`
}
