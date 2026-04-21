package step40_classify

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
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
	items, classifications, err := buildCandidates(cfg.IO.RunID, createdAt, scores, compliance, registry, filepath.Dir(cfg.registryPath()))
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

func buildCandidates(runID contracts.RunID, now time.Time, scores []contracts.ScoreEntry, compliance []contracts.ComplianceEntry, registry []contracts.RuleRegistryEntry, registryBase string) ([]contracts.Candidate, []contracts.ClassificationEntry, error) {
	if len(scores) == 0 || len(compliance) == 0 {
		return []contracts.Candidate{}, []contracts.ClassificationEntry{}, nil
	}

	violations := collectViolations(compliance)
	if len(violations) == 0 {
		return []contracts.Candidate{}, []contracts.ClassificationEntry{}, nil
	}

	activeRules := activeRulesFromRegistry(registry)
	activeRuleBodies := activeRuleBodiesFromRegistry(registry, registryBase, activeRules)
	ruleIDs := make([]string, 0, len(violations))
	for ruleID := range violations {
		ruleIDs = append(ruleIDs, ruleID)
	}
	sort.Strings(ruleIDs)

	candidates := make([]contracts.Candidate, 0, len(ruleIDs))
	classifications := make([]contracts.ClassificationEntry, 0, len(ruleIDs))
	for idx, ruleID := range ruleIDs {
		candidate, classification, err := buildCandidate(runID, now, idx+1, ruleID, violations[ruleID], activeRules[ruleID], activeRuleBodies)
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

func buildCandidate(runID contracts.RunID, now time.Time, index int, ruleID string, violationCount int, existsInRegistry bool, activeRuleBodies map[string]string) (contracts.Candidate, contracts.ClassificationEntry, error) {
	candidateID := fmt.Sprintf("cand-%s-%03d", runID, index)
	title := fmt.Sprintf("Rule candidate for %s", ruleID)
	problem := fmt.Sprintf("Pass1 recorded %d violation(s) for rule %s.", violationCount, ruleID)
	rationale := fmt.Sprintf("Phase 0 deterministic classify generated one candidate from compliance-A.jsonl for %s.", ruleID)

	bodyPath := filepath.Join("40", "candidates", candidateID+".md")
	draftBody := candidateBodyMarkdown(contracts.Candidate{
		CandidateID:      candidateID,
		Kind:             contracts.CandidateKindNew,
		TargetRuleID:     "",
		Title:            title,
		Problem:          problem,
		Rationale:        rationale,
		ProposedBodyPath: bodyPath,
	})

	kind := contracts.CandidateKindNew
	targetRuleID := ""
	similarity := 0
	if matchedRuleID, matchedScore := bestDuplicateMatch(draftBody, activeRuleBodies); matchedRuleID != "" && matchedScore >= 0.9 {
		kind = contracts.CandidateKindDuplicate
		targetRuleID = matchedRuleID
		similarity = int(matchedScore * 100)
	} else if existsInRegistry {
		kind = contracts.CandidateKindUpdate
		targetRuleID = ruleID
		similarity = 90
	}
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

func activeRuleBodiesFromRegistry(entries []contracts.RuleRegistryEntry, registryBase string, activeRules map[string]bool) map[string]string {
	bodies := make(map[string]string, len(activeRules))
	for ruleID := range activeRules {
		path, ok := latestRulePath(entries, ruleID)
		if !ok {
			continue
		}
		body, err := os.ReadFile(filepath.Join(registryBase, path))
		if err != nil {
			continue
		}
		bodies[ruleID] = string(body)
	}
	return bodies
}

func latestRulePath(entries []contracts.RuleRegistryEntry, ruleID string) (string, bool) {
	for i := len(entries) - 1; i >= 0; i-- {
		switch v := entries[i].Value.(type) {
		case contracts.RuleRegistryUpdated:
			if v.RuleID == ruleID {
				return v.RulePath, true
			}
		case *contracts.RuleRegistryUpdated:
			if v != nil && v.RuleID == ruleID {
				return v.RulePath, true
			}
		case contracts.RuleRegistryAdded:
			if v.RuleID == ruleID {
				return v.RulePath, true
			}
		case *contracts.RuleRegistryAdded:
			if v != nil && v.RuleID == ruleID {
				return v.RulePath, true
			}
		}
	}
	return "", false
}

func bestDuplicateMatch(candidateBody string, activeRuleBodies map[string]string) (string, float64) {
	bestRuleID := ""
	bestScore := 0.0
	normalizedCandidate := normalizeRuleContent(candidateBody)
	if strings.TrimSpace(normalizedCandidate) == "" {
		return "", 0
	}
	for ruleID, body := range activeRuleBodies {
		normalizedBody := normalizeRuleContent(body)
		if strings.TrimSpace(normalizedBody) == "" {
			continue
		}
		score := tokenSetSimilarity(normalizedCandidate, normalizedBody)
		if score > bestScore {
			bestRuleID = ruleID
			bestScore = score
		}
	}
	return bestRuleID, bestScore
}

func tokenSetSimilarity(left, right string) float64 {
	leftSet := normalizedTokenSet(left)
	rightSet := normalizedTokenSet(right)
	if len(leftSet) == 0 && len(rightSet) == 0 {
		return 1
	}
	intersection := 0
	union := make(map[string]struct{}, len(leftSet)+len(rightSet))
	for token := range leftSet {
		union[token] = struct{}{}
	}
	for token := range rightSet {
		if _, ok := leftSet[token]; ok {
			intersection++
		}
		union[token] = struct{}{}
	}
	return float64(intersection) / float64(len(union))
}

func normalizeRuleContent(value string) string {
	lines := strings.Split(value, "\n")
	normalized := make([]string, 0, len(lines))
	section := ""
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			continue
		case strings.HasPrefix(trimmed, "# "):
			continue
		case strings.HasPrefix(trimmed, "- source_rule_id:"):
			continue
		case strings.HasPrefix(trimmed, "- classification:"):
			continue
		case trimmed == "## Problem":
			section = "problem"
			continue
		case trimmed == "## Rationale":
			section = "rationale"
			continue
		}
		if section == "problem" && strings.HasPrefix(trimmed, "Pass1 recorded ") && strings.Contains(trimmed, " violation(s) for rule ") {
			continue
		}
		if section == "rationale" && strings.HasPrefix(trimmed, "Phase 0 deterministic classify generated one candidate from compliance-A.jsonl for ") {
			continue
		}
		normalized = append(normalized, trimmed)
	}
	return strings.Join(normalized, "\n")
}

func normalizedTokenSet(value string) map[string]struct{} {
	value = strings.ToLower(value)
	value = strings.ReplaceAll(value, "\n", " ")
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	})
	set := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		if field == "" {
			continue
		}
		set[field] = struct{}{}
	}
	return set
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
