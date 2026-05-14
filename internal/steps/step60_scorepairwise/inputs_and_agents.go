package step60_scorepairwise

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/nishimoto265/harnest/internal/candidaterules"
	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/nishimoto265/harnest/internal/judges"
	"github.com/nishimoto265/harnest/internal/steps/scorecore"
)

func applyDefaults(in Input) (Input, error) {
	if in.TaskPackage == nil {
		return Input{}, errors.New("step60: task package is required")
	}
	if err := in.TaskPackage.Validate(); err != nil {
		return Input{}, err
	}
	if in.TaskPackage.RunID != in.IO.RunID {
		return Input{}, fmt.Errorf("step60: task package run_id mismatch: task_package=%s io=%s", in.TaskPackage.RunID, in.IO.RunID)
	}
	if in.Now == nil {
		in.Now = time.Now
	}
	if in.Primary == nil {
		in.Primary = judges.NewPrimaryStub()
	}
	if in.PairwiseMode == "" {
		in.PairwiseMode = judges.PairwiseModeBasic
	}
	switch in.PairwiseMode {
	case judges.PairwiseModeSingle, judges.PairwiseModeBasic, judges.PairwiseModeStrict:
	default:
		return Input{}, fmt.Errorf("step60: unsupported pairwise_mode=%q", in.PairwiseMode)
	}
	if in.PairwiseJudge == nil {
		in.PairwiseJudge = judges.NewScoreDerivedPairwiseJudge()
	}
	if in.PairwiseDecisionJudge == nil {
		in.PairwiseDecisionJudge = judges.NewScoreDerivedPairwiseDecisionJudge()
	}
	if in.RubricVersion == "" || in.PromptVersion == "" {
		versions, ok, err := inferPass1ScoringVersions(in.IO)
		if err != nil {
			return Input{}, err
		}
		if ok && in.RubricVersion == "" {
			in.RubricVersion = versions.RubricVersion
		}
		if ok && in.PromptVersion == "" {
			switch {
			case versions.PromptVersion == judges.PanelPromptVersion("phase0-stub", in.Primary, in.Secondary, in.Arbiter):
				in.PromptVersion = "phase0-stub"
			case versions.PromptVersion == judges.PanelPromptVersion(versions.PromptVersion, in.Primary, in.Secondary, in.Arbiter):
				in.PromptVersion = versions.PromptVersion
			}
		}
	}
	if in.RubricVersion == "" {
		in.RubricVersion = "default"
	}
	if in.PromptVersion == "" {
		in.PromptVersion = "phase0-stub"
	}
	if in.RubricPath == "" {
		rubricPath, err := judges.ResolveRunRubricPath(in.IO)
		if err != nil {
			return Input{}, err
		}
		in.RubricPath = rubricPath
	}
	if err := contracts.EnsureCleanAbsolutePath(in.RubricPath); err != nil {
		return Input{}, err
	}
	if in.CandidateRules == nil {
		candidateRules, err := loadCandidateRules(in.IO)
		if err != nil {
			return Input{}, err
		}
		in.CandidateRules = candidateRules
	}
	in.PromptVersion = judges.PanelPromptVersion(in.PromptVersion, in.Primary, in.Secondary, in.Arbiter)
	if in.PairwisePromptVersion == "" {
		in.PairwisePromptVersion = judges.PairwisePanelPromptVersion(in.PromptVersion, in.PairwiseMode, in.PairwiseJudge, in.PairwiseDecisionJudge)
	}
	return in, nil
}

func inferPass1ScoringVersions(runIO internalio.RunContext) (pass1ScoringVersions, bool, error) {
	scorePath, err := runIO.ResolveRunRelative("30/scores-A.jsonl")
	if err != nil {
		return pass1ScoringVersions{}, false, fmt.Errorf("step60: resolve pass1 scores path: %w", err)
	}
	scoreRows, err := internalio.ReadJSONL[contracts.ScoreEntry](scorePath)
	if err != nil {
		return pass1ScoringVersions{}, false, fmt.Errorf("step60: read pass1 scores for version inference: %w", err)
	}

	var versions pass1ScoringVersions
	for _, row := range scorecore.CollapseFinalScores(scoreRows) {
		next, err := collectPass1ScoringVersion(versions, row.RubricVersion, row.PromptVersion)
		if err != nil {
			return pass1ScoringVersions{}, false, fmt.Errorf("step60: infer pass1 score versions: %w", err)
		}
		versions = next
	}

	compliancePath, err := runIO.ResolveRunRelative("30/compliance-A.jsonl")
	if err != nil {
		return pass1ScoringVersions{}, false, fmt.Errorf("step60: resolve pass1 compliance path: %w", err)
	}
	complianceRows, err := internalio.ReadJSONL[contracts.ComplianceEntry](compliancePath)
	if err != nil {
		return pass1ScoringVersions{}, false, fmt.Errorf("step60: read pass1 compliance for version inference: %w", err)
	}
	for _, row := range scorecore.CollapseFinalCompliance(complianceRows) {
		next, err := collectPass1ScoringVersion(versions, row.RubricVersion, row.PromptVersion)
		if err != nil {
			return pass1ScoringVersions{}, false, fmt.Errorf("step60: infer pass1 compliance versions: %w", err)
		}
		versions = next
	}

	if versions.RubricVersion == "" || versions.PromptVersion == "" {
		return pass1ScoringVersions{}, false, nil
	}
	return versions, true, nil
}

func collectPass1ScoringVersion(current pass1ScoringVersions, rubricVersion, promptVersion string) (pass1ScoringVersions, error) {
	if rubricVersion == "" || promptVersion == "" {
		return pass1ScoringVersions{}, ErrPass1VersionMismatch
	}
	if current.RubricVersion == "" && current.PromptVersion == "" {
		return pass1ScoringVersions{RubricVersion: rubricVersion, PromptVersion: promptVersion}, nil
	}
	if current.RubricVersion != rubricVersion || current.PromptVersion != promptVersion {
		return pass1ScoringVersions{}, fmt.Errorf(
			"%w: mixed pass1 scoring versions: got rubric=%s prompt=%s want rubric=%s prompt=%s",
			ErrPass1VersionMismatch, rubricVersion, promptVersion, current.RubricVersion, current.PromptVersion,
		)
	}
	return current, nil
}

func resolveStep60Paths(runIO internalio.RunContext) (step60Paths, error) {
	lockPath, err := runIO.ResolveRunRelative("60/.step60.lock")
	if err != nil {
		return step60Paths{}, fmt.Errorf("step60: resolve lock path: %w", err)
	}
	donePath, err := runIO.ResolveRunRelative("60/done.marker")
	if err != nil {
		return step60Paths{}, fmt.Errorf("step60: resolve done marker path: %w", err)
	}
	rawReusePath, err := runIO.ResolveRunRelative("60/raw-reuse.marker")
	if err != nil {
		return step60Paths{}, fmt.Errorf("step60: resolve raw reuse marker path: %w", err)
	}
	scoresRawPath, err := runIO.ResolveRunRelative("60/scores-B-raw.jsonl")
	if err != nil {
		return step60Paths{}, fmt.Errorf("step60: resolve scores raw path: %w", err)
	}
	scoresFinalPath, err := runIO.ResolveRunRelative("60/scores-B.jsonl")
	if err != nil {
		return step60Paths{}, fmt.Errorf("step60: resolve scores final path: %w", err)
	}
	complianceRawPath, err := runIO.ResolveRunRelative("60/compliance-B-raw.jsonl")
	if err != nil {
		return step60Paths{}, fmt.Errorf("step60: resolve compliance raw path: %w", err)
	}
	complianceFinalPath, err := runIO.ResolveRunRelative("60/compliance-B.jsonl")
	if err != nil {
		return step60Paths{}, fmt.Errorf("step60: resolve compliance final path: %w", err)
	}
	pairwisePath, err := runIO.ResolveRunRelative("60/pairwise.jsonl")
	if err != nil {
		return step60Paths{}, fmt.Errorf("step60: resolve pairwise path: %w", err)
	}
	return step60Paths{
		Lock:            lockPath,
		Done:            donePath,
		RawReuse:        rawReusePath,
		ScoresRaw:       scoresRawPath,
		ScoresFinal:     scoresFinalPath,
		ComplianceRaw:   complianceRawPath,
		ComplianceFinal: complianceFinalPath,
		Pairwise:        pairwisePath,
	}, nil
}

func declaredScorableAgents(in Input) []contracts.AgentID {
	if len(in.ScorableAgents) > 0 {
		agents := append([]contracts.AgentID(nil), in.ScorableAgents...)
		sort.Slice(agents, func(i, j int) bool { return agents[i] < agents[j] })
		return agents
	}

	agents := make([]contracts.AgentID, 0, len(in.TaskPackage.Worktrees)/2)
	for _, worktree := range in.TaskPackage.Worktrees {
		if worktree.Pass == 2 {
			agents = append(agents, worktree.Agent)
		}
	}
	sort.Slice(agents, func(i, j int) bool { return agents[i] < agents[j] })
	return agents
}

func scorableAgentsFromRuns(runs []scorableAgentRun) []contracts.AgentID {
	agents := make([]contracts.AgentID, 0, len(runs))
	for _, run := range runs {
		agents = append(agents, run.Agent)
	}
	return agents
}

func loadCandidateRules(runIO internalio.RunContext) ([]judges.CandidateRule, error) {
	candidatesPath, err := runIO.ResolveRunRelative("40/candidates.json")
	if err != nil {
		return nil, fmt.Errorf("step60: resolve candidates path: %w", err)
	}
	if _, err := os.Stat(candidatesPath); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("step60: stat candidates: %w", err)
	}
	payloads, err := candidaterules.LoadRulePayloads(candidatesPath)
	if err != nil {
		return nil, fmt.Errorf("step60: load candidate rules: %w", err)
	}
	return candidaterules.ToJudgeRules(payloads), nil
}

func shouldSkipAgent(err error) bool {
	return errors.Is(err, internalio.ErrNotScorable)
}

func collectScorableAgentRuns(in Input, agents []contracts.AgentID, explicit bool) ([]scorableAgentRun, error) {
	runs := make([]scorableAgentRun, 0, len(agents))
	for _, agent := range agents {
		manifest, err := internalio.LoadScorableManifest(in.IO, 2, agent)
		if err != nil {
			if explicit && shouldSkipAgent(err) {
				return nil, fmt.Errorf("step60: declared scorable agent missing pass2 scorable manifest: agent=%s: %w", agent, err)
			}
			if explicit && os.IsNotExist(err) {
				return nil, fmt.Errorf("step60: declared scorable agent missing pass2 manifest: agent=%s: %w", agent, err)
			}
			if shouldSkipAgent(err) {
				continue
			}
			if os.IsNotExist(err) {
				if _, pass1Err := internalio.LoadScorableManifest(in.IO, 1, agent); pass1Err == nil {
					return nil, fmt.Errorf("step60: pass1 scorable agent missing matching pass2 manifest: agent=%s: %w", agent, err)
				} else if shouldSkipAgent(pass1Err) || os.IsNotExist(pass1Err) {
					continue
				} else {
					return nil, fmt.Errorf("step60: load pass1 scorable manifest for agent=%s: %w", agent, pass1Err)
				}
			}
			return nil, fmt.Errorf("step60: load pass2 manifest for agent=%s: %w", agent, err)
		}
		pass1Manifest, err := internalio.LoadScorableManifest(in.IO, 1, agent)
		if err != nil {
			if shouldSkipAgent(err) || os.IsNotExist(err) {
				return nil, fmt.Errorf("step60: pass2 scorable agent missing matching pass1 scorable manifest: agent=%s: %w", agent, err)
			}
			return nil, fmt.Errorf("step60: load pass1 scorable manifest for agent=%s: %w", agent, err)
		}
		pass1OutputPath, err := requireExistingManifestArtifact(in.IO, agent, pass1Manifest.DiffPath, "pass1 diff")
		if err != nil {
			return nil, err
		}
		outputPath, err := requireExistingManifestArtifact(in.IO, agent, manifest.DiffPath, "diff")
		if err != nil {
			return nil, err
		}
		if _, err := requireExistingManifestArtifact(in.IO, agent, manifest.SessionPath, "session"); err != nil {
			return nil, err
		}
		if _, err := requireExistingManifestArtifact(in.IO, agent, manifest.ChecklistPath, "checklist"); err != nil {
			return nil, err
		}
		pass1SnapshotPath, pass1OutputHash, err := snapshotAndHashPass1Diff(in.IO, agent, pass1OutputPath)
		if err != nil {
			return nil, fmt.Errorf("step60: snapshot pass1 diff agent=%s: %w", agent, err)
		}
		snapshotPath, outputHash, err := snapshotAndHashPass2Diff(in.IO, agent, outputPath)
		if err != nil {
			return nil, fmt.Errorf("step60: snapshot pass2 diff agent=%s: %w", agent, err)
		}
		runs = append(runs, scorableAgentRun{
			Agent:             agent,
			OutputSha256:      outputHash,
			Pass1OutputPath:   pass1SnapshotPath,
			Pass1OutputSha256: pass1OutputHash,
			JudgeInput: judges.JudgeInput{
				RunID:      in.TaskPackage.RunID,
				Pass:       2,
				Agent:      agent,
				OutputPath: snapshotPath,
				RubricPath: in.RubricPath,
			},
		})
	}
	if len(runs) == 0 {
		return nil, ErrNoScorablePass2Agents
	}
	return runs, nil
}

func requireExistingManifestArtifact(runIO internalio.RunContext, agent contracts.AgentID, relativePath, label string) (string, error) {
	resolvedPath, ok, err := resolveExistingManifestArtifact(runIO, relativePath)
	if err != nil {
		return "", fmt.Errorf("step60: resolve pass2 %s path for agent=%s: %w", label, agent, err)
	}
	if !ok {
		return "", fmt.Errorf("step60: missing declared pass2 %s artifact for agent=%s: %s", label, agent, relativePath)
	}
	return resolvedPath, nil
}

func resolveExistingManifestArtifact(runIO internalio.RunContext, relativePath string) (string, bool, error) {
	resolvedPath, err := runIO.ResolveRunRelative(relativePath)
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
