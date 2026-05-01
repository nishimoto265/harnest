package step40_classify

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/lessons"
	"github.com/nishimoto265/auto-improve/internal/steps/scorecore"
)

const (
	candidatesJSONPath       = "40/candidates.json"
	classificationJSONLPath  = "40/classification.jsonl"
	experimentLessonsDirPath = "40/experiment/lessons"
	experimentChecklistPath  = "40/experiment/checklist.md"
	scoresPath               = "30/scores-A.jsonl"
	compliancePath           = "30/compliance-A.jsonl"
	issuesPath               = "30/issues-A.jsonl"
)

var ErrTaskPackageRequired = errors.New("step40_classify: task package is required")

type Config struct {
	IO           internalio.RunContext
	RegistryPath string
	TaskPackage  *contracts.TaskPackage
	Now          func() time.Time
}

type builtCandidate struct {
	Candidate      contracts.Candidate
	Body           string
	Lesson         lessons.Lesson
	Classification contracts.ClassificationEntry
}

func Run(ctx context.Context, cfg Config) (*contracts.Candidates, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if valid, err := step30Ready(cfg.IO, cfg.TaskPackage); err != nil {
		return nil, err
	} else if !valid {
		return nil, errors.New("step40_classify: step30 done.marker is missing or invalid")
	}

	scores, err := readJSONLAt[contracts.ScoreEntry](cfg.IO, scoresPath)
	if err != nil {
		return nil, err
	}
	compliance, err := readJSONLAt[contracts.ComplianceEntry](cfg.IO, compliancePath)
	if err != nil {
		return nil, err
	}
	issues, err := readOptionalJSONLAt[contracts.IssueEntry](cfg.IO, issuesPath)
	if err != nil {
		return nil, err
	}
	registry, err := internalio.RegistryEntries(cfg.registryPath())
	if err != nil {
		return nil, err
	}

	createdAt := cfg.now()
	built, err := buildCandidates(cfg.IO, createdAt, scores, compliance, issues, registry, filepath.Dir(cfg.registryPath()))
	if err != nil {
		return nil, err
	}
	items := make([]contracts.Candidate, 0, len(built))
	classifications := make([]contracts.ClassificationEntry, 0, len(built))
	for _, item := range built {
		items = append(items, item.Candidate)
		classifications = append(classifications, item.Classification)
	}

	if err := writeCandidateBodies(cfg.IO, built); err != nil {
		return nil, err
	}
	if err := writeExperimentChecklist(cfg.IO, built); err != nil {
		return nil, err
	}
	if err := writeClassificationJSONL(cfg.IO, classifications); err != nil {
		return nil, err
	}

	candidates := &contracts.Candidates{
		SchemaVersion:  "1",
		RunID:          cfg.IO.RunID,
		Candidates:     items,
		CandidatesHash: contracts.CanonicalCandidatesHash(items),
		CreatedAt:      createdAt,
	}
	if err := candidates.Validate(); err != nil {
		return nil, err
	}

	candidatesPath, err := cfg.IO.ResolveRunRelative(candidatesJSONPath)
	if err != nil {
		return nil, err
	}
	if err := internalio.WriteJSONAtomic(candidatesPath, candidates); err != nil {
		return nil, err
	}
	return candidates, nil
}

func (cfg Config) validate() error {
	if cfg.TaskPackage == nil {
		return ErrTaskPackageRequired
	}
	if err := cfg.TaskPackage.Validate(); err != nil {
		return err
	}
	if cfg.TaskPackage.RunID != cfg.IO.RunID {
		return fmt.Errorf("step40_classify: task package run_id mismatch: task_package=%s io=%s", cfg.TaskPackage.RunID, cfg.IO.RunID)
	}
	return contracts.EnsureCleanAbsolutePath(cfg.registryPath())
}

func (cfg Config) now() time.Time {
	if cfg.Now == nil {
		return time.Now().UTC()
	}
	return cfg.Now().UTC()
}

func (cfg Config) registryPath() string {
	if cfg.RegistryPath != "" {
		return cfg.RegistryPath
	}
	return cfg.IO.RulesRegistryPath()
}

func readJSONLAt[T any](runIO internalio.RunContext, rel string) ([]T, error) {
	path, err := runIO.ResolveRunRelative(rel)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("step40_classify: missing step30 artifact: %s", rel)
		}
		return nil, err
	}
	return internalio.ReadJSONL[T](path)
}

func readOptionalJSONLAt[T any](runIO internalio.RunContext, rel string) ([]T, error) {
	path, err := runIO.ResolveRunRelative(rel)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return internalio.ReadJSONL[T](path)
}

func step30Ready(runIO internalio.RunContext, pkg *contracts.TaskPackage) (bool, error) {
	markerPath, err := runIO.ResolveRunRelative("30/done.marker")
	if err != nil {
		return false, err
	}
	marker, err := internalio.ReadJSON[contracts.Step30DoneMarker](markerPath)
	if err != nil {
		return false, nil
	}
	if err := marker.Validate(); err != nil {
		return false, nil
	}
	expectedAgents, known, err := currentPass1ScorableAgents(runIO, pkg)
	if err != nil {
		return false, err
	}
	if known && !slices.Equal(marker.CompletedAgents, expectedAgents) {
		return false, nil
	}
	scoreFinal, err := runIO.ResolveRunRelative(scoresPath)
	if err != nil {
		return false, err
	}
	complianceFinal, err := runIO.ResolveRunRelative(compliancePath)
	if err != nil {
		return false, err
	}
	scoreRaw, err := runIO.ResolveRunRelative("30/scores-A-raw.jsonl")
	if err != nil {
		return false, err
	}
	complianceRaw, err := runIO.ResolveRunRelative("30/compliance-A-raw.jsonl")
	if err != nil {
		return false, err
	}
	return scorecore.VerifyStep30DoneMarker(runIO, scorecore.Step30MarkerPaths{
		ScoreFinal:      scoreFinal,
		ComplianceFinal: complianceFinal,
		ScoreRaw:        scoreRaw,
		ComplianceRaw:   complianceRaw,
	})
}

func currentPass1ScorableAgents(runIO internalio.RunContext, pkg *contracts.TaskPackage) ([]contracts.AgentID, bool, error) {
	if pkg == nil {
		return nil, false, ErrTaskPackageRequired
	}
	agents := make([]contracts.AgentID, 0, len(pkg.Worktrees)/2)
	seen := make(map[contracts.AgentID]struct{}, len(pkg.Worktrees))
	pass1Agents := make(map[contracts.AgentID]struct{}, len(pkg.Worktrees)/2)
	manifestCount := 0
	for _, wt := range pkg.Worktrees {
		if wt.Pass != 1 {
			continue
		}
		if _, dup := pass1Agents[wt.Agent]; dup {
			continue
		}
		pass1Agents[wt.Agent] = struct{}{}
		if _, dup := seen[wt.Agent]; dup {
			continue
		}
		manifestPath, err := runIO.ManifestPath(1, wt.Agent)
		if err != nil {
			return nil, false, err
		}
		if _, err := os.Stat(manifestPath); err == nil {
			manifestCount++
		} else if err != nil && !os.IsNotExist(err) {
			return nil, false, fmt.Errorf("step40_classify: stat pass1 manifest for agent=%s: %w", wt.Agent, err)
		}
		manifest, err := internalio.LoadScorableManifest(runIO, 1, wt.Agent)
		if err != nil {
			if errors.Is(err, internalio.ErrNotScorable) || os.IsNotExist(err) {
				continue
			}
			return nil, false, fmt.Errorf("step40_classify: load pass1 manifest for agent=%s: %w", wt.Agent, err)
		}
		if manifest == nil {
			continue
		}
		seen[wt.Agent] = struct{}{}
		agents = append(agents, wt.Agent)
	}
	if len(pass1Agents) > 0 && manifestCount == 0 {
		return nil, false, errors.New("step40_classify: pass1 worktrees exist but no pass1 manifests are resolvable")
	}
	sort.Slice(agents, func(i, j int) bool { return agents[i] < agents[j] })
	return agents, manifestCount > 0, nil
}
