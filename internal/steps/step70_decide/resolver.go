package step70_decide

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
)

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
	if r.RepoDir == "" {
		return Target{}, false, errors.New("step70: resolver repo_dir is required")
	}
	now := r.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}

	bestShaBefore, err := resolveBestBranchSHA(r.RepoDir, pkg.BestBranch)
	if err != nil {
		return Target{}, false, err
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
	idempotencyKey := contracts.ComputeAdoptIdempotencyKey(string(runCtx.RunID), manifest.HeadSHA, bestShaBefore, candidates.CandidatesHash)

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
		BestShaBefore: bestShaBefore,
		TargetSHA:     manifest.HeadSHA,
		RulesToAppend: entries,
	}, true, nil
}

func resolveWinningAgent(runCtx internalio.RunContext) (contracts.AgentID, bool, error) {
	pairwisePath, err := runCtx.ResolveRunRelative("60/pairwise.jsonl")
	if err != nil {
		return "", false, err
	}
	pairwise, err := internalio.ReadJSONL[contracts.PairwiseEntry](pairwisePath)
	if err != nil {
		return "", false, err
	}
	if len(pairwise) == 0 {
		return "", false, nil
	}

	scoresPath, err := runCtx.ResolveRunRelative("60/scores-B.jsonl")
	if err != nil {
		return "", false, err
	}
	scores, err := internalio.ReadJSONL[contracts.ScoreEntry](scoresPath)
	if err != nil {
		return "", false, err
	}
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

func resolveBestBranchSHA(repoDir, branch string) (string, error) {
	candidates := []string{
		"refs/remotes/origin/" + branch,
		"refs/heads/" + branch,
		branch,
	}
	for _, ref := range candidates {
		out, err := exec.Command("git", "-C", repoDir, "rev-parse", "--verify", ref).Output()
		if err == nil {
			return strings.TrimSpace(string(out)), nil
		}
	}
	return "", fmt.Errorf("step70: resolver could not resolve best branch %q in %s", branch, repoDir)
}

func (r FilesystemResolver) buildRegistryEntry(runCtx internalio.RunContext, candidate contracts.Candidate, registryLines []registryLine, idempotencyKey string, at time.Time) (contracts.RuleRegistryEntry, error) {
	ruleID := candidate.TargetRuleID
	if candidate.Kind == contracts.CandidateKindNew {
		ruleID = "r-" + candidate.CandidateID
	}
	if ruleID == "" {
		return contracts.RuleRegistryEntry{}, fmt.Errorf("step70: missing rule_id for candidate %s", candidate.CandidateID)
	}

	rulePath := filepath.Join("rules", ruleID+".md")
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
	for i := len(lines) - 1; i >= 0; i-- {
		switch v := lines[i].Entry.Value.(type) {
		case contracts.RuleRegistryUpdated:
			if v.RuleID == ruleID {
				return v.Sha256, nil
			}
		case contracts.RuleRegistryAdded:
			if v.RuleID == ruleID {
				return v.Sha256, nil
			}
		}
	}
	return "", fmt.Errorf("step70: no prior rule content found for update rule_id=%s", ruleID)
}

func materializeRuleSidecar(runCtx internalio.RunContext, candidate contracts.Candidate, rulePath string) error {
	srcPath, err := runCtx.ResolveRunRelative(candidate.ProposedBodyPath)
	if err != nil {
		return err
	}
	body, err := os.ReadFile(srcPath)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(body)
	if got := hex.EncodeToString(sum[:]); got != candidate.ProposedBodySha256 {
		return fmt.Errorf("step70: candidate body sha256 mismatch: candidate_id=%s got=%s want=%s", candidate.CandidateID, got, candidate.ProposedBodySha256)
	}
	dstPath := filepath.Join(runCtx.RunsBase, rulePath)
	return internalio.WriteAtomic(dstPath, append(body, '\n'))
}
