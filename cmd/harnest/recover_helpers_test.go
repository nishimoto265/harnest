package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/stretchr/testify/require"
)

type recoverTestGit struct {
	head      string
	pushCalls int
}

type blockingRecoverGit struct{}

func (g *recoverTestGit) RemoteHead(context.Context, string) (string, error) {
	return g.head, nil
}

func (*blockingRecoverGit) RemoteHead(ctx context.Context, branch string) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

func (g *recoverTestGit) PushForceWithLease(_ context.Context, _ string, targetSHA, _ string) error {
	g.pushCalls++
	g.head = targetSHA
	return nil
}

func (*blockingRecoverGit) PushForceWithLease(ctx context.Context, branch, targetSHA, expectedOldSHA string) error {
	<-ctx.Done()
	return ctx.Err()
}

func (*recoverTestGit) RemoveWorktree(context.Context, string) error {
	return nil
}

func (*blockingRecoverGit) RemoveWorktree(context.Context, string) error {
	return nil
}

func seedRecoverActionRun(t *testing.T) (string, string, string, contracts.RunID) {
	t.Helper()
	root := realTempDir(t)
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	runID := contracts.RunID("2026-04-21-PR52-abcdef0")
	runDir := filepath.Join(runsBase, string(runID))
	require.NoError(t, os.MkdirAll(filepath.Join(runDir, "70"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(runsBase, "needs-recovery"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(runDir, "config.snapshot.yaml"), []byte(
		"repo:\n"+
			"  root: "+root+"\n"+
			"  default_branch: main\n"+
			"  best_branch: harnest/best\n"+
			"paths:\n"+
			"  runs: "+runsBase+"\n"+
			"worktree:\n"+
			"  base: "+worktreeBase+"\n",
	), 0o644))

	pkg := contracts.TaskPackage{
		SchemaVersion:           "1",
		RunID:                   runID,
		PR:                      52,
		Title:                   "recover",
		BaseSHA:                 strings.Repeat("1", 40),
		BestBranch:              "harnest/best",
		ReconstructedTaskPrompt: "prompt",
		CreatedAt:               time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
		Worktrees: []contracts.WorktreeAllocation{
			{Agent: "a1", Pass: 1, Path: filepath.Join(worktreeBase, string(runID)+"-pass1-a1"), Branch: "test/pass1/a1", BaseSHA: strings.Repeat("1", 40), HeadSHA: strings.Repeat("1", 40)},
			{Agent: "a2", Pass: 1, Path: filepath.Join(worktreeBase, string(runID)+"-pass1-a2"), Branch: "test/pass1/a2", BaseSHA: strings.Repeat("1", 40), HeadSHA: strings.Repeat("1", 40)},
			{Agent: "a3", Pass: 1, Path: filepath.Join(worktreeBase, string(runID)+"-pass1-a3"), Branch: "test/pass1/a3", BaseSHA: strings.Repeat("1", 40), HeadSHA: strings.Repeat("1", 40)},
			{Agent: "a1", Pass: 2, Path: filepath.Join(worktreeBase, string(runID)+"-pass2-a1"), Branch: "test/pass2/a1", BaseSHA: strings.Repeat("1", 40), HeadSHA: strings.Repeat("1", 40)},
			{Agent: "a2", Pass: 2, Path: filepath.Join(worktreeBase, string(runID)+"-pass2-a2"), Branch: "test/pass2/a2", BaseSHA: strings.Repeat("1", 40), HeadSHA: strings.Repeat("1", 40)},
			{Agent: "a3", Pass: 2, Path: filepath.Join(worktreeBase, string(runID)+"-pass2-a3"), Branch: "test/pass2/a3", BaseSHA: strings.Repeat("1", 40), HeadSHA: strings.Repeat("1", 40)},
		},
	}
	require.NoError(t, internalio.WriteJSONAtomic(filepath.Join(runDir, "task-package.json"), pkg))

	candidate := contracts.Candidate{
		CandidateID:        "c-1",
		Kind:               contracts.CandidateKindNew,
		Title:              "rule",
		ProposedBodyPath:   "40/candidates/c-1.md",
		ProposedBodySha256: strings.Repeat("1", 64),
	}
	candidates := contracts.Candidates{
		SchemaVersion:  "1",
		RunID:          runID,
		Candidates:     []contracts.Candidate{candidate},
		CandidatesHash: contracts.CanonicalCandidatesHash([]contracts.Candidate{candidate}),
		CreatedAt:      time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
	}
	require.NoError(t, os.MkdirAll(filepath.Join(runDir, "40"), 0o755))
	require.NoError(t, internalio.WriteJSONAtomic(filepath.Join(runDir, "40", "candidates.json"), candidates))
	return root, runsBase, worktreeBase, runID
}

func seedRecoverIntention(runID contracts.RunID, stage contracts.IntentionStage, bestShaBefore, targetSha, candidatesHash string) contracts.IntentionRecord {
	idempotencyKey := contracts.ComputeAdoptIdempotencyKey(string(runID), targetSha, bestShaBefore, candidatesHash)
	return contracts.IntentionRecord{
		SchemaVersion:      "1",
		Stage:              stage,
		IdempotencyKey:     idempotencyKey,
		RunID:              runID,
		BestShaBefore:      bestShaBefore,
		TargetSha:          targetSha,
		CandidatesHash:     candidatesHash,
		RegistryHeadBefore: "",
		StartedAt:          time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
		PlannedAdoption: &contracts.PlannedAdoption{
			IdempotencyKey: idempotencyKey,
			Entries: []contracts.PlannedAdoptionEntry{
				{
					Kind:     contracts.RegistryKindAdded,
					OpID:     contracts.ComputePlannedAdoptionEntryOpID(idempotencyKey, 0, "r-0001"),
					RuleID:   "r-0001",
					RulePath: "rules/r-0001.md",
					Sha256:   recoverRuleSHA(),
				},
			},
		},
	}
}

func appendRecoverRegistryEntry(t *testing.T, runsBase string, runID contracts.RunID, intention contracts.IntentionRecord) contracts.RegistryAppendResult {
	t.Helper()
	entry := contracts.RuleRegistryEntry{
		Kind: contracts.RegistryKindAdded,
		Value: contracts.RuleRegistryAdded{
			Kind:           contracts.RegistryKindAdded,
			SchemaVersion:  "1",
			RuleID:         "r-0001",
			RulePath:       "rules/r-0001.md",
			Sha256:         recoverRuleSHA(),
			IdempotencyKey: intention.PlannedAdoption.Entries[0].OpID,
			VersionSeq:     1,
			PrevHash:       "",
			ByRunID:        runID,
			At:             time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
		},
	}
	result, err := internalio.AppendRegistryEntry(filepath.Join(runsBase, "rules-registry.jsonl"), entry)
	require.NoError(t, err)
	return result
}

func seedRecoverPublishedRule(t *testing.T, runsBase string) {
	t.Helper()
	require.NoError(t, internalio.WriteAtomic(filepath.Join(runsBase, "rules", "r-0001.md"), []byte(recoverRuleBody())))
}

func recoverRuleBody() string {
	return "recover rule body\n"
}

func recoverRuleSHA() string {
	return sha256String(recoverRuleBody())
}
