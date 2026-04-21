package step40_classify

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
)

const (
	candidatesJSONPath      = "40/candidates.json"
	classificationJSONLPath = "40/classification.jsonl"
	scoresPath              = "30/scores-A.jsonl"
	compliancePath          = "30/compliance-A.jsonl"
)

var ErrTaskPackageRequired = errors.New("step40_classify: task package is required")

type Config struct {
	IO           internalio.RunContext
	RegistryPath string
	TaskPackage  *contracts.TaskPackage
	Now          func() time.Time
}

func Run(ctx context.Context, cfg Config) (*contracts.Candidates, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	scores, err := readJSONLAt[contracts.ScoreEntry](cfg.IO, scoresPath)
	if err != nil {
		return nil, err
	}
	compliance, err := readJSONLAt[contracts.ComplianceEntry](cfg.IO, compliancePath)
	if err != nil {
		return nil, err
	}
	registry, err := internalio.ReadJSONL[contracts.RuleRegistryEntry](cfg.registryPath())
	if err != nil {
		return nil, err
	}

	createdAt := cfg.now()
	items, classifications, err := buildCandidates(cfg.IO.RunID, createdAt, scores, compliance, registry)
	if err != nil {
		return nil, err
	}

	if err := writeCandidateBodies(cfg.IO, items); err != nil {
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
	return internalio.ReadJSONL[T](path)
}

func buildCandidates(runID contracts.RunID, now time.Time, scores []contracts.ScoreEntry, compliance []contracts.ComplianceEntry, registry []contracts.RuleRegistryEntry) ([]contracts.Candidate, []contracts.ClassificationEntry, error) {
	if len(scores) == 0 || len(compliance) == 0 {
		return []contracts.Candidate{}, []contracts.ClassificationEntry{}, nil
	}

	violations := collectViolations(compliance)
	if len(violations) == 0 {
		return []contracts.Candidate{}, []contracts.ClassificationEntry{}, nil
	}

	activeRules := activeRulesFromRegistry(registry)
	ruleIDs := make([]string, 0, len(violations))
	for ruleID := range violations {
		ruleIDs = append(ruleIDs, ruleID)
	}
	sort.Strings(ruleIDs)

	candidates := make([]contracts.Candidate, 0, len(ruleIDs))
	classifications := make([]contracts.ClassificationEntry, 0, len(ruleIDs))
	for idx, ruleID := range ruleIDs {
		candidate, classification, err := buildCandidate(runID, now, idx+1, ruleID, violations[ruleID], activeRules[ruleID])
		if err != nil {
			return nil, nil, err
		}
		candidates = append(candidates, candidate)
		classifications = append(classifications, classification)
	}
	return candidates, classifications, nil
}

func collectViolations(entries []contracts.ComplianceEntry) map[string]int {
	violations := make(map[string]int)
	for _, entry := range entries {
		if !isViolationVerdict(entry.Verdict) {
			continue
		}
		violations[entry.RuleID]++
	}
	return violations
}

func isViolationVerdict(verdict contracts.ComplianceVerdict) bool {
	switch verdict {
	case contracts.ComplianceVerdictViolated, contracts.ComplianceVerdictInvalidException, contracts.ComplianceVerdictMissed:
		return true
	default:
		return false
	}
}

type ruleState struct {
	exists  bool
	lastSeq int
}

type rollbackState struct {
	ruleID     string
	prevExists bool
	seq        int
}

func activeRulesFromRegistry(entries []contracts.RuleRegistryEntry) map[string]bool {
	states := make(map[string]ruleState)
	rollbackTargets := make(map[string]rollbackState)

	apply := func(ruleID string, exists bool, seq int) {
		states[ruleID] = ruleState{exists: exists, lastSeq: seq}
	}

	for idx, entry := range entries {
		seq := idx + 1
		switch v := entry.Value.(type) {
		case contracts.RuleRegistryAdded:
			previous := states[v.RuleID]
			rollbackTargets[v.IdempotencyKey] = rollbackState{ruleID: v.RuleID, prevExists: previous.exists, seq: seq}
			apply(v.RuleID, true, seq)
		case *contracts.RuleRegistryAdded:
			if v == nil {
				continue
			}
			previous := states[v.RuleID]
			rollbackTargets[v.IdempotencyKey] = rollbackState{ruleID: v.RuleID, prevExists: previous.exists, seq: seq}
			apply(v.RuleID, true, seq)
		case contracts.RuleRegistryUpdated:
			previous := states[v.RuleID]
			rollbackTargets[v.IdempotencyKey] = rollbackState{ruleID: v.RuleID, prevExists: previous.exists, seq: seq}
			apply(v.RuleID, true, seq)
		case *contracts.RuleRegistryUpdated:
			if v == nil {
				continue
			}
			previous := states[v.RuleID]
			rollbackTargets[v.IdempotencyKey] = rollbackState{ruleID: v.RuleID, prevExists: previous.exists, seq: seq}
			apply(v.RuleID, true, seq)
		case contracts.RuleRegistryStatusChanged:
			apply(v.RuleID, v.NewStatus != contracts.RuleStatusArchived, seq)
		case *contracts.RuleRegistryStatusChanged:
			if v == nil {
				continue
			}
			apply(v.RuleID, v.NewStatus != contracts.RuleStatusArchived, seq)
		case contracts.RuleRegistryArchived:
			apply(v.RuleID, false, seq)
		case *contracts.RuleRegistryArchived:
			if v == nil {
				continue
			}
			apply(v.RuleID, false, seq)
		case contracts.RuleRegistryRestored:
			apply(v.RuleID, true, seq)
		case *contracts.RuleRegistryRestored:
			if v == nil {
				continue
			}
			apply(v.RuleID, true, seq)
		case contracts.RuleRegistryRolledBack:
			rollbackRule(states, rollbackTargets, v.TargetOpID, seq)
		case *contracts.RuleRegistryRolledBack:
			if v == nil {
				continue
			}
			rollbackRule(states, rollbackTargets, v.TargetOpID, seq)
		}
	}

	active := make(map[string]bool, len(states))
	for ruleID, state := range states {
		if state.exists {
			active[ruleID] = true
		}
	}
	return active
}

func rollbackRule(states map[string]ruleState, rollbackTargets map[string]rollbackState, targetOpID string, seq int) {
	target, ok := rollbackTargets[targetOpID]
	if !ok {
		return
	}
	current := states[target.ruleID]
	if current.lastSeq != target.seq {
		return
	}
	states[target.ruleID] = ruleState{
		exists:  target.prevExists,
		lastSeq: seq,
	}
}

func buildCandidate(runID contracts.RunID, now time.Time, index int, ruleID string, violationCount int, existsInRegistry bool) (contracts.Candidate, contracts.ClassificationEntry, error) {
	candidateID := fmt.Sprintf("cand-%s-%03d", runID, index)
	title := fmt.Sprintf("Rule candidate for %s", ruleID)
	problem := fmt.Sprintf("Pass1 recorded %d violation(s) for rule %s.", violationCount, ruleID)
	rationale := fmt.Sprintf("Phase 0 deterministic classify generated one candidate from compliance-A.jsonl for %s.", ruleID)

	kind := contracts.CandidateKindNew
	targetRuleID := ""
	similarity := 0
	if existsInRegistry {
		kind = contracts.CandidateKindUpdate
		targetRuleID = ruleID
		similarity = 90
	}

	bodyPath := filepath.Join("40", "candidates", candidateID+".md")
	body := candidateBodyMarkdown(contracts.Candidate{
		CandidateID:      candidateID,
		Kind:             kind,
		TargetRuleID:     targetRuleID,
		Title:            title,
		Problem:          problem,
		Rationale:        rationale,
		ProposedBodyPath: bodyPath,
	})
	bodySha256 := sha256Hex([]byte(body))

	candidate := contracts.Candidate{
		CandidateID:        candidateID,
		Kind:               kind,
		TargetRuleID:       targetRuleID,
		Title:              title,
		Problem:            problem,
		Rationale:          rationale,
		ProposedBodyPath:   bodyPath,
		ProposedBodySha256: bodySha256,
	}
	if err := candidate.Validate(); err != nil {
		return contracts.Candidate{}, contracts.ClassificationEntry{}, err
	}

	classification := contracts.ClassificationEntry{
		SchemaVersion:   "1",
		RunID:           runID,
		CandidateID:     candidateID,
		Kind:            kind,
		SimilarityScore: similarity,
		MatchedRuleID:   targetRuleID,
		Rationale:       rationale,
		ClassifiedAt:    now,
	}
	if err := classification.Validate(); err != nil {
		return contracts.Candidate{}, contracts.ClassificationEntry{}, err
	}

	return candidate, classification, nil
}

func candidateBodyMarkdown(candidate contracts.Candidate) string {
	ruleID := candidate.TargetRuleID
	if ruleID == "" {
		ruleID = strings.TrimPrefix(candidate.Title, "Rule candidate for ")
	}
	return fmt.Sprintf(
		"# %s\n\n- source_rule_id: %s\n- classification: %s\n\n## Problem\n%s\n\n## Rationale\n%s\n",
		candidate.Title,
		ruleID,
		candidate.Kind,
		candidate.Problem,
		candidate.Rationale,
	)
}

func writeCandidateBodies(runIO internalio.RunContext, candidates []contracts.Candidate) error {
	for _, candidate := range candidates {
		path, err := runIO.ResolveRunRelative(candidate.ProposedBodyPath)
		if err != nil {
			return err
		}
		body := candidateBodyMarkdown(candidate)
		if sha256Hex([]byte(body)) != candidate.ProposedBodySha256 {
			return fmt.Errorf("step40_classify: candidate body sha mismatch: candidate_id=%s", candidate.CandidateID)
		}
		if err := internalio.WriteAtomic(path, []byte(body)); err != nil {
			return err
		}
	}
	return nil
}

func writeClassificationJSONL(runIO internalio.RunContext, classifications []contracts.ClassificationEntry) error {
	path, err := runIO.ResolveRunRelative(classificationJSONLPath)
	if err != nil {
		return err
	}

	var buffer bytes.Buffer
	for _, entry := range classifications {
		if _, err := contracts.MarshalStrict(entry); err != nil {
			return err
		}
		payload, err := contracts.CanonicalMarshal(entry)
		if err != nil {
			return err
		}
		if len(payload)+1 > internalio.JSONLMaxLineBytes {
			return internalio.ErrEntryTooLarge
		}
		if _, err := buffer.Write(payload); err != nil {
			return err
		}
		if err := buffer.WriteByte('\n'); err != nil {
			return err
		}
	}
	return internalio.WriteAtomic(path, buffer.Bytes())
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
