// Package step40_classify implements the Phase 0 deterministic classify step.
// See docs/design/io-contracts.md §step 40 and the impl-spec for contract.
package step40_classify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
)

// Config is the input envelope for Run. orchestrator constructs it from its
// StepRunContext; keeping this package orchestrator-free avoids an import
// cycle.
type Config struct {
	IO           internalio.RunContext
	RegistryPath string
	TaskPackage  *contracts.TaskPackage
	Now          func() time.Time
}

var (
	ErrTaskPackageRequired = errors.New("step40_classify: task package is required")
	ErrRunIDMismatch       = errors.New("step40_classify: task package run_id does not match run context")
)

// Run reads pass1 compliance / score artifacts and the shared rules-registry,
// deterministically classifies any compliance violations into candidates, and
// writes `<run>/40/candidates.json` (completion marker) + sidecar files.
func Run(ctx context.Context, cfg Config) (*contracts.Candidates, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cfg.TaskPackage == nil {
		return nil, ErrTaskPackageRequired
	}
	if cfg.TaskPackage.RunID != cfg.IO.RunID {
		return nil, fmt.Errorf("%w: task_package=%s io=%s", ErrRunIDMismatch, cfg.TaskPackage.RunID, cfg.IO.RunID)
	}
	now := cfg.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}

	compliancePath, err := cfg.IO.ResolveRunRelative("30/compliance-A.jsonl")
	if err != nil {
		return nil, err
	}
	compliance, err := internalio.ReadJSONL[contracts.ComplianceEntry](compliancePath)
	if err != nil {
		return nil, err
	}

	scoresPath, err := cfg.IO.ResolveRunRelative("30/scores-A.jsonl")
	if err != nil {
		return nil, err
	}
	if _, err := internalio.ReadJSONL[contracts.ScoreEntry](scoresPath); err != nil {
		return nil, err
	}

	activeRules, err := loadActiveRules(cfg.RegistryPath)
	if err != nil {
		return nil, err
	}

	violatingRuleIDs := selectViolatingRuleIDs(compliance)

	candidates := make([]contracts.Candidate, 0, len(violatingRuleIDs))
	classifications := make([]contracts.ClassificationEntry, 0, len(violatingRuleIDs))
	runID := cfg.IO.RunID
	classifiedAt := now()
	for i, ruleID := range violatingRuleIDs {
		candidateID := fmt.Sprintf("cand-%s-%03d", runID, i)
		count := countVerdicts(compliance, ruleID)
		bodyRel := filepath.ToSlash(filepath.Join("40", "candidates", candidateID+".md"))
		bodyAbs, err := cfg.IO.ResolveRunRelative(bodyRel)
		if err != nil {
			return nil, err
		}
		body := renderBody(ruleID, count)
		if err := internalio.WriteAtomic(bodyAbs, []byte(body)); err != nil {
			return nil, err
		}
		sum := sha256.Sum256([]byte(body))
		bodySha := hex.EncodeToString(sum[:])

		matched := activeRules[ruleID]
		candidate := contracts.Candidate{
			CandidateID:        candidateID,
			Title:              truncate(fmt.Sprintf("Rule candidate for %s", ruleID), 200),
			Problem:            truncate(fmt.Sprintf("Pass1 judges recorded %d compliance violation(s) for rule %s.", count, ruleID), 500),
			Rationale:          truncate(fmt.Sprintf("Phase 0 deterministic classifier surfaced rule %s from compliance-A.jsonl.", ruleID), 500),
			ProposedBodyPath:   bodyRel,
			ProposedBodySha256: bodySha,
		}
		classification := contracts.ClassificationEntry{
			SchemaVersion: "1",
			RunID:         runID,
			CandidateID:   candidateID,
			Rationale:     truncate(fmt.Sprintf("Phase 0: matched_rule_id presence derives from rules-registry lookup for %s.", ruleID), 500),
			ClassifiedAt:  classifiedAt,
		}
		if matched {
			candidate.Kind = contracts.CandidateKindUpdate
			candidate.TargetRuleID = ruleID
			classification.Kind = contracts.CandidateKindUpdate
			classification.SimilarityScore = 90
			classification.MatchedRuleID = ruleID
		} else {
			candidate.Kind = contracts.CandidateKindNew
			classification.Kind = contracts.CandidateKindNew
			classification.SimilarityScore = 0
		}
		candidates = append(candidates, candidate)
		classifications = append(classifications, classification)
	}

	result := contracts.Candidates{
		SchemaVersion:  "1",
		RunID:          runID,
		Candidates:     candidates,
		CandidatesHash: contracts.CanonicalCandidatesHash(candidates),
		CreatedAt:      now(),
	}

	classificationPath, err := cfg.IO.ResolveRunRelative("40/classification.jsonl")
	if err != nil {
		return nil, err
	}
	if err := os.Remove(classificationPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, entry := range classifications {
		if err := internalio.AppendJSONL(classificationPath, entry); err != nil {
			return nil, err
		}
	}

	candidatesPath, err := cfg.IO.ResolveRunRelative("40/candidates.json")
	if err != nil {
		return nil, err
	}
	if err := internalio.WriteJSONAtomic(candidatesPath, result); err != nil {
		return nil, err
	}
	return &result, nil
}

// selectViolatingRuleIDs returns rule_ids that have at least one
// violated / invalid_exception / missed verdict, sorted alphabetically.
func selectViolatingRuleIDs(entries []contracts.ComplianceEntry) []string {
	seen := make(map[string]struct{})
	for _, e := range entries {
		if !isViolation(e.Verdict) {
			continue
		}
		seen[e.RuleID] = struct{}{}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func isViolation(v contracts.ComplianceVerdict) bool {
	switch v {
	case contracts.ComplianceVerdictViolated,
		contracts.ComplianceVerdictInvalidException,
		contracts.ComplianceVerdictMissed:
		return true
	}
	return false
}

func countVerdicts(entries []contracts.ComplianceEntry, ruleID string) int {
	n := 0
	for _, e := range entries {
		if e.RuleID == ruleID && isViolation(e.Verdict) {
			n++
		}
	}
	return n
}

// loadActiveRules returns the set of rule_ids whose latest lifecycle-bearing
// entry is added / updated / restored (i.e. not archived / rolled_back).
func loadActiveRules(path string) (map[string]bool, error) {
	active := make(map[string]bool)
	if path == "" {
		return active, nil
	}
	entries, err := internalio.ReadJSONL[contracts.RuleRegistryEntry](path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return active, nil
		}
		return nil, err
	}
	for _, entry := range entries {
		switch v := entry.Value.(type) {
		case contracts.RuleRegistryAdded:
			active[v.RuleID] = true
		case contracts.RuleRegistryUpdated:
			active[v.RuleID] = true
		case contracts.RuleRegistryRestored:
			active[v.RuleID] = true
		case contracts.RuleRegistryArchived:
			delete(active, v.RuleID)
		case contracts.RuleRegistryRolledBack:
			// rolled_back references an idempotency_key, not a rule_id; leave
			// the active set untouched (phase 0 deterministic approximation).
		}
	}
	return active, nil
}

func renderBody(ruleID string, violations int) string {
	return fmt.Sprintf("# Rule candidate: %s\n\nPhase 0 deterministic proposal surfaced by %d compliance violation(s).\n", ruleID, violations)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
