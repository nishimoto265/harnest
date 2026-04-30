package step70_decide

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/registryview"
)

func (r FilesystemResolver) buildRegistryEntry(runCtx internalio.RunContext, candidate contracts.Candidate, registryLines []registryLine, idempotencyKey string, at time.Time) (contracts.RuleRegistryEntry, error) {
	ruleID := candidate.TargetRuleID
	if candidate.Kind == contracts.CandidateKindNew {
		ruleID = generatedRuleIDForCandidate(candidate)
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

func generatedRuleIDForCandidate(candidate contracts.Candidate) string {
	const experimentLessonPrefix = "40/experiment/lessons/"
	if rel := filepath.ToSlash(candidate.ProposedBodyPath); strings.HasPrefix(rel, experimentLessonPrefix) {
		id := strings.TrimSuffix(strings.TrimPrefix(rel, experimentLessonPrefix), ".md")
		if !strings.Contains(id, "/") && contracts.ValidateRuleID(id) == nil {
			return id
		}
	}
	return generatedRuleID(candidate.CandidateID)
}

func rewriteRegistryEntryIdempotencyKeys(entries []contracts.RuleRegistryEntry, idempotencyKey string) []contracts.RuleRegistryEntry {
	if len(entries) == 0 {
		return nil
	}
	out := make([]contracts.RuleRegistryEntry, len(entries))
	for i, entry := range entries {
		switch v := entry.Value.(type) {
		case contracts.RuleRegistryAdded:
			v.IdempotencyKey = idempotencyKey
			entry.Value = v
		case contracts.RuleRegistryUpdated:
			v.IdempotencyKey = idempotencyKey
			entry.Value = v
		}
		out[i] = entry
	}
	return out
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
