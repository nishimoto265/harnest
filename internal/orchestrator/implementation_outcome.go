package orchestrator

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/contracts/stepio"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
)

func noActionableCandidates(candidates *contracts.Candidates) bool {
	return len(actionableCandidateIDs(candidates)) == 0
}

func hasFinalizedManifest(runIO internalio.RunContext, pass int, agent contracts.AgentID) (bool, error) {
	manifest, err := internalio.LoadFinalizedManifest(runIO, pass, agent)
	if err == nil && manifest != nil {
		if isProviderInterruptedManifest(*manifest) {
			return false, nil
		}
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func scorableAgentsForPass(runIO internalio.RunContext, pkg *contracts.TaskPackage, pass int) ([]contracts.AgentID, error) {
	if pkg == nil {
		return nil, errors.New("orchestrator: task package is required")
	}
	agents := make([]contracts.AgentID, 0, len(pkg.Worktrees))
	seen := make(map[contracts.AgentID]struct{}, len(pkg.Worktrees))
	for _, wt := range pkg.Worktrees {
		if wt.Pass != pass {
			continue
		}
		if _, ok := seen[wt.Agent]; ok {
			continue
		}
		manifest, err := internalio.LoadScorableManifest(runIO, pass, wt.Agent)
		if err != nil {
			if shouldSkipScorableManifest(err) || os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		if manifest == nil {
			continue
		}
		seen[wt.Agent] = struct{}{}
		agents = append(agents, wt.Agent)
	}
	return agents, nil
}

func providerInterruptionFromManifests(run *StepRunContext, pass int) (contracts.InterruptedReason, string, bool, error) {
	if run == nil || run.TaskPackage == nil {
		return "", "", false, errors.New("orchestrator: task package is required")
	}
	agents := passAgents(run.TaskPackage, pass)
	if len(agents) == 0 {
		return "", "", false, nil
	}
	var reason contracts.InterruptedReason
	details := make([]string, 0, len(agents))
	for _, agent := range agents {
		manifest, err := internalio.LoadFinalizedManifest(run.IO, pass, agent)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", "", false, err
		}
		agentReason, ok := providerInterruptionManifestReason(*manifest)
		if !ok {
			continue
		}
		if reason == "" {
			reason = agentReason
		} else if reason != agentReason {
			reason = contracts.InterruptedReasonUnknown
		}
		details = append(details, fmt.Sprintf("agent=%s reason=%s", agent, manifestErrorReason(*manifest)))
	}
	if reason == "" {
		return "", "", false, nil
	}
	return reason, strings.Join(details, "; "), true, nil
}

func providerInterruptionFromNonScorableManifests(run *StepRunContext, pass int) (contracts.InterruptedReason, string, bool, error) {
	if run == nil || run.TaskPackage == nil {
		return "", "", false, errors.New("orchestrator: task package is required")
	}
	agents := passAgents(run.TaskPackage, pass)
	if len(agents) == 0 {
		return "", "", false, nil
	}
	var reason contracts.InterruptedReason
	details := make([]string, 0, len(agents))
	for _, agent := range agents {
		manifest, err := internalio.LoadFinalizedManifest(run.IO, pass, agent)
		if err != nil {
			return "", "", false, err
		}
		agentReason, ok := providerInterruptionManifestReason(*manifest)
		if !ok {
			return "", "", false, nil
		}
		if reason == "" {
			reason = agentReason
		} else if reason != agentReason {
			reason = contracts.InterruptedReasonUnknown
		}
		details = append(details, fmt.Sprintf("agent=%s reason=%s", agent, manifestErrorReason(*manifest)))
	}
	if reason == "" {
		return "", "", false, nil
	}
	return reason, strings.Join(details, "; "), true, nil
}

func allFinalizedManifestsTimedOut(run *StepRunContext, pass int) (bool, error) {
	if run == nil || run.TaskPackage == nil {
		return false, errors.New("orchestrator: task package is required")
	}
	agents := passAgents(run.TaskPackage, pass)
	if len(agents) == 0 {
		return false, nil
	}
	for _, agent := range agents {
		manifest, err := internalio.LoadFinalizedManifest(run.IO, pass, agent)
		if err != nil {
			if os.IsNotExist(err) {
				return false, nil
			}
			return false, err
		}
		if manifest.Kind != contracts.ManifestKindTimeout {
			return false, nil
		}
	}
	return true, nil
}

func isProviderInterruptedManifest(manifest contracts.Manifest) bool {
	_, ok := providerInterruptionManifestReason(manifest)
	return ok
}

func providerInterruptionManifestReason(manifest contracts.Manifest) (contracts.InterruptedReason, bool) {
	value, ok := manifestError(manifest)
	if !ok {
		return "", false
	}
	if reason, ok := providerManifestReason(value.Reason); ok {
		return reason, true
	}
	if value.Reason == string(contracts.InterruptedReasonUnknown) && value.ExitCode != 0 {
		return contracts.InterruptedReasonUnknown, true
	}
	return "", false
}

func manifestErrorReason(manifest contracts.Manifest) string {
	value, ok := manifestError(manifest)
	if !ok {
		return ""
	}
	return value.Reason
}

func manifestError(manifest contracts.Manifest) (contracts.ManifestError, bool) {
	switch value := manifest.Value.(type) {
	case contracts.ManifestError:
		return value, true
	case *contracts.ManifestError:
		if value != nil {
			return *value, true
		}
	}
	return contracts.ManifestError{}, false
}

func providerManifestReason(reason string) (contracts.InterruptedReason, bool) {
	switch reason {
	case string(contracts.InterruptedReasonRateLimit):
		return contracts.InterruptedReasonRateLimit, true
	case string(contracts.InterruptedReasonBudget):
		return contracts.InterruptedReasonBudget, true
	case string(contracts.InterruptedReasonContext):
		return contracts.InterruptedReasonContext, true
	case string(contracts.InterruptedReasonSignal):
		return contracts.InterruptedReasonSignal, true
	default:
		return "", false
	}
}

func shouldSkipScorableManifest(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, internalio.ErrNotScorable) ||
		errors.Is(err, contracts.ErrDuplicateJSONKey) ||
		errors.Is(err, contracts.ErrTrailingJSON) ||
		errors.Is(err, contracts.ErrUnknownManifestKind) {
		return true
	}
	return strings.Contains(err.Error(), "Field validation")
}

func (o *Orchestrator) validateImplementationBoundary(run *StepRunContext, pass int, agents []contracts.AgentID) error {
	if run == nil || run.TaskPackage == nil {
		return errors.New("orchestrator: task package is required")
	}
	if run.Config == nil {
		return errors.New("orchestrator: config is required")
	}
	results := make([]stepio.Step20AgentResult, 0, len(agents))
	for _, agent := range agents {
		manifest, err := internalio.LoadFinalizedManifest(run.IO, pass, agent)
		if err != nil {
			return fmt.Errorf("orchestrator: load finalized manifest pass=%d agent=%s: %w", pass, agent, err)
		}
		results = append(results, stepio.Step20AgentResult{
			Agent:    agent,
			Manifest: *manifest,
		})
	}

	switch pass {
	case 1:
		if o.decoders.Step20 == nil {
			return nil
		}
		timeout := run.Config.StepTimeouts["step20"]
		req := stepio.Step20Request{
			TaskPackage:    *run.TaskPackage,
			Agents:         append([]contracts.AgentID(nil), agents...),
			TimeoutSeconds: timeout,
		}
		resp, err := stepio.NewStep20Response(results, nil, req)
		if err != nil {
			return err
		}
		payload, err := resp.MarshalJSON()
		if err != nil {
			return err
		}
		_, err = o.decoders.Step20(payload, req)
		return err
	case 2:
		if o.decoders.Step50 == nil {
			return nil
		}
		timeout := run.Config.StepTimeouts["step50"]
		req := stepio.Step50Request{
			TaskPackage:      *run.TaskPackage,
			Agents:           append([]contracts.AgentID(nil), agents...),
			TimeoutSeconds:   timeout,
			CandidateRuleIDs: actionableCandidateIDs(run.Candidates),
		}
		resp, err := stepio.NewStep50Response(results, nil, req)
		if err != nil {
			return err
		}
		payload, err := resp.MarshalJSON()
		if err != nil {
			return err
		}
		_, err = o.decoders.Step50(payload, req)
		return err
	default:
		return fmt.Errorf("orchestrator: unsupported implementation pass=%d", pass)
	}
}

func actionableCandidateIDs(candidates *contracts.Candidates) []string {
	if candidates == nil || len(candidates.Candidates) == 0 {
		return nil
	}
	ids := make([]string, 0, len(candidates.Candidates))
	for _, candidate := range candidates.Candidates {
		if candidate.Kind == contracts.CandidateKindDuplicate {
			continue
		}
		ids = append(ids, candidate.CandidateID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}
