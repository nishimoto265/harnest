package step70_decide

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/policyrepo"
	"github.com/nishimoto265/auto-improve/internal/steps/scorecore"
)

var step70CanonicalDimensions = []contracts.Dimension{
	contracts.DimensionFidelity,
	contracts.DimensionCorrectness,
	contracts.DimensionMaintainability,
	contracts.DimensionDiscipline,
	contracts.DimensionCommunication,
}

// FilesystemResolver is the production TargetResolver used by the orchestrator.
// It reads step40/50/60 artifacts to choose the pass2 candidate that cleared
// the pairwise gate and had the best pass2 score profile.
type FilesystemResolver struct {
	RepoDir string
	Now     func() time.Time
}

type step60ArtifactSnapshot struct {
	Scores     []contracts.ScoreEntry
	Compliance []contracts.ComplianceEntry
	Pairwise   []contracts.PairwiseEntry
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

	step60Artifacts, err := loadVerifiedStep60Artifacts(runCtx, pkg)
	if err != nil {
		return Target{}, false, err
	}
	winningAgents := rankedWinningAgentsFromArtifacts(step60Artifacts)
	if len(winningAgents) == 0 {
		return Target{}, false, nil
	}
	if hasDuplicateUpdateTarget(candidates.Candidates) {
		return Target{}, false, nil
	}
	var winningAgent contracts.AgentID
	for _, agent := range winningAgents {
		ok, err := promotionGatePassedWithArtifacts(runCtx, step60Artifacts, agent, candidates)
		if err != nil {
			return Target{}, false, err
		}
		if ok {
			winningAgent = agent
			break
		}
	}
	if winningAgent == "" {
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

func resolveWinningAgentFromArtifacts(artifacts step60ArtifactSnapshot) (contracts.AgentID, bool, error) {
	agents := rankedWinningAgentsFromArtifacts(artifacts)
	if len(agents) == 0 {
		return "", false, nil
	}
	return agents[0], true, nil
}

type winningAgentScoreSummary struct {
	agent contracts.AgentID
	sum   int
	count int
}

func rankedWinningAgentsFromArtifacts(artifacts step60ArtifactSnapshot) []contracts.AgentID {
	pairwise := internalio.CollapseByKey(artifacts.Pairwise, func(entry contracts.PairwiseEntry) [2]contracts.AgentID {
		return [2]contracts.AgentID{entry.AgentA, entry.AgentB}
	})
	if len(pairwise) == 0 {
		return nil
	}

	scores := scorecore.CollapseFinalScores(artifacts.Scores)
	summaries := make(map[contracts.AgentID]winningAgentScoreSummary)
	for _, score := range scores {
		s := summaries[score.Agent]
		s.agent = score.Agent
		s.sum += score.Score
		s.count++
		summaries[score.Agent] = s
	}

	winning := make(map[contracts.AgentID]struct{})
	for _, entry := range pairwise {
		if entry.Winner != contracts.PairwiseWinnerB {
			continue
		}
		winning[entry.AgentB] = struct{}{}
	}

	ranked := make([]winningAgentScoreSummary, 0, len(winning))
	for agent := range winning {
		s, ok := summaries[agent]
		if !ok || s.count == 0 {
			continue
		}
		ranked = append(ranked, s)
	}
	sort.Slice(ranked, func(i, j int) bool {
		left := ranked[i]
		right := ranked[j]
		leftProduct := left.sum * right.count
		rightProduct := right.sum * left.count
		if leftProduct == rightProduct {
			return string(left.agent) < string(right.agent)
		}
		return leftProduct > rightProduct
	})
	agents := make([]contracts.AgentID, 0, len(ranked))
	for _, summary := range ranked {
		agents = append(agents, summary.agent)
	}
	return agents
}
