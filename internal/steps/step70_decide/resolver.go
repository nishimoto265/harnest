package step70_decide

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/policyrepo"
	"github.com/nishimoto265/auto-improve/internal/registryview"
	"github.com/nishimoto265/auto-improve/internal/steps/scorecore"
)

const minimumPromotionDeltaTenths = 30

var promotionCriticalDimensions = map[contracts.Dimension]struct{}{
	contracts.DimensionFidelity:    {},
	contracts.DimensionCorrectness: {},
	contracts.DimensionDiscipline:  {},
}

// FilesystemResolver is the production TargetResolver used by the orchestrator.
// It reads step40/50/60 artifacts to choose the pass2 candidate that cleared
// the pairwise gate and had the best pass2 score profile.
type FilesystemResolver struct {
	RepoDir string
	Now     func() time.Time
}

func (r FilesystemResolver) Resolve(runCtx internalio.RunContext, pkg *contracts.TaskPackage, candidates *contracts.Candidates) (Target, bool, error) {
	if pkg == nil || candidates == nil {
		return Target{}, false, errors.New("step70: resolver requires task_package and candidates")
	}
	if len(candidates.Candidates) == 0 {
		return Target{}, false, nil
	}
	now := r.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}

	winningAgent, ok, err := resolveWinningAgent(runCtx)
	if err != nil {
		return Target{}, false, err
	}
	if !ok {
		return Target{}, false, nil
	}
	if ok, err := promotionGatePassed(runCtx, winningAgent, candidates); err != nil {
		return Target{}, false, err
	} else if !ok {
		return Target{}, false, nil
	}

	manifest, err := internalio.LoadScorableManifest(runCtx, 2, winningAgent)
	if err != nil {
		return Target{}, false, err
	}
	idempotencyKey := contracts.ComputeAdoptIdempotencyKey(string(runCtx.RunID), manifest.HeadSHA, "", candidates.CandidatesHash)

	registryPath, err := policyrepo.RegistryPathForRun(runCtx)
	if err != nil {
		return Target{}, false, err
	}
	registryLines, err := readRegistryLines(registryPath)
	if err != nil {
		return Target{}, false, err
	}
	entries := make([]contracts.RuleRegistryEntry, 0, len(candidates.Candidates))
	for _, candidate := range candidates.Candidates {
		switch candidate.Kind {
		case contracts.CandidateKindDuplicate:
			continue
		case contracts.CandidateKindNew, contracts.CandidateKindUpdate:
			entry, err := r.buildRegistryEntry(runCtx, candidate, registryLines, idempotencyKey, now())
			if err != nil {
				return Target{}, false, err
			}
			entries = append(entries, entry)
		default:
			return Target{}, false, fmt.Errorf("step70: unsupported candidate kind=%q", candidate.Kind)
		}
	}
	if len(entries) == 0 {
		return Target{}, false, nil
	}

	return Target{
		BestBranch:    pkg.BestBranch,
		BestShaBefore: "",
		TargetSHA:     manifest.HeadSHA,
		RulesToAppend: entries,
	}, true, nil
}

func resolveWinningAgent(runCtx internalio.RunContext) (contracts.AgentID, bool, error) {
	pairwisePath, err := runCtx.ResolveRunRelative("60/pairwise.jsonl")
	if err != nil {
		return "", false, err
	}
	pairwiseRows, err := internalio.ReadJSONL[contracts.PairwiseEntry](pairwisePath)
	if err != nil {
		return "", false, err
	}
	pairwise := internalio.CollapseByKey(pairwiseRows, func(entry contracts.PairwiseEntry) [2]contracts.AgentID {
		return [2]contracts.AgentID{entry.AgentA, entry.AgentB}
	})
	if len(pairwise) == 0 {
		return "", false, nil
	}

	scoresPath, err := runCtx.ResolveRunRelative("60/scores-B.jsonl")
	if err != nil {
		return "", false, err
	}
	scoreRows, err := internalio.ReadJSONL[contracts.ScoreEntry](scoresPath)
	if err != nil {
		return "", false, err
	}
	scores := scorecore.CollapseFinalScores(scoreRows)
	type scoreSummary struct {
		agent contracts.AgentID
		sum   int
		count int
	}
	summaries := make(map[contracts.AgentID]scoreSummary)
	for _, score := range scores {
		s := summaries[score.Agent]
		s.agent = score.Agent
		s.sum += score.Score
		s.count++
		summaries[score.Agent] = s
	}

	best := scoreSummary{}
	bestSet := false
	for _, entry := range pairwise {
		if entry.Winner != contracts.PairwiseWinnerB {
			continue
		}
		s, ok := summaries[entry.AgentB]
		if !ok || s.count == 0 {
			continue
		}
		if !bestSet || s.sum*best.count > best.sum*s.count || (s.sum*best.count == best.sum*s.count && string(s.agent) < string(best.agent)) {
			best = s
			bestSet = true
		}
	}
	if !bestSet {
		return "", false, nil
	}
	return best.agent, true, nil
}

func promotionGatePassed(runCtx internalio.RunContext, agent contracts.AgentID, candidates *contracts.Candidates) (bool, error) {
	if ok, err := hasCompliantCandidateEvidence(runCtx, agent, candidates); err != nil || !ok {
		return ok, err
	}
	pass1Scores, err := loadCollapsedScores(runCtx, "30/scores-A.jsonl")
	if err != nil {
		return false, err
	}
	pass2Scores, err := loadCollapsedScores(runCtx, "60/scores-B.jsonl")
	if err != nil {
		return false, err
	}
	pass1 := pass1Scores[agent]
	pass2 := pass2Scores[agent]
	if !completeScoreSet(pass1) || !completeScoreSet(pass2) {
		return false, nil
	}
	if averageTenths(pass2)-averageTenths(pass1) < minimumPromotionDeltaTenths {
		return false, nil
	}
	for dimension := range promotionCriticalDimensions {
		if pass2[dimension].Score < pass1[dimension].Score {
			return false, nil
		}
	}
	return true, nil
}

func hasCompliantCandidateEvidence(runCtx internalio.RunContext, agent contracts.AgentID, candidates *contracts.Candidates) (bool, error) {
	required := requiredCandidateComplianceRuleIDs(candidates)
	if len(required) == 0 {
		return true, nil
	}
	path, err := runCtx.ResolveRunRelative("60/compliance-B.jsonl")
	if err != nil {
		return false, err
	}
	rows, err := internalio.ReadJSONL[contracts.ComplianceEntry](path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	collapsed := scorecore.CollapseFinalCompliance(rows)
	byRule := make(map[string]contracts.ComplianceEntry, len(collapsed))
	for _, row := range collapsed {
		if row.Agent == agent {
			byRule[row.RuleID] = row
		}
	}
	for _, ruleID := range required {
		row, ok := byRule[ruleID]
		if !ok || row.Verdict != contracts.ComplianceVerdictCompliant {
			return false, nil
		}
	}
	return true, nil
}

func requiredCandidateComplianceRuleIDs(candidates *contracts.Candidates) []string {
	if candidates == nil {
		return nil
	}
	ruleIDs := make([]string, 0, len(candidates.Candidates))
	for _, candidate := range candidates.Candidates {
		switch candidate.Kind {
		case contracts.CandidateKindNew, contracts.CandidateKindUpdate:
			ruleIDs = append(ruleIDs, candidate.CandidateID)
		}
	}
	return ruleIDs
}

func loadCollapsedScores(runCtx internalio.RunContext, rel string) (map[contracts.AgentID]map[contracts.Dimension]contracts.ScoreEntry, error) {
	path, err := runCtx.ResolveRunRelative(rel)
	if err != nil {
		return nil, err
	}
	rows, err := internalio.ReadJSONL[contracts.ScoreEntry](path)
	if err != nil {
		return nil, err
	}
	collapsed := scorecore.CollapseFinalScores(rows)
	byAgent := make(map[contracts.AgentID]map[contracts.Dimension]contracts.ScoreEntry)
	for _, row := range collapsed {
		if _, ok := byAgent[row.Agent]; !ok {
			byAgent[row.Agent] = make(map[contracts.Dimension]contracts.ScoreEntry)
		}
		byAgent[row.Agent][row.Dimension] = row
	}
	return byAgent, nil
}

func completeScoreSet(scores map[contracts.Dimension]contracts.ScoreEntry) bool {
	if len(scores) != 5 {
		return false
	}
	for _, dimension := range []contracts.Dimension{
		contracts.DimensionFidelity,
		contracts.DimensionCorrectness,
		contracts.DimensionMaintainability,
		contracts.DimensionDiscipline,
		contracts.DimensionCommunication,
	} {
		if _, ok := scores[dimension]; !ok {
			return false
		}
	}
	return true
}

func averageTenths(scores map[contracts.Dimension]contracts.ScoreEntry) int {
	total := 0
	for _, score := range scores {
		total += score.Score
	}
	return total * 10 / len(scores)
}

func (r FilesystemResolver) buildRegistryEntry(runCtx internalio.RunContext, candidate contracts.Candidate, registryLines []registryLine, idempotencyKey string, at time.Time) (contracts.RuleRegistryEntry, error) {
	ruleID := candidate.TargetRuleID
	if candidate.Kind == contracts.CandidateKindNew {
		ruleID = generatedRuleID(candidate.CandidateID)
	}
	if ruleID == "" {
		return contracts.RuleRegistryEntry{}, fmt.Errorf("step70: missing rule_id for candidate %s", candidate.CandidateID)
	}
	if err := contracts.ValidateRuleID(ruleID); err != nil {
		return contracts.RuleRegistryEntry{}, err
	}

	rulePath := filepath.Join("rules", ruleID+".md")
	if err := contracts.ValidateRulePath(rulePath); err != nil {
		return contracts.RuleRegistryEntry{}, err
	}
	if err := materializeRuleSidecar(runCtx, candidate, rulePath); err != nil {
		return contracts.RuleRegistryEntry{}, err
	}

	switch candidate.Kind {
	case contracts.CandidateKindNew:
		entry := contracts.RuleRegistryAdded{
			Kind:           contracts.RegistryKindAdded,
			SchemaVersion:  "1",
			RuleID:         ruleID,
			RulePath:       rulePath,
			Sha256:         candidate.ProposedBodySha256,
			IdempotencyKey: idempotencyKey,
			VersionSeq:     1,
			PrevHash:       "",
			ByRunID:        runCtx.RunID,
			At:             at,
		}
		return contracts.RuleRegistryEntry{Kind: entry.Kind, Value: entry}, nil
	case contracts.CandidateKindUpdate:
		prevSha, err := latestRuleSha256(registryLines, ruleID)
		if err != nil {
			return contracts.RuleRegistryEntry{}, err
		}
		entry := contracts.RuleRegistryUpdated{
			Kind:           contracts.RegistryKindUpdated,
			SchemaVersion:  "1",
			RuleID:         ruleID,
			RulePath:       rulePath,
			Sha256:         candidate.ProposedBodySha256,
			PrevSha256:     prevSha,
			IdempotencyKey: idempotencyKey,
			VersionSeq:     1,
			PrevHash:       "",
			ByRunID:        runCtx.RunID,
			At:             at,
		}
		return contracts.RuleRegistryEntry{Kind: entry.Kind, Value: entry}, nil
	default:
		return contracts.RuleRegistryEntry{}, fmt.Errorf("step70: unsupported candidate kind=%q", candidate.Kind)
	}
}

func generatedRuleID(candidateID string) string {
	sum := sha256.Sum256([]byte(candidateID))
	return "r-" + hex.EncodeToString(sum[:])[:12]
}

func latestRuleSha256(lines []registryLine, ruleID string) (string, error) {
	entries := make([]contracts.RuleRegistryEntry, 0, len(lines))
	for _, line := range lines {
		entries = append(entries, line.Entry)
	}
	states, err := registryview.Build(entries)
	if err != nil {
		return "", err
	}
	state, ok := states[ruleID]
	if !ok || state.Sha256 == "" {
		return "", fmt.Errorf("step70: no prior rule content found for update rule_id=%s", ruleID)
	}
	return state.Sha256, nil
}

func materializeRuleSidecar(runCtx internalio.RunContext, candidate contracts.Candidate, rulePath string) error {
	if err := contracts.ValidateRulePath(rulePath); err != nil {
		return err
	}
	srcPath, err := runCtx.ResolveRunRelative(candidate.ProposedBodyPath)
	if err != nil {
		return err
	}
	body, err := internalio.OpenValidatedRegularFile(srcPath, runCtx.RunDir())
	if err != nil {
		return err
	}
	sum := sha256.Sum256(body)
	if got := hex.EncodeToString(sum[:]); got != candidate.ProposedBodySha256 {
		return fmt.Errorf("step70: candidate body sha256 mismatch: candidate_id=%s got=%s want=%s", candidate.CandidateID, got, candidate.ProposedBodySha256)
	}
	dstPath, err := stagedRuleSidecarPath(runCtx, rulePath)
	if err != nil {
		return err
	}
	// Persist the exact bytes hashed into candidate.ProposedBodySha256.
	return internalio.WriteAtomic(dstPath, body)
}

func stagedRuleSidecarPath(runCtx internalio.RunContext, rulePath string) (string, error) {
	if err := contracts.ValidateRulePath(rulePath); err != nil {
		return "", err
	}
	return runCtx.ResolveRunRelative(filepath.Join("staging", filepath.FromSlash(rulePath)))
}
