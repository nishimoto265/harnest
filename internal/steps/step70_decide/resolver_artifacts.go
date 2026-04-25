package step70_decide

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"

	"github.com/nishimoto265/auto-improve/internal/candidaterules"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/judges"
	"github.com/nishimoto265/auto-improve/internal/steps/scorecore"
	"github.com/nishimoto265/auto-improve/internal/steps/step60contract"
)

func loadStep60Artifacts(runCtx internalio.RunContext) (step60ArtifactSnapshot, error) {
	pairwisePath, err := runCtx.ResolveRunRelative("60/pairwise.jsonl")
	if err != nil {
		return step60ArtifactSnapshot{}, err
	}
	pairwiseRows, err := internalio.ReadJSONL[contracts.PairwiseEntry](pairwisePath)
	if err != nil {
		return step60ArtifactSnapshot{}, err
	}

	scoresPath, err := runCtx.ResolveRunRelative("60/scores-B.jsonl")
	if err != nil {
		return step60ArtifactSnapshot{}, err
	}
	scoreRows, err := internalio.ReadJSONL[contracts.ScoreEntry](scoresPath)
	if err != nil {
		return step60ArtifactSnapshot{}, err
	}

	compliancePath, err := runCtx.ResolveRunRelative("60/compliance-B.jsonl")
	if err != nil {
		return step60ArtifactSnapshot{}, err
	}
	complianceRows, err := internalio.ReadJSONL[contracts.ComplianceEntry](compliancePath)
	if err != nil {
		return step60ArtifactSnapshot{}, err
	}

	return step60ArtifactSnapshot{Scores: scoreRows, Compliance: complianceRows, Pairwise: pairwiseRows}, nil
}

func loadVerifiedStep60Artifacts(runCtx internalio.RunContext, pkg *contracts.TaskPackage) (step60ArtifactSnapshot, error) {
	markerPath, err := runCtx.ResolveRunRelative("60/done.marker")
	if err != nil {
		return step60ArtifactSnapshot{}, err
	}
	marker, err := internalio.ReadJSON[contracts.Step60DoneMarker](markerPath)
	if err != nil {
		return step60ArtifactSnapshot{}, fmt.Errorf("step70: read step60 done marker: %w", err)
	}
	if err := marker.Validate(); err != nil {
		return step60ArtifactSnapshot{}, fmt.Errorf("step70: validate step60 done marker: %w", err)
	}
	artifacts, err := loadStep60Artifacts(runCtx)
	if err != nil {
		return step60ArtifactSnapshot{}, err
	}
	if err := verifyStep60ArtifactSnapshot(marker, artifacts); err != nil {
		return step60ArtifactSnapshot{}, err
	}
	if err := verifyStep60InputSnapshot(runCtx, pkg, marker); err != nil {
		return step60ArtifactSnapshot{}, err
	}
	return artifacts, nil
}

func verifyStep60ArtifactSnapshot(marker contracts.Step60DoneMarker, artifacts step60ArtifactSnapshot) error {
	if !slices.Equal(marker.Dimensions, step70CanonicalDimensions) {
		return errors.New("step70: step60 done marker dimensions do not match current dimensions")
	}
	scoreCount, scoreHash, err := step70FinalScoresState(artifacts.Scores)
	if err != nil {
		return fmt.Errorf("step70: hash step60 scores: %w", err)
	}
	complianceCount, complianceHash, err := step70FinalComplianceState(artifacts.Compliance)
	if err != nil {
		return fmt.Errorf("step70: hash step60 compliance: %w", err)
	}
	pairwiseCount, pairwiseHash, err := step70FinalPairwiseState(artifacts.Pairwise)
	if err != nil {
		return fmt.Errorf("step70: hash step60 pairwise: %w", err)
	}
	if marker.ExpectedCounts.Scores != int64(scoreCount) ||
		marker.ExpectedCounts.Compliance != int64(complianceCount) ||
		marker.ExpectedCounts.Pairwise != int64(pairwiseCount) ||
		marker.ContentHashes.ScoresFinal != scoreHash ||
		marker.ContentHashes.ComplianceFinal != complianceHash ||
		marker.ContentHashes.PairwiseFinal != pairwiseHash {
		return errors.New("step70: step60 done marker does not match step60 artifacts")
	}
	if !step70MarkerAgentsMatchArtifacts(marker, artifacts) {
		return errors.New("step70: step60 done marker completed agents do not match step60 artifacts")
	}
	return nil
}

func verifyStep60InputSnapshot(runCtx internalio.RunContext, pkg *contracts.TaskPackage, marker contracts.Step60DoneMarker) error {
	inputHashes, completedAgents, err := currentStep60InputHashes(runCtx, pkg)
	if err != nil {
		return err
	}
	if !slices.Equal(marker.CompletedAgents, completedAgents) {
		return errors.New("step70: step60 done marker completed agents do not match current scorable agents")
	}
	if marker.InputHashes != inputHashes {
		return errors.New("step70: step60 done marker input hashes do not match current step60 inputs")
	}
	scoresRawHash, err := step70ReducedRawScoresHash(runCtx)
	if err != nil {
		return fmt.Errorf("step70: hash step60 raw scores: %w", err)
	}
	complianceRawHash, err := step70ReducedRawComplianceHash(runCtx)
	if err != nil {
		return fmt.Errorf("step70: hash step60 raw compliance: %w", err)
	}
	if marker.RawHashes.ScoresRaw != scoresRawHash || marker.RawHashes.ComplianceRaw != complianceRawHash {
		return errors.New("step70: step60 done marker raw hashes do not match step60 raw artifacts")
	}
	return nil
}

func step70MarkerAgentsMatchArtifacts(marker contracts.Step60DoneMarker, artifacts step60ArtifactSnapshot) bool {
	expected := append([]contracts.AgentID(nil), marker.CompletedAgents...)
	sort.Slice(expected, func(i, j int) bool { return expected[i] < expected[j] })
	scoreAgents := step70AgentsFromScores(artifacts.Scores)
	pairwiseAgents := step70AgentsFromPairwise(artifacts.Pairwise)
	if !slices.Equal(expected, scoreAgents) || !slices.Equal(expected, pairwiseAgents) {
		return false
	}
	complianceAgents := step70AgentsFromCompliance(artifacts.Compliance)
	if marker.ExpectedCounts.Compliance == 0 {
		return len(complianceAgents) == 0
	}
	return slices.Equal(expected, complianceAgents)
}

func step70AgentsFromScores(rows []contracts.ScoreEntry) []contracts.AgentID {
	seen := map[contracts.AgentID]struct{}{}
	for _, row := range scorecore.CollapseFinalScores(rows) {
		seen[row.Agent] = struct{}{}
	}
	return step70SortedAgents(seen)
}

func step70AgentsFromCompliance(rows []contracts.ComplianceEntry) []contracts.AgentID {
	seen := map[contracts.AgentID]struct{}{}
	for _, row := range scorecore.CollapseFinalCompliance(rows) {
		seen[row.Agent] = struct{}{}
	}
	return step70SortedAgents(seen)
}

func step70AgentsFromPairwise(rows []contracts.PairwiseEntry) []contracts.AgentID {
	seen := map[contracts.AgentID]struct{}{}
	for _, row := range rows {
		seen[row.AgentA] = struct{}{}
	}
	return step70SortedAgents(seen)
}

func step70SortedAgents(seen map[contracts.AgentID]struct{}) []contracts.AgentID {
	agents := make([]contracts.AgentID, 0, len(seen))
	for agent := range seen {
		agents = append(agents, agent)
	}
	sort.Slice(agents, func(i, j int) bool { return agents[i] < agents[j] })
	return agents
}

func currentStep60InputHashes(runCtx internalio.RunContext, pkg *contracts.TaskPackage) (contracts.Step60DoneInputHashes, []contracts.AgentID, error) {
	if pkg == nil {
		return contracts.Step60DoneInputHashes{}, nil, errors.New("step70: task package is required to verify step60 inputs")
	}
	pass1Scores, err := step70Pass1ScoresState(runCtx)
	if err != nil {
		return contracts.Step60DoneInputHashes{}, nil, err
	}
	pass1ComplianceRows, pass1ComplianceRules, err := step70Pass1ComplianceState(runCtx)
	if err != nil {
		return contracts.Step60DoneInputHashes{}, nil, err
	}
	pass2OutputHashes, completedAgents, err := step70Pass2OutputHashes(runCtx, pkg)
	if err != nil {
		return contracts.Step60DoneInputHashes{}, nil, err
	}
	candidateRules, err := step70LoadCandidateRules(runCtx)
	if err != nil {
		return contracts.Step60DoneInputHashes{}, nil, err
	}
	activeRules, fallbackRules, err := step70ComplianceRuleSources(runCtx)
	if err != nil {
		return contracts.Step60DoneInputHashes{}, nil, err
	}
	expectedComplianceByAgent := step70ExpectedComplianceRuleIDsByAgent(completedAgents, pass1ComplianceRules, activeRules, fallbackRules, candidateRules)
	inputHashes, err := step60contract.InputHashes(pass1Scores, pass1ComplianceRows, pass2OutputHashes, candidateRules, expectedComplianceByAgent)
	if err != nil {
		return contracts.Step60DoneInputHashes{}, nil, err
	}
	return inputHashes, completedAgents, nil
}

func step70Pass1ScoresState(runCtx internalio.RunContext) ([]contracts.ScoreEntry, error) {
	path, err := runCtx.ResolveRunRelative("30/scores-A.jsonl")
	if err != nil {
		return nil, fmt.Errorf("step70: resolve pass1 scores: %w", err)
	}
	rows, err := internalio.ReadJSONL[contracts.ScoreEntry](path)
	if err != nil {
		return nil, fmt.Errorf("step70: read pass1 scores: %w", err)
	}
	return scorecore.CollapseFinalScores(rows), nil
}

func step70Pass1ComplianceState(runCtx internalio.RunContext) ([]contracts.ComplianceEntry, map[contracts.AgentID]map[string]struct{}, error) {
	path, err := runCtx.ResolveRunRelative("30/compliance-A.jsonl")
	if err != nil {
		return nil, nil, fmt.Errorf("step70: resolve pass1 compliance: %w", err)
	}
	rows, err := internalio.ReadJSONL[contracts.ComplianceEntry](path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, map[contracts.AgentID]map[string]struct{}{}, nil
		}
		return nil, nil, fmt.Errorf("step70: read pass1 compliance: %w", err)
	}
	collapsed := scorecore.CollapseFinalCompliance(rows)
	sort.Slice(collapsed, func(i, j int) bool {
		if collapsed[i].Agent != collapsed[j].Agent {
			return collapsed[i].Agent < collapsed[j].Agent
		}
		return collapsed[i].RuleID < collapsed[j].RuleID
	})
	byAgent := make(map[contracts.AgentID]map[string]struct{}, len(collapsed))
	for _, row := range collapsed {
		if _, ok := byAgent[row.Agent]; !ok {
			byAgent[row.Agent] = map[string]struct{}{}
		}
		byAgent[row.Agent][row.RuleID] = struct{}{}
	}
	return collapsed, byAgent, nil
}

func step70Pass2OutputHashes(runCtx internalio.RunContext, pkg *contracts.TaskPackage) (map[contracts.AgentID]string, []contracts.AgentID, error) {
	agents := step70TaskPackageAgents(pkg, 2)
	hashes := make(map[contracts.AgentID]string, len(agents))
	completedAgents := make([]contracts.AgentID, 0, len(agents))
	for _, agent := range agents {
		if _, err := internalio.LoadScorableManifest(runCtx, 1, agent); err != nil {
			if errors.Is(err, internalio.ErrNotScorable) || os.IsNotExist(err) {
				pass2Manifest, pass2Err := internalio.LoadScorableManifest(runCtx, 2, agent)
				switch {
				case errors.Is(pass2Err, internalio.ErrNotScorable):
					continue
				case os.IsNotExist(pass2Err):
					continue
				case pass2Err != nil:
					return nil, nil, fmt.Errorf("step70: load pass2 manifest for agent=%s: %w", agent, pass2Err)
				case pass2Manifest != nil:
					return nil, nil, fmt.Errorf("step70: pass2 scorable agent missing pass1 scorable manifest: agent=%s: %w", agent, err)
				default:
					continue
				}
			}
			return nil, nil, fmt.Errorf("step70: load pass1 scorable manifest for agent=%s: %w", agent, err)
		}
		manifest, err := internalio.LoadScorableManifest(runCtx, 2, agent)
		if errors.Is(err, internalio.ErrNotScorable) {
			continue
		}
		if err != nil {
			return nil, nil, fmt.Errorf("step70: load pass2 manifest for agent=%s: %w", agent, err)
		}
		outputPath, ok, err := step70ResolveExistingRunArtifact(runCtx, manifest.DiffPath)
		if err != nil {
			return nil, nil, fmt.Errorf("step70: resolve pass2 diff path for agent=%s: %w", agent, err)
		}
		if !ok {
			return nil, nil, fmt.Errorf("step70: missing declared pass2 diff artifact for agent=%s: %s", agent, manifest.DiffPath)
		}
		hash, err := step70FileSHA256(outputPath)
		if err != nil {
			return nil, nil, fmt.Errorf("step70: hash pass2 diff for agent=%s: %w", agent, err)
		}
		hashes[agent] = hash
		completedAgents = append(completedAgents, agent)
	}
	if len(completedAgents) == 0 {
		return nil, nil, errors.New("step70: no scorable pass2 agents found while verifying step60 inputs")
	}
	return hashes, completedAgents, nil
}

func step70TaskPackageAgents(pkg *contracts.TaskPackage, pass int) []contracts.AgentID {
	agents := make([]contracts.AgentID, 0, len(pkg.Worktrees)/2)
	for _, worktree := range pkg.Worktrees {
		if worktree.Pass == pass {
			agents = append(agents, worktree.Agent)
		}
	}
	sort.Slice(agents, func(i, j int) bool { return agents[i] < agents[j] })
	return agents
}

func step70ResolveExistingRunArtifact(runCtx internalio.RunContext, relativePath string) (string, bool, error) {
	resolvedPath, err := runCtx.ResolveRunRelative(relativePath)
	if err != nil {
		return "", false, err
	}
	if _, err := os.Stat(resolvedPath); err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	return resolvedPath, true, nil
}

func step70LoadCandidateRules(runCtx internalio.RunContext) ([]judges.CandidateRule, error) {
	candidatesPath, err := runCtx.ResolveRunRelative("40/candidates.json")
	if err != nil {
		return nil, fmt.Errorf("step70: resolve candidates path: %w", err)
	}
	if _, err := os.Stat(candidatesPath); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("step70: stat candidates: %w", err)
	}
	payloads, err := candidaterules.LoadRulePayloads(candidatesPath)
	if err != nil {
		return nil, fmt.Errorf("step70: load candidate rules: %w", err)
	}
	return candidaterules.ToJudgeRules(payloads), nil
}

func step70ComplianceRuleSources(runCtx internalio.RunContext) ([]string, []string, error) {
	rubricPath, err := judges.ResolveRunRubricPath(runCtx)
	if err != nil {
		return nil, nil, fmt.Errorf("step70: resolve rubric path: %w", err)
	}
	activeRules, err := judges.ActiveComplianceRuleIDs(rubricPath)
	if err != nil {
		return nil, nil, fmt.Errorf("step70: load active compliance rules: %w", err)
	}
	fallbackRules, err := judges.ExpectedComplianceRuleIDs(rubricPath)
	if err != nil {
		return nil, nil, fmt.Errorf("step70: load expected compliance rules: %w", err)
	}
	return activeRules, fallbackRules, nil
}

func step70ExpectedComplianceRuleIDsByAgent(
	agents []contracts.AgentID,
	pass1Rules map[contracts.AgentID]map[string]struct{},
	activeRules []string,
	fallbackRules []string,
	candidateRules []judges.CandidateRule,
) map[contracts.AgentID]map[string]struct{} {
	return step60contract.ExpectedComplianceRuleIDsByAgent(agents, pass1Rules, activeRules, fallbackRules, candidateRules)
}

func step70ReducedRawScoresHash(runCtx internalio.RunContext) (string, error) {
	path, err := runCtx.ResolveRunRelative("60/scores-B-raw.jsonl")
	if err != nil {
		return "", err
	}
	return step60contract.HashReducedRawScoresFile(runCtx, path)
}

func step70ReducedRawComplianceHash(runCtx internalio.RunContext) (string, error) {
	path, err := runCtx.ResolveRunRelative("60/compliance-B-raw.jsonl")
	if err != nil {
		return "", err
	}
	return step60contract.HashReducedRawComplianceFile(runCtx, path)
}

func step70FileSHA256(path string) (string, error) {
	return step60contract.FileSHA256(path)
}

func step70FinalScoresState(rows []contracts.ScoreEntry) (int, string, error) {
	return step60contract.FinalScoresState(rows)
}

func step70FinalComplianceState(rows []contracts.ComplianceEntry) (int, string, error) {
	return step60contract.FinalComplianceState(rows)
}

func step70FinalPairwiseState(rows []contracts.PairwiseEntry) (int, string, error) {
	return step60contract.FinalPairwiseState(rows)
}
