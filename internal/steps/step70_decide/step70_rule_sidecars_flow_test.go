package step70_decide

import (
	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestPromoteRuleSidecarAndCleanup_FsyncParentDirsAfterDeletion(t *testing.T) {
	runCtx, _, _, _, _ := newFixtureWithResolver(t, "PR413")
	originalSync := syncStagingParentDir
	var calls []string
	syncStagingParentDir = func(path string) error {
		calls = append(calls, filepath.Clean(path))
		return nil
	}
	t.Cleanup(func() {
		syncStagingParentDir = originalSync
	})

	stagedPath := mustStagedRulePath(t, runCtx, "rules/rule-a.md")
	require.NoError(t, internalio.WriteAtomic(stagedPath, []byte("rule-a body\n")))
	require.NoError(t, promoteRuleSidecar(stagedPath, filepath.Join(runCtx.RunsBase, "rules", "rule-a.md"), sha256String("rule-a body\n"), ""))

	require.NoError(t, internalio.WriteAtomic(mustStagedRulePath(t, runCtx, "rules/rule-b.md"), []byte("rule-b body\n")))
	require.NoError(t, cleanupStagedRuleSidecars(runCtx))

	assert.Contains(t, calls, filepath.Clean(filepath.Dir(stagedPath)))
	assert.Contains(t, calls, filepath.Clean(runCtx.RunDir()))
}

// F10: after the first rule sidecar in a multi-entry adoption is published and
// its staged copy removed, a resume must recognise the destination's matching
// SHA as already-published instead of escalating the whole batch to
// needs_manual_recovery via errRulePublishStagedMissing.
func TestPromoteRuleSidecar_TreatsMissingStagedWithMatchingDestinationAsPublished(t *testing.T) {
	runCtx, _, _, _, _ := newFixtureWithResolver(t, "PR4101")
	body := "rule-a body\n"
	stagedPath := mustStagedRulePath(t, runCtx, "rules/rule-a.md")

	dstPath := filepath.Join(runCtx.RunsBase, "rules", "rule-a.md")
	require.NoError(t, internalio.WriteAtomic(dstPath, []byte(body)))
	// staged file intentionally absent to simulate a crash after the first
	// entry in the batch was published and its staged copy fsynced away.
	assert.NoFileExists(t, stagedPath)

	require.NoError(t, promoteRuleSidecar(stagedPath, dstPath, sha256String(body), ""))
	// Destination unchanged and still holds the planned bytes.
	got, err := os.ReadFile(dstPath)
	require.NoError(t, err)
	assert.Equal(t, body, string(got))
}

// F10: if the staged file is missing AND the destination does not hold the
// planned SHA, the original errRulePublishStagedMissing signal is preserved so
// the batch still escalates to needs_manual_recovery rather than silently
// committing stale bytes.
func TestPromoteRuleSidecar_MissingStagedWithWrongDestinationStillFails(t *testing.T) {
	runCtx, _, _, _, _ := newFixtureWithResolver(t, "PR4102")
	stagedPath := mustStagedRulePath(t, runCtx, "rules/rule-a.md")
	dstPath := filepath.Join(runCtx.RunsBase, "rules", "rule-a.md")
	require.NoError(t, internalio.WriteAtomic(dstPath, []byte("stale bytes\n")))

	err := promoteRuleSidecar(stagedPath, dstPath, sha256String("rule-a body\n"), "")
	require.ErrorIs(t, err, errRulePublishStagedMissing)
}
func TestPromoteRuleSidecar_AllowsUpdateWhenDestinationMatchesPrevSha(t *testing.T) {
	runCtx, _, _, _, _ := newFixtureWithResolver(t, "PR41025")
	oldBody := "old body\n"
	newBody := "new body\n"
	stagedPath := mustStagedRulePath(t, runCtx, "rules/rule-a.md")
	require.NoError(t, internalio.WriteAtomic(stagedPath, []byte(newBody)))

	dstPath := filepath.Join(runCtx.RunsBase, "rules", "rule-a.md")
	require.NoError(t, internalio.WriteAtomic(dstPath, []byte(oldBody)))

	require.NoError(t, promoteRuleSidecar(stagedPath, dstPath, sha256String(newBody), sha256String(oldBody)))

	got, err := os.ReadFile(dstPath)
	require.NoError(t, err)
	assert.Equal(t, newBody, string(got))
	assert.NoFileExists(t, stagedPath)
}
func TestPromoteRuleSidecar_RejectsUpdateWhenDestinationMissing(t *testing.T) {
	runCtx, _, _, _, _ := newFixtureWithResolver(t, "PR41026")
	oldBody := "old body\n"
	newBody := "new body\n"
	stagedPath := mustStagedRulePath(t, runCtx, "rules/rule-a.md")
	require.NoError(t, internalio.WriteAtomic(stagedPath, []byte(newBody)))

	dstPath := filepath.Join(runCtx.RunsBase, "rules", "rule-a.md")
	assert.NoFileExists(t, dstPath)

	err := promoteRuleSidecar(stagedPath, dstPath, sha256String(newBody), sha256String(oldBody))
	require.ErrorIs(t, err, errRulePublishConflict)
	assert.NoFileExists(t, dstPath)
	assert.FileExists(t, stagedPath)
}

// F10: multi-entry adoption resuming after a crash-between-publishes must
// complete without re-publishing the entry whose destination already holds
// the planned SHA.
func TestPromoteStagedRuleSidecars_MultiEntryResumeAfterFirstPublish(t *testing.T) {
	runCtx, _, candidates, _, resolver := newFixtureWithResolver(t, "PR4103")
	resolver.target.RulesToAppend = []contracts.RuleRegistryEntry{
		adoptAddedEntryWithBody(runCtx.RunID, "rule-a", "rule-a body\n"),
		adoptAddedEntryWithBody(runCtx.RunID, "rule-b", "rule-b body\n"),
	}
	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)

	// Prepare staging dir + entry-b staged file. Entry-a was already published
	// and its staged copy removed; entry-b's staged file still exists.
	require.NoError(t, internalio.WriteAtomic(mustStagedRulePath(t, runCtx, "rules/rule-b.md"), []byte("rule-b body\n")))
	dstA := filepath.Join(runCtx.RunsBase, "rules", "rule-a.md")
	require.NoError(t, internalio.WriteAtomic(dstA, []byte("rule-a body\n")))

	store := newTrackingStore(intentionPath(t, runCtx))
	require.NoError(t, promoteStagedRuleSidecars(runCtx, &intention, store))

	dstB := filepath.Join(runCtx.RunsBase, "rules", "rule-b.md")
	got, err := os.ReadFile(dstB)
	require.NoError(t, err)
	assert.Equal(t, "rule-b body\n", string(got))

	// Both entries were persisted as published (even the one recognised via
	// matching destination SHA with a missing staged file).
	require.NotEmpty(t, intention.PublishedRuleOpIDs)
	assert.ElementsMatch(t, []string{
		intention.PlannedAdoption.Entries[0].OpID,
		intention.PlannedAdoption.Entries[1].OpID,
	}, intention.PublishedRuleOpIDs)

	// Staging directory cleaned up after the last publish.
	stagingDir, err := runCtx.ResolveRunRelative("staging")
	require.NoError(t, err)
	_, err = os.Stat(stagingDir)
	assert.True(t, os.IsNotExist(err), "staging dir must be removed after successful promotion")
}

// F10: intention.PublishedRuleOpIDs persisted by a previous tick means the
// resume path skips the already-published entry's staged file, but success
// still requires the canonical destination to hold the planned bytes.
func TestPromoteStagedRuleSidecars_SkipsPublishedOpIDsOnResume(t *testing.T) {
	runCtx, _, candidates, _, resolver := newFixtureWithResolver(t, "PR4104")
	resolver.target.RulesToAppend = []contracts.RuleRegistryEntry{
		adoptAddedEntryWithBody(runCtx.RunID, "rule-a", "rule-a body\n"),
		adoptAddedEntryWithBody(runCtx.RunID, "rule-b", "rule-b body\n"),
	}
	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)
	intention.PublishedRuleOpIDs = []string{intention.PlannedAdoption.Entries[0].OpID}

	// Only entry-b's staged file exists; entry-a is already marked published.
	require.NoError(t, internalio.WriteAtomic(mustStagedRulePath(t, runCtx, "rules/rule-b.md"), []byte("rule-b body\n")))
	require.NoError(t, internalio.WriteAtomic(filepath.Join(runCtx.RunsBase, "rules", "rule-a.md"), []byte("rule-a body\n")))

	require.NoError(t, promoteStagedRuleSidecars(runCtx, &intention, nil))

	// rule-a had no staged file, but its destination was verified.
	assert.FileExists(t, filepath.Join(runCtx.RunsBase, "rules", "rule-a.md"))
	assert.FileExists(t, filepath.Join(runCtx.RunsBase, "rules", "rule-b.md"))
}
func TestPromoteStagedRuleSidecars_MissingStagingDirRequiresPublishedDestinations(t *testing.T) {
	runCtx, _, candidates, _, resolver := newFixtureWithResolver(t, "PR4105")
	resolver.target.RulesToAppend = []contracts.RuleRegistryEntry{
		adoptAddedEntryWithBody(runCtx.RunID, "rule-a", "rule-a body\n"),
	}
	intention := planningIntention(runCtx.RunID, resolver.target, candidates.CandidatesHash)

	err := promoteStagedRuleSidecars(runCtx, &intention, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, errRulePublishStagedMissing)

	require.NoError(t, internalio.WriteAtomic(filepath.Join(runCtx.RunsBase, "rules", "rule-a.md"), []byte("rule-a body\n")))
	require.NoError(t, promoteStagedRuleSidecars(runCtx, &intention, nil))
}
func TestPromoteRuleSidecar_RejectsSymlinkDestination(t *testing.T) {
	runCtx, _, _, _, _ := newFixtureWithResolver(t, "PR414")
	body := "rule-a body\n"
	stagedPath := mustStagedRulePath(t, runCtx, "rules/rule-a.md")
	require.NoError(t, internalio.WriteAtomic(stagedPath, []byte(body)))

	externalPath := filepath.Join(realTempDir(t), "external-rule.md")
	require.NoError(t, os.WriteFile(externalPath, []byte(body), 0o644))

	dstPath := filepath.Join(runCtx.RunsBase, "rules", "rule-a.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(dstPath), 0o755))
	require.NoError(t, os.Symlink(externalPath, dstPath))

	err := promoteRuleSidecar(stagedPath, dstPath, sha256String(body), "")
	require.ErrorIs(t, err, errRulePublishDestinationType)
	assert.FileExists(t, stagedPath)
	info, statErr := os.Lstat(dstPath)
	require.NoError(t, statErr)
	assert.NotZero(t, info.Mode()&os.ModeSymlink)
}
func TestPromoteRuleSidecar_DetectsDestinationSwapToSymlinkBeforeRead(t *testing.T) {
	runCtx, _, _, _, _ := newFixtureWithResolver(t, "PR4141")
	body := "rule-a body\n"
	stagedPath := mustStagedRulePath(t, runCtx, "rules/rule-a.md")
	require.NoError(t, internalio.WriteAtomic(stagedPath, []byte(body)))

	dstPath := filepath.Join(runCtx.RunsBase, "rules", "rule-a.md")
	require.NoError(t, internalio.WriteAtomic(dstPath, []byte("old body\n")))

	externalPath := filepath.Join(realTempDir(t), "external-rule.md")
	require.NoError(t, os.WriteFile(externalPath, []byte(body), 0o644))

	originalHook := promoteRuleSidecarBeforeDestinationRead
	promoteRuleSidecarBeforeDestinationRead = func(path string) {
		require.NoError(t, os.Remove(path))
		require.NoError(t, os.Symlink(externalPath, path))
	}
	t.Cleanup(func() {
		promoteRuleSidecarBeforeDestinationRead = originalHook
	})

	err := promoteRuleSidecar(stagedPath, dstPath, sha256String(body), "")
	require.ErrorIs(t, err, errRulePublishDestinationType)
	assert.FileExists(t, stagedPath)
}
