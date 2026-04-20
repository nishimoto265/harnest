package stepio

import "github.com/nishimoto265/auto-improve/internal/contracts"

// Step40Request is the input envelope for step 40 (extract-rules).
// io-contracts.md §step 40.
type Step40Request struct {
	TaskPackage contracts.TaskPackage `json:"task_package"`
	// RegistryPath: `<runs_base>/rules-registry.jsonl` への絶対 path。
	RegistryPath string `json:"registry_path"`
}

// Step40Response is the output envelope for step 40.
// 完了マーカー `<run>/40/candidates.json` が書かれている前提で返す。
type Step40Response struct {
	RunID           contracts.RunID        `json:"run_id"`
	Candidates      contracts.Candidates   `json:"candidates"`
	// CandidatesCount: 便利 accessor。len(Candidates.Candidates) と等価。
	CandidatesCount int `json:"candidates_count"`
}
