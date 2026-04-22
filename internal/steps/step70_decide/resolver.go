package step70_decide

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/registryview"
	"github.com/nishimoto265/auto-improve/internal/steps/scorecore"
)

// maxStep70SidecarBytes caps individual rule sidecar reads when staging for
// promotion. 8 MiB is an upper bound on what a prompt-ready rule could ever be.
const maxStep70SidecarBytes = 8 << 20

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

	manifest, err := internalio.LoadScorableManifest(runCtx, 2, winningAgent)
	if err != nil {
		return Target{}, false, err
	}
	idempotencyKey := contracts.ComputeAdoptIdempotencyKey(string(runCtx.RunID), manifest.HeadSHA, "", candidates.CandidatesHash)

	registryLines, err := readRegistryLines(runCtx.RulesRegistryPath())
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

func (r FilesystemResolver) buildRegistryEntry(runCtx internalio.RunContext, candidate contracts.Candidate, registryLines []registryLine, idempotencyKey string, at time.Time) (contracts.RuleRegistryEntry, error) {
	ruleID := candidate.TargetRuleID
	if candidate.Kind == contracts.CandidateKindNew {
		ruleID = "r-" + candidate.CandidateID
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
	body, err := internalio.ReadValidatedRegularFile(srcPath, maxStep70SidecarBytes)
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
