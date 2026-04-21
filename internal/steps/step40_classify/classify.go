package step40_classify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
)

const (
	schemaVersion           = "1"
	scoresPath              = "30/scores-A.jsonl"
	compliancePath          = "30/compliance-A.jsonl"
	candidatesJSONPath      = "40/candidates.json"
	classificationJSONLPath = "40/classification.jsonl"
	candidateBodiesDir      = "40/candidates"
)

var (
	ErrTaskPackageRequired = errors.New("step40_classify: task package is required")
	ErrRunIDMismatch       = errors.New("step40_classify: task package run_id must match io.run_id")
)

type Config struct {
	IO           internalio.RunContext
	RegistryPath string
	TaskPackage  *contracts.TaskPackage
	Now          func() time.Time
}

func Run(ctx context.Context, cfg Config) (*contracts.Candidates, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	scores, err := readJSONL[contracts.ScoreEntry](cfg.IO, scoresPath)
	if err != nil {
		return nil, err
	}
	compliance, err := readJSONL[contracts.ComplianceEntry](cfg.IO, compliancePath)
	if err != nil {
		return nil, err
	}

	activeRules, err := loadActiveRules(cfg.RegistryPath)
	if err != nil {
		return nil, err
	}

	now := cfg.now()
	candidates := buildCandidates(cfg.IO.RunID, activeRules, scores, compliance, now)
	if err := writeCandidates(ctx, cfg.IO, candidates); err != nil {
		return nil, err
	}
	return &candidates, nil
}

func (cfg Config) Validate() error {
	if cfg.TaskPackage == nil {
		return ErrTaskPackageRequired
	}
	if err := cfg.TaskPackage.Validate(); err != nil {
		return err
	}
	if cfg.TaskPackage.RunID != cfg.IO.RunID {
		return fmt.Errorf("%w: task_package.run_id=%s io.run_id=%s", ErrRunIDMismatch, cfg.TaskPackage.RunID, cfg.IO.RunID)
	}
	if err := contracts.EnsureCleanAbsolutePath(cfg.IO.RunsBase); err != nil {
		return err
	}
	if err := contracts.EnsureCleanAbsolutePath(cfg.IO.WorktreeBase); err != nil {
		return err
	}
	if err := contracts.EnsureCleanAbsolutePath(cfg.RegistryPath); err != nil {
		return err
	}
	return nil
}

func (cfg Config) now() time.Time {
	if cfg.Now == nil {
		return time.Now().UTC()
	}
	return cfg.Now().UTC()
}

func buildCandidates(runID contracts.RunID, activeRules map[string]bool, scores []contracts.ScoreEntry, compliance []contracts.ComplianceEntry, now time.Time) contracts.Candidates {
	items := make([]contracts.Candidate, 0)

	if len(scores) > 0 && len(compliance) > 0 {
		violationCounts := violationCounts(compliance)
		ruleIDs := make([]string, 0, len(violationCounts))
		for ruleID := range violationCounts {
			ruleIDs = append(ruleIDs, ruleID)
		}
		sort.Strings(ruleIDs)

		items = make([]contracts.Candidate, 0, len(ruleIDs))
		for i, ruleID := range ruleIDs {
			candidateID := fmt.Sprintf("cand-%s-%03d", runID, i+1)
			kind := contracts.CandidateKindNew
			targetRuleID := ""
			if activeRules[ruleID] {
				kind = contracts.CandidateKindUpdate
				targetRuleID = ruleID
			}

			violationCount := violationCounts[ruleID]
			rationale := truncateRunes(
				fmt.Sprintf("Phase-0 classify emitted a deterministic %s candidate for %s from %d pass1 compliance finding(s).", kind, ruleID, violationCount),
				500,
			)
			candidate := contracts.Candidate{
				CandidateID:      candidateID,
				Kind:             kind,
				TargetRuleID:     targetRuleID,
				Title:            truncateRunes(fmt.Sprintf("Rule candidate for %s", ruleID), 200),
				Problem:          truncateRunes(fmt.Sprintf("%d pass1 compliance finding(s) referenced rule %s.", violationCount, ruleID), 500),
				Rationale:        rationale,
				ProposedBodyPath: filepath.Join(candidateBodiesDir, candidateID+".md"),
			}
			candidate.ProposedBodySha256 = sha256Hex(candidateBody(candidate))
			items = append(items, candidate)
		}
	}

	return contracts.Candidates{
		SchemaVersion:  schemaVersion,
		RunID:          runID,
		Candidates:     items,
		CandidatesHash: contracts.CanonicalCandidatesHash(items),
		CreatedAt:      now,
	}
}

func writeCandidates(ctx context.Context, ioCtx internalio.RunContext, candidates contracts.Candidates) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	classificationPath, err := ioCtx.ResolveRunRelative(classificationJSONLPath)
	if err != nil {
		return err
	}
	if err := internalio.WriteAtomic(classificationPath, []byte{}); err != nil {
		return err
	}

	for _, candidate := range candidates.Candidates {
		if err := ctx.Err(); err != nil {
			return err
		}
		bodyPath, err := ioCtx.ResolveRunRelative(candidate.ProposedBodyPath)
		if err != nil {
			return err
		}
		body := candidateBody(candidate)
		if sha256Hex(body) != candidate.ProposedBodySha256 {
			return fmt.Errorf("step40_classify: proposed body sha mismatch: candidate_id=%s", candidate.CandidateID)
		}
		if err := internalio.WriteAtomic(bodyPath, body); err != nil {
			return err
		}

		entry := contracts.ClassificationEntry{
			SchemaVersion:   schemaVersion,
			RunID:           candidates.RunID,
			CandidateID:     candidate.CandidateID,
			Kind:            candidate.Kind,
			SimilarityScore: 0,
			Rationale:       candidate.Rationale,
			ClassifiedAt:    candidates.CreatedAt,
		}
		if candidate.Kind != contracts.CandidateKindNew {
			entry.MatchedRuleID = candidate.TargetRuleID
			entry.SimilarityScore = 90
		}
		if err := internalio.AppendJSONL(classificationPath, entry); err != nil {
			return err
		}
	}

	candidatesPath, err := ioCtx.ResolveRunRelative(candidatesJSONPath)
	if err != nil {
		return err
	}
	return internalio.WriteJSONAtomic(candidatesPath, candidates)
}

func readJSONL[T any](ioCtx internalio.RunContext, relPath string) ([]T, error) {
	path, err := ioCtx.ResolveRunRelative(relPath)
	if err != nil {
		return nil, err
	}
	return internalio.ReadJSONL[T](path)
}

func loadActiveRules(registryPath string) (map[string]bool, error) {
	entries, err := internalio.ReadJSONL[contracts.RuleRegistryEntry](registryPath)
	if err != nil {
		return nil, err
	}

	active := make(map[string]bool)
	opRuleID := make(map[string]string)
	for _, entry := range entries {
		switch v := entry.Value.(type) {
		case contracts.RuleRegistryAdded:
			active[v.RuleID] = true
			opRuleID[v.IdempotencyKey] = v.RuleID
		case *contracts.RuleRegistryAdded:
			if v != nil {
				active[v.RuleID] = true
				opRuleID[v.IdempotencyKey] = v.RuleID
			}
		case contracts.RuleRegistryUpdated:
			active[v.RuleID] = true
			opRuleID[v.IdempotencyKey] = v.RuleID
		case *contracts.RuleRegistryUpdated:
			if v != nil {
				active[v.RuleID] = true
				opRuleID[v.IdempotencyKey] = v.RuleID
			}
		case contracts.RuleRegistryRestored:
			active[v.RuleID] = true
		case *contracts.RuleRegistryRestored:
			if v != nil {
				active[v.RuleID] = true
			}
		case contracts.RuleRegistryArchived:
			delete(active, v.RuleID)
		case *contracts.RuleRegistryArchived:
			if v != nil {
				delete(active, v.RuleID)
			}
		case contracts.RuleRegistryStatusChanged:
			if v.NewStatus == contracts.RuleStatusArchived {
				delete(active, v.RuleID)
			} else {
				active[v.RuleID] = true
			}
		case *contracts.RuleRegistryStatusChanged:
			if v != nil {
				if v.NewStatus == contracts.RuleStatusArchived {
					delete(active, v.RuleID)
				} else {
					active[v.RuleID] = true
				}
			}
		case contracts.RuleRegistryRolledBack:
			delete(active, opRuleID[v.TargetOpID])
		case *contracts.RuleRegistryRolledBack:
			if v != nil {
				delete(active, opRuleID[v.TargetOpID])
			}
		}
	}
	return active, nil
}

func violationCounts(entries []contracts.ComplianceEntry) map[string]int {
	counts := make(map[string]int)
	for _, entry := range entries {
		switch entry.Verdict {
		case contracts.ComplianceVerdictViolated, contracts.ComplianceVerdictInvalidException, contracts.ComplianceVerdictMissed:
			counts[entry.RuleID]++
		}
	}
	return counts
}

func candidateBody(candidate contracts.Candidate) []byte {
	target := "none"
	if candidate.TargetRuleID != "" {
		target = candidate.TargetRuleID
	}
	return []byte(fmt.Sprintf(
		"# %s\n\n- kind: %s\n- target_rule_id: %s\n\n%s\n",
		candidate.Title,
		candidate.Kind,
		target,
		candidate.Rationale,
	))
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max <= 3 {
		return string(runes[:max])
	}
	return string(runes[:max-3]) + "..."
}
