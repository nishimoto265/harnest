package step40_classify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
)

func TestRunAbsentInputsWritesEmptyCandidates(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	now := time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC)

	got, err := Run(context.Background(), fixture.config(now))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(got.Candidates) != 0 {
		t.Fatalf("len(candidates) = %d, want 0", len(got.Candidates))
	}
	if err := got.VerifyCandidatesHash(); err != nil {
		t.Fatalf("VerifyCandidatesHash() error = %v", err)
	}

	decoded := readCandidatesFile(t, fixture.io)
	if len(decoded.Candidates) != 0 {
		t.Fatalf("decoded len(candidates) = %d, want 0", len(decoded.Candidates))
	}

	classifications := readClassificationFile(t, fixture.io)
	if len(classifications) != 0 {
		t.Fatalf("len(classifications) = %d, want 0", len(classifications))
	}

	classificationPath, err := fixture.io.ResolveRunRelative(classificationJSONLPath)
	if err != nil {
		t.Fatalf("ResolveRunRelative(classification) error = %v", err)
	}
	info, err := os.Stat(classificationPath)
	if err != nil {
		t.Fatalf("Stat(classification) error = %v", err)
	}
	if info.Size() != 0 {
		t.Fatalf("classification size = %d, want 0", info.Size())
	}
}

func TestRunComplianceWithoutRegistryCreatesNewCandidates(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	now := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)

	writeScores(t, fixture.io, scoreEntry(fixture.io.RunID, "fidelity"))
	writeCompliance(t, fixture.io,
		complianceEntry(fixture.io.RunID, "rule-b", contracts.ComplianceVerdictViolated),
		complianceEntry(fixture.io.RunID, "rule-a", contracts.ComplianceVerdictMissed),
		complianceEntry(fixture.io.RunID, "rule-a", contracts.ComplianceVerdictInvalidException),
		complianceEntry(fixture.io.RunID, "rule-z", contracts.ComplianceVerdictCompliant),
	)

	got, err := Run(context.Background(), fixture.config(now))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(got.Candidates) != 2 {
		t.Fatalf("len(candidates) = %d, want 2", len(got.Candidates))
	}
	if got.Candidates[0].CandidateID != "cand-2026-04-21-PR42-abcdef0-001" {
		t.Fatalf("candidate[0].candidate_id = %q", got.Candidates[0].CandidateID)
	}
	if got.Candidates[0].Kind != contracts.CandidateKindNew || got.Candidates[1].Kind != contracts.CandidateKindNew {
		t.Fatalf("candidate kinds = %v, want all new", []contracts.CandidateKind{got.Candidates[0].Kind, got.Candidates[1].Kind})
	}
	if got.Candidates[0].TargetRuleID != "" || got.Candidates[1].TargetRuleID != "" {
		t.Fatalf("new candidates must not carry target_rule_id")
	}
	if got.Candidates[0].Title != "Rule candidate for rule-a" || got.Candidates[1].Title != "Rule candidate for rule-b" {
		t.Fatalf("candidate titles = %q, %q", got.Candidates[0].Title, got.Candidates[1].Title)
	}

	decoded := readCandidatesFile(t, fixture.io)
	if err := decoded.VerifyCandidatesHash(); err != nil {
		t.Fatalf("decoded.VerifyCandidatesHash() error = %v", err)
	}
	classifications := readClassificationFile(t, fixture.io)
	if len(classifications) != 2 {
		t.Fatalf("len(classifications) = %d, want 2", len(classifications))
	}
	for _, entry := range classifications {
		if entry.Kind != contracts.CandidateKindNew {
			t.Fatalf("classification kind = %s, want new", entry.Kind)
		}
		if entry.SimilarityScore != 0 {
			t.Fatalf("classification similarity_score = %d, want 0", entry.SimilarityScore)
		}
		if entry.MatchedRuleID != "" {
			t.Fatalf("classification matched_rule_id = %q, want empty", entry.MatchedRuleID)
		}
	}
	assertCandidateBodies(t, fixture.io, decoded.Candidates)
}

func TestRunRegistryMixCreatesUpdateAndNewCandidates(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	now := time.Date(2026, 4, 21, 11, 0, 0, 0, time.UTC)

	writeScores(t, fixture.io, scoreEntry(fixture.io.RunID, "correctness"))
	writeCompliance(t, fixture.io,
		complianceEntry(fixture.io.RunID, "rule-active", contracts.ComplianceVerdictViolated),
		complianceEntry(fixture.io.RunID, "rule-archived", contracts.ComplianceVerdictMissed),
		complianceEntry(fixture.io.RunID, "rule-rolled-back", contracts.ComplianceVerdictInvalidException),
		complianceEntry(fixture.io.RunID, "rule-missing", contracts.ComplianceVerdictViolated),
	)
	writeRegistry(t, fixture.registryPath,
		registryAdded("rule-active", strings.Repeat("1", 64), 1),
		registryAdded("rule-archived", strings.Repeat("2", 64), 2),
		registryArchived("rule-archived", 3),
		registryAdded("rule-rolled-back", strings.Repeat("3", 64), 4),
		registryRolledBack(strings.Repeat("3", 64), 5),
	)

	got, err := Run(context.Background(), fixture.config(now))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	wantKinds := []contracts.CandidateKind{
		contracts.CandidateKindUpdate,
		contracts.CandidateKindNew,
		contracts.CandidateKindNew,
		contracts.CandidateKindNew,
	}
	if len(got.Candidates) != len(wantKinds) {
		t.Fatalf("len(candidates) = %d, want %d", len(got.Candidates), len(wantKinds))
	}
	for i, wantKind := range wantKinds {
		if got.Candidates[i].Kind != wantKind {
			t.Fatalf("candidate[%d].kind = %s, want %s", i, got.Candidates[i].Kind, wantKind)
		}
	}
	if got.Candidates[0].TargetRuleID != "rule-active" {
		t.Fatalf("candidate[0].target_rule_id = %q, want rule-active", got.Candidates[0].TargetRuleID)
	}

	decoded := readCandidatesFile(t, fixture.io)
	classifications := readClassificationFile(t, fixture.io)
	if len(classifications) != 4 {
		t.Fatalf("len(classifications) = %d, want 4", len(classifications))
	}
	if classifications[0].MatchedRuleID != "rule-active" || classifications[0].SimilarityScore != 90 {
		t.Fatalf("classification[0] = %+v, want matched_rule_id=rule-active similarity_score=90", classifications[0])
	}
	for i := 1; i < len(classifications); i++ {
		if classifications[i].MatchedRuleID != "" || classifications[i].SimilarityScore != 0 {
			t.Fatalf("classification[%d] = %+v, want new classification", i, classifications[i])
		}
	}
	assertCandidateBodies(t, fixture.io, decoded.Candidates)
}

type fixture struct {
	io           internalio.RunContext
	registryPath string
	taskPackage  *contracts.TaskPackage
}

func newFixture(t *testing.T) fixture {
	t.Helper()

	runsBase := t.TempDir()
	worktreeBase := t.TempDir()
	runID := contracts.RunID("2026-04-21-PR42-abcdef0")
	ioCtx, err := internalio.NewRunContext(runID, runsBase, worktreeBase)
	if err != nil {
		t.Fatalf("NewRunContext() error = %v", err)
	}
	taskPackage := newTaskPackage(t, ioCtx, worktreeBase)
	return fixture{
		io:           ioCtx,
		registryPath: ioCtx.RulesRegistryPath(),
		taskPackage:  taskPackage,
	}
}

func (f fixture) config(now time.Time) Config {
	return Config{
		IO:           f.io,
		RegistryPath: f.registryPath,
		TaskPackage:  f.taskPackage,
		Now: func() time.Time {
			return now
		},
	}
}

func newTaskPackage(t *testing.T, ioCtx internalio.RunContext, worktreeBase string) *contracts.TaskPackage {
	t.Helper()

	agents := []contracts.AgentID{"a1", "a2", "a3"}
	worktrees := make([]contracts.WorktreeAllocation, 0, 6)
	for pass := 1; pass <= 2; pass++ {
		for _, agent := range agents {
			worktrees = append(worktrees, contracts.WorktreeAllocation{
				Agent:   agent,
				Pass:    pass,
				Path:    filepath.Join(worktreeBase, string(ioCtx.RunID), string(agent), "pass", string(rune('0'+pass))),
				Branch:  "branch-" + string(agent) + "-pass-" + string(rune('0'+pass)),
				BaseSHA: strings.Repeat("a", 40),
				HeadSHA: strings.Repeat("b", 40),
			})
		}
	}

	pkg := &contracts.TaskPackage{
		SchemaVersion:           "1",
		RunID:                   ioCtx.RunID,
		PR:                      42,
		Title:                   "step40 fixture",
		BaseSHA:                 strings.Repeat("a", 40),
		BestBranch:              "main",
		ReconstructedTaskPrompt: "fixture prompt",
		Worktrees:               worktrees,
		CreatedAt:               time.Date(2026, 4, 21, 8, 0, 0, 0, time.UTC),
	}
	if err := pkg.Validate(); err != nil {
		t.Fatalf("task package Validate() error = %v", err)
	}
	return pkg
}

func scoreEntry(runID contracts.RunID, dimension contracts.Dimension) contracts.ScoreEntry {
	return contracts.ScoreEntry{
		SchemaVersion: "1",
		RunID:         runID,
		Pass:          1,
		Agent:         "a1",
		Dimension:     dimension,
		Score:         80,
		Reasons:       "fixture score",
		VerdictPath:   contracts.VerdictPathSingle,
		RubricVersion: "default",
		PromptVersion: "stub",
		ResolvedAt:    time.Date(2026, 4, 21, 8, 30, 0, 0, time.UTC),
	}
}

func complianceEntry(runID contracts.RunID, ruleID string, verdict contracts.ComplianceVerdict) contracts.ComplianceEntry {
	return contracts.ComplianceEntry{
		SchemaVersion: "1",
		RunID:         runID,
		Pass:          1,
		Agent:         "a1",
		RuleID:        ruleID,
		Verdict:       verdict,
		Rationale:     "fixture compliance",
		VerdictPath:   contracts.VerdictPathSingle,
		RubricVersion: "default",
		PromptVersion: "stub",
		ResolvedAt:    time.Date(2026, 4, 21, 8, 35, 0, 0, time.UTC),
	}
}

func registryAdded(ruleID, opID string, versionSeq int64) contracts.RuleRegistryEntry {
	return contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindAdded,
		Value: contracts.RuleRegistryAdded{
			Kind:           contracts.RegistryKindAdded,
			SchemaVersion:  "1",
			RuleID:         ruleID,
			RulePath:       filepath.Join("rules", ruleID+".md"),
			Sha256:         strings.Repeat("c", 64),
			IdempotencyKey: opID,
			VersionSeq:     versionSeq,
			PrevHash:       prevHash(versionSeq),
			ByRunID:        "2026-04-21-PR42-aaaaaaa",
			At:             time.Date(2026, 4, 21, 7, 0, 0, 0, time.UTC),
		},
	}
}

func registryArchived(ruleID string, versionSeq int64) contracts.RuleRegistryEntry {
	return contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindArchived,
		Value: contracts.RuleRegistryArchived{
			Kind:          contracts.RegistryKindArchived,
			SchemaVersion: "1",
			RuleID:        ruleID,
			PrevStatus:    contracts.RuleStatusActive,
			NewStatus:     contracts.RuleStatusArchived,
			OpID:          strings.Repeat("d", 64),
			VersionSeq:    versionSeq,
			PrevHash:      prevHash(versionSeq),
			BySunsetRunID: "sunset-1",
			At:            time.Date(2026, 4, 21, 7, 10, 0, 0, time.UTC),
		},
	}
}

func registryRolledBack(targetOpID string, versionSeq int64) contracts.RuleRegistryEntry {
	return contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindRolledBack,
		Value: contracts.RuleRegistryRolledBack{
			Kind:           contracts.RegistryKindRolledBack,
			SchemaVersion:  "1",
			TargetOpID:     targetOpID,
			TargetOffset:   0,
			TargetSha256:   strings.Repeat("e", 64),
			ByRunID:        "2026-04-21-PR42-bbbbbbb",
			RollbackReason: contracts.RollbackReasonTransactionalFailure,
			FailedStep:     contracts.FailedStep70,
			VersionSeq:     versionSeq,
			PrevHash:       prevHash(versionSeq),
			At:             time.Date(2026, 4, 21, 7, 20, 0, 0, time.UTC),
		},
	}
}

func prevHash(versionSeq int64) string {
	if versionSeq == 1 {
		return ""
	}
	return strings.Repeat("f", 64)
}

func writeScores(t *testing.T, ioCtx internalio.RunContext, entries ...contracts.ScoreEntry) {
	t.Helper()

	path, err := ioCtx.ResolveRunRelative(scoresPath)
	if err != nil {
		t.Fatalf("ResolveRunRelative(scores) error = %v", err)
	}
	if err := internalio.WriteAtomic(path, []byte{}); err != nil {
		t.Fatalf("WriteAtomic(scores) error = %v", err)
	}
	for _, entry := range entries {
		if err := internalio.AppendJSONL(path, entry); err != nil {
			t.Fatalf("AppendJSONL(scores) error = %v", err)
		}
	}
}

func writeCompliance(t *testing.T, ioCtx internalio.RunContext, entries ...contracts.ComplianceEntry) {
	t.Helper()

	path, err := ioCtx.ResolveRunRelative(compliancePath)
	if err != nil {
		t.Fatalf("ResolveRunRelative(compliance) error = %v", err)
	}
	if err := internalio.WriteAtomic(path, []byte{}); err != nil {
		t.Fatalf("WriteAtomic(compliance) error = %v", err)
	}
	for _, entry := range entries {
		if err := internalio.AppendJSONL(path, entry); err != nil {
			t.Fatalf("AppendJSONL(compliance) error = %v", err)
		}
	}
}

func writeRegistry(t *testing.T, registryPath string, entries ...contracts.RuleRegistryEntry) {
	t.Helper()

	if err := internalio.WriteAtomic(registryPath, []byte{}); err != nil {
		t.Fatalf("WriteAtomic(registry) error = %v", err)
	}
	for _, entry := range entries {
		if err := internalio.AppendJSONL(registryPath, entry); err != nil {
			t.Fatalf("AppendJSONL(registry) error = %v", err)
		}
	}
}

func readCandidatesFile(t *testing.T, ioCtx internalio.RunContext) contracts.Candidates {
	t.Helper()

	path, err := ioCtx.ResolveRunRelative(candidatesJSONPath)
	if err != nil {
		t.Fatalf("ResolveRunRelative(candidates) error = %v", err)
	}
	got, err := internalio.ReadJSON[contracts.Candidates](path)
	if err != nil {
		t.Fatalf("ReadJSON(candidates) error = %v", err)
	}
	return got
}

func readClassificationFile(t *testing.T, ioCtx internalio.RunContext) []contracts.ClassificationEntry {
	t.Helper()

	path, err := ioCtx.ResolveRunRelative(classificationJSONLPath)
	if err != nil {
		t.Fatalf("ResolveRunRelative(classification) error = %v", err)
	}
	got, err := internalio.ReadJSONL[contracts.ClassificationEntry](path)
	if err != nil {
		t.Fatalf("ReadJSONL(classification) error = %v", err)
	}
	return got
}

func assertCandidateBodies(t *testing.T, ioCtx internalio.RunContext, candidates []contracts.Candidate) {
	t.Helper()

	for _, candidate := range candidates {
		path, err := ioCtx.ResolveRunRelative(candidate.ProposedBodyPath)
		if err != nil {
			t.Fatalf("ResolveRunRelative(body) error = %v", err)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(%s) error = %v", candidate.ProposedBodyPath, err)
		}
		sum := sha256.Sum256(body)
		if got := hex.EncodeToString(sum[:]); got != candidate.ProposedBodySha256 {
			t.Fatalf("body sha256 = %s, want %s", got, candidate.ProposedBodySha256)
		}
	}
}
