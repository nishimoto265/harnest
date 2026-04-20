package stepio

import (
	"errors"
	"fmt"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/validation"
)

var (
	ErrStepScorableAgentPassMismatch     = errors.New("stepio: scorable_agents do not match TaskPackage.Worktrees for the requested pass")
	ErrStepResponseDuplicateResultAgent  = errors.New("stepio: response results must not repeat an agent")
	ErrStepResponseDuplicateRescueAgent  = errors.New("stepio: response rescue_exhausted must not repeat an agent")
	ErrStepResponseManifestRunIDMismatch = errors.New("stepio: response.run_id must equal manifest.run_id")
	ErrStepResponseManifestPassMismatch  = errors.New("stepio: response.pass must equal manifest.pass")
	ErrStepResponseManifestAgentMismatch = errors.New("stepio: result.agent must equal manifest.agent")
	ErrResponseRunIDMismatch             = errors.New("stepio: response.run_id must equal request.task_package.run_id")
	ErrAgentResultOverlap                = errors.New("stepio: response agent must not appear in both results and rescue_exhausted")
	ErrAgentCoverageMismatch             = errors.New("stepio: response agents must partition request.agents exactly")
	ErrRegistryPathNotAbsolute           = errors.New("stepio: registry_path must be an absolute path")
	ErrRegistryPathNotClean              = errors.New("stepio: registry_path must be a clean absolute path without . or .. elements")
	ErrRegistryPathBasename              = errors.New("stepio: registry_path basename must be rules-registry.jsonl")
)

func validateRegistryPath(path string) error {
	if err := contracts.EnsureCleanAbsolutePathWithBasename(path, "rules-registry.jsonl"); err != nil {
		switch {
		case errors.Is(err, contracts.ErrPathNotAbsolute):
			return fmt.Errorf("%w: registry_path=%q", ErrRegistryPathNotAbsolute, path)
		case errors.Is(err, contracts.ErrPathNotClean), errors.Is(err, contracts.ErrPathContainsNUL):
			return fmt.Errorf("%w: registry_path=%q", ErrRegistryPathNotClean, path)
		case errors.Is(err, contracts.ErrPathBasenameMismatch):
			return fmt.Errorf("%w: registry_path=%q", ErrRegistryPathBasename, path)
		default:
			return err
		}
	}
	return nil
}

func validateAgentsWithinPass(agents []contracts.AgentID, pkg contracts.TaskPackage, pass int, errWrap error) error {
	allowed := map[contracts.AgentID]struct{}{}
	for _, w := range pkg.Worktrees {
		if w.Pass == pass {
			allowed[w.Agent] = struct{}{}
		}
	}
	for _, agent := range agents {
		if _, ok := allowed[agent]; !ok {
			return fmt.Errorf("%w: agent %s not present in worktrees(pass=%d)", errWrap, agent, pass)
		}
	}
	return nil
}

func validateImplementationResponse(runID contracts.RunID, pass int, results []Step20AgentResult, rescue []RescueExhausted) error {
	seenResults := make(map[contracts.AgentID]struct{}, len(results))
	for i := range results {
		if err := validation.Instance().Struct(results[i]); err != nil {
			return fmt.Errorf("results[%d]: %w", i, err)
		}
		if _, dup := seenResults[results[i].Agent]; dup {
			return fmt.Errorf("%w: agent=%s", ErrStepResponseDuplicateResultAgent, results[i].Agent)
		}
		seenResults[results[i].Agent] = struct{}{}
		if err := results[i].Manifest.Validate(); err != nil {
			return fmt.Errorf("results[%d].manifest: %w", i, err)
		}
		manifestPass, manifestAgent, manifestRunID, err := manifestMetadata(results[i].Manifest)
		if err != nil {
			return fmt.Errorf("results[%d].manifest: %w", i, err)
		}
		if manifestRunID != runID {
			return fmt.Errorf("%w: response.run_id=%s manifest.run_id=%s", ErrStepResponseManifestRunIDMismatch, runID, manifestRunID)
		}
		if manifestPass != pass {
			return fmt.Errorf("%w: response.pass=%d manifest.pass=%d", ErrStepResponseManifestPassMismatch, pass, manifestPass)
		}
		if manifestAgent != results[i].Agent {
			return fmt.Errorf("%w: result.agent=%s manifest.agent=%s", ErrStepResponseManifestAgentMismatch, results[i].Agent, manifestAgent)
		}
	}

	seenRescue := make(map[contracts.AgentID]struct{}, len(rescue))
	for i := range rescue {
		if err := validation.Instance().Struct(rescue[i]); err != nil {
			return fmt.Errorf("rescue_exhausted[%d]: %w", i, err)
		}
		if _, dup := seenRescue[rescue[i].Agent]; dup {
			return fmt.Errorf("%w: agent=%s", ErrStepResponseDuplicateRescueAgent, rescue[i].Agent)
		}
		seenRescue[rescue[i].Agent] = struct{}{}
	}
	return nil
}

func validateImplementationPartition(results []Step20AgentResult, rescue []RescueExhausted, expectedAgents []contracts.AgentID) error {
	expected := make(map[contracts.AgentID]struct{}, len(expectedAgents))
	for _, agent := range expectedAgents {
		expected[agent] = struct{}{}
	}

	covered := make(map[contracts.AgentID]struct{}, len(results)+len(rescue))
	for _, result := range results {
		covered[result.Agent] = struct{}{}
	}
	for _, exhausted := range rescue {
		if _, overlap := covered[exhausted.Agent]; overlap {
			return fmt.Errorf("%w: agent=%s", ErrAgentResultOverlap, exhausted.Agent)
		}
		covered[exhausted.Agent] = struct{}{}
	}

	if len(covered) != len(expected) {
		return fmt.Errorf("%w: covered=%d expected=%d", ErrAgentCoverageMismatch, len(covered), len(expected))
	}
	for agent := range covered {
		if _, ok := expected[agent]; !ok {
			return fmt.Errorf("%w: unexpected agent=%s", ErrAgentCoverageMismatch, agent)
		}
	}
	for agent := range expected {
		if _, ok := covered[agent]; !ok {
			return fmt.Errorf("%w: missing agent=%s", ErrAgentCoverageMismatch, agent)
		}
	}
	return nil
}

func cloneImplementationResults(results []Step20AgentResult) []Step20AgentResult {
	if results == nil {
		return nil
	}
	cloned := make([]Step20AgentResult, len(results))
	for i := range results {
		cloned[i] = Step20AgentResult{
			Agent:    results[i].Agent,
			Manifest: cloneManifest(results[i].Manifest),
		}
	}
	return cloned
}

func cloneRescueExhausted(items []RescueExhausted) []RescueExhausted {
	if items == nil {
		return nil
	}
	cloned := make([]RescueExhausted, len(items))
	copy(cloned, items)
	return cloned
}

func cloneManifest(m contracts.Manifest) contracts.Manifest {
	cloned := contracts.Manifest{Kind: m.Kind}
	switch v := m.Value.(type) {
	case contracts.ManifestSuccess:
		cloned.Value = v
	case *contracts.ManifestSuccess:
		if v != nil {
			cloned.Value = *v
		}
	case contracts.ManifestError:
		cloned.Value = v
	case *contracts.ManifestError:
		if v != nil {
			cloned.Value = *v
		}
	case contracts.ManifestTimeout:
		cloned.Value = v
	case *contracts.ManifestTimeout:
		if v != nil {
			cloned.Value = *v
		}
	default:
		cloned.Value = nil
	}
	return cloned
}

func manifestMetadata(m contracts.Manifest) (pass int, agent contracts.AgentID, runID contracts.RunID, err error) {
	if m.Value == nil {
		return 0, "", "", contracts.ErrUnknownManifestKind
	}
	switch v := m.Value.(type) {
	case contracts.ManifestSuccess:
		return v.Pass, v.Agent, v.RunID, nil
	case *contracts.ManifestSuccess:
		if v == nil {
			return 0, "", "", contracts.ErrUnknownManifestKind
		}
		return v.Pass, v.Agent, v.RunID, nil
	case contracts.ManifestError:
		return v.Pass, v.Agent, v.RunID, nil
	case *contracts.ManifestError:
		if v == nil {
			return 0, "", "", contracts.ErrUnknownManifestKind
		}
		return v.Pass, v.Agent, v.RunID, nil
	case contracts.ManifestTimeout:
		return v.Pass, v.Agent, v.RunID, nil
	case *contracts.ManifestTimeout:
		if v == nil {
			return 0, "", "", contracts.ErrUnknownManifestKind
		}
		return v.Pass, v.Agent, v.RunID, nil
	default:
		return 0, "", "", contracts.ErrUnknownManifestKind
	}
}
