package state

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAppendAndScanEventsForRun_RoundTrip(t *testing.T) {
	ctx := testRunContext(t, "2026-04-21-PR42-abcdef0", t.TempDir(), t.TempDir())
	entry := contracts.StateEntry{
		Kind: contracts.StateKindStepDone,
		Value: contracts.StateEntryStepDone{
			Kind:  contracts.StateKindStepDone,
			PR:    42,
			RunID: ctx.RunID,
			Step:  contracts.FailedStep20,
			At:    time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
		},
	}

	require.NoError(t, AppendStateEntry(ctx, entry))

	events, err := ScanEventsForRun(ctx, ctx.RunID)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, entry, events[0])
}

func TestReaderLastEventForPR_ReverseScanCorrectness(t *testing.T) {
	runsBase := t.TempDir()
	worktreeBase := t.TempDir()
	ctx42 := testRunContext(t, "2026-04-21-PR42-abcdef0", runsBase, worktreeBase)
	ctx43 := testRunContext(t, "2026-04-21-PR43-bcdef01", runsBase, worktreeBase)

	started42 := startedEntry(42, ctx42.RunID, time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC))
	step20 := stepDoneEntry(42, ctx42.RunID, contracts.FailedStep20, time.Date(2026, 4, 21, 10, 30, 0, 0, time.UTC))
	started43 := startedEntry(43, ctx43.RunID, time.Date(2026, 4, 21, 10, 45, 0, 0, time.UTC))
	step30 := stepDoneEntry(42, ctx42.RunID, contracts.FailedStep30, time.Date(2026, 4, 21, 11, 0, 0, 0, time.UTC))

	require.NoError(t, Append(ctx42, started42))
	require.NoError(t, Append(ctx42, step20))
	require.NoError(t, Append(ctx43, started43))
	require.NoError(t, Append(ctx42, step30))

	last, err := LastEventForPR(ctx42, 42)
	require.NoError(t, err)
	require.NotNil(t, last)
	assert.Equal(t, step30, *last)
}

func TestState_MultiplePRsInterleavedInSameFile(t *testing.T) {
	runsBase := t.TempDir()
	worktreeBase := t.TempDir()
	ctx42 := testRunContext(t, "2026-04-21-PR42-abcdef0", runsBase, worktreeBase)
	ctx43 := testRunContext(t, "2026-04-21-PR43-bcdef01", runsBase, worktreeBase)

	entries42 := []contracts.StateEntry{
		startedEntry(42, ctx42.RunID, time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC)),
		stepDoneEntry(42, ctx42.RunID, contracts.FailedStep20, time.Date(2026, 4, 21, 9, 30, 0, 0, time.UTC)),
	}
	entries43 := []contracts.StateEntry{
		startedEntry(43, ctx43.RunID, time.Date(2026, 4, 21, 9, 5, 0, 0, time.UTC)),
		completedEntry(43, ctx43.RunID, contracts.FailedStep70, time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)),
	}

	require.NoError(t, Append(ctx42, entries42[0]))
	require.NoError(t, Append(ctx43, entries43[0]))
	require.NoError(t, Append(ctx42, entries42[1]))
	require.NoError(t, Append(ctx43, entries43[1]))

	last42, err := LastEventForPR(ctx42, 42)
	require.NoError(t, err)
	require.NotNil(t, last42)
	assert.Equal(t, entries42[1], *last42)

	last43, err := LastEventForPR(ctx43, 43)
	require.NoError(t, err)
	require.NotNil(t, last43)
	assert.Equal(t, entries43[1], *last43)

	run42, err := LatestRunForPR(ctx42, 42)
	require.NoError(t, err)
	assert.Equal(t, NextActionResume, run42.Action)
	assert.Equal(t, ctx42.RunID, run42.RunID)
	assert.Equal(t, contracts.FailedStep20, run42.Step)

	run43, err := LatestRunForPR(ctx43, 43)
	require.NoError(t, err)
	assert.Equal(t, NextActionFreshStart, run43.Action)
	assert.Equal(t, ctx43.RunID, run43.RunID)
	assert.Equal(t, contracts.FailedStep70, run43.Step)

	scan42, err := ScanEventsForRun(ctx42, ctx42.RunID)
	require.NoError(t, err)
	assert.Equal(t, entries42, scan42)

	scan43, err := ScanEventsForRun(ctx43, ctx43.RunID)
	require.NoError(t, err)
	assert.Equal(t, entries43, scan43)
}

func TestReaderLastEventForPR_CorruptLineReturnsTypedError(t *testing.T) {
	ctx := testRunContext(t, "2026-04-21-PR42-abcdef0", t.TempDir(), t.TempDir())
	path := ctx.ProcessedPath()
	payload := strings.Join([]string{
		`{"kind":"started","pr":42,"run_id":"2026-04-21-PR42-abcdef0","step":"10","at":"2026-04-21T10:00:00Z"}`,
		`{"kind":"step_done","kind":"started","pr":42,"run_id":"2026-04-21-PR42-abcdef0","step":"20","at":"2026-04-21T10:30:00Z"}`,
	}, "\n") + "\n"
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(payload), 0o644))

	_, err := LastEventForPR(ctx, 42)
	require.Error(t, err)
	assert.True(t, errors.Is(err, contracts.ErrDuplicateJSONKey) || errors.Is(err, contracts.ErrUnknownStateKind))
}

func TestLatestRunForPR_ReturnsErrPartialStateLineForTruncatedTail(t *testing.T) {
	runsBase := t.TempDir()
	worktreeBase := t.TempDir()
	ctx := testRunContext(t, "2026-04-21-PR42-abcdef0", runsBase, worktreeBase)
	path := ctx.ProcessedPath()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))

	entry := startedEntry(42, ctx.RunID, time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC))
	payload, err := contracts.MarshalStrict(entry)
	require.NoError(t, err)
	fullLine := append(append([]byte{}, payload...), '\n')
	require.NoError(t, os.WriteFile(path, fullLine, 0o644))

	stepDone := stepDoneEntry(42, ctx.RunID, contracts.FailedStep20, time.Date(2026, 4, 21, 10, 1, 0, 0, time.UTC))
	stepPayload, err := contracts.MarshalStrict(stepDone)
	require.NoError(t, err)
	partial := stepPayload[:len(stepPayload)/2]
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	require.NoError(t, err)
	_, err = f.Write(partial)
	require.NoError(t, err)
	require.NoError(t, f.Sync())
	require.NoError(t, f.Close())

	_, err = LatestRunForPR(ctx, 42)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPartialStateLine)
}

func TestLatestRunForPR_IgnoresPartialTrailingLineWhileWriterHoldsStateLock(t *testing.T) {
	runsBase := t.TempDir()
	worktreeBase := t.TempDir()
	ctx := testRunContext(t, "2026-04-21-PR42-abcdef0", runsBase, worktreeBase)
	path := ctx.ProcessedPath()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))

	entry := startedEntry(42, ctx.RunID, time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC))
	payload, err := contracts.MarshalStrict(entry)
	require.NoError(t, err)
	fullLine := append(append([]byte{}, payload...), '\n')
	require.NoError(t, os.WriteFile(path, fullLine, 0o644))

	lock, err := internalio.AcquireFileLock(filepath.Join(filepath.Dir(path), "state.lock"))
	require.NoError(t, err)
	defer func() {
		require.NoError(t, lock.Unlock())
	}()

	stepDone := stepDoneEntry(42, ctx.RunID, contracts.FailedStep20, time.Date(2026, 4, 21, 10, 1, 0, 0, time.UTC))
	stepPayload, err := contracts.MarshalStrict(stepDone)
	require.NoError(t, err)
	partial := stepPayload[:len(stepPayload)/2]

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	require.NoError(t, err)
	_, err = f.Write(partial)
	require.NoError(t, err)
	require.NoError(t, f.Sync())
	require.NoError(t, f.Close())

	latest, err := LatestRunForPR(ctx, 42)
	require.NoError(t, err)
	require.NotNil(t, latest.LastEvent)
	assert.Equal(t, contracts.StateKindStarted, latest.LastEvent.Kind)
}

func TestResumeTargetPath_IgnoresPartialTrailingLineWhileWriterHoldsStateLock(t *testing.T) {
	runsBase := t.TempDir()
	worktreeBase := t.TempDir()
	ctx := testRunContext(t, "2026-04-21-PR42-abcdef0", runsBase, worktreeBase)
	path := ctx.ProcessedPath()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))

	entry := startedEntry(42, ctx.RunID, time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC))
	payload, err := contracts.MarshalStrict(entry)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, append(payload, '\n'), 0o644))

	lock, err := internalio.AcquireFileLock(filepath.Join(filepath.Dir(path), "state.lock"))
	require.NoError(t, err)
	defer func() {
		require.NoError(t, lock.Unlock())
	}()

	stepDone := stepDoneEntry(42, ctx.RunID, contracts.FailedStep20, time.Date(2026, 4, 21, 10, 1, 0, 0, time.UTC))
	stepPayload, err := contracts.MarshalStrict(stepDone)
	require.NoError(t, err)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	require.NoError(t, err)
	_, err = f.Write(stepPayload[:len(stepPayload)/2])
	require.NoError(t, err)
	require.NoError(t, f.Sync())
	require.NoError(t, f.Close())

	targets, err := ResumeTargetPath(path)
	require.NoError(t, err)
	require.Len(t, targets, 1)
	assert.Equal(t, 42, targets[0].PR)
	assert.Equal(t, contracts.FailedStep10, targets[0].Step)
}

func TestClassifyNextAction_CoversAllStateKinds(t *testing.T) {
	tests := map[contracts.StateKind]NextAction{
		contracts.StateKindStarted:                     NextActionResume,
		contracts.StateKindStepDone:                    NextActionResume,
		contracts.StateKindInterrupted:                 NextActionResume,
		contracts.StateKindPromoting:                   NextActionResume,
		contracts.StateKindWarningRegistrySizeHigh:     NextActionResume,
		contracts.StateKindWarningRegistrySizeCritical: NextActionResume,
		contracts.StateKindWarningRescueRetry:          NextActionResume,
		contracts.StateKindCompleted:                   NextActionFreshStart,
		contracts.StateKindFailed:                      NextActionFreshStart,
		contracts.StateKindPromoted:                    NextActionFreshStart,
		contracts.StateKindRollback:                    NextActionFreshStart,
		contracts.StateKindSkipped:                     NextActionFreshStart,
		contracts.StateKindTimeout:                     NextActionFreshStart,
		contracts.StateKindNeedsManualRecovery:         NextActionNeedsManualRecovery,
	}

	for kind, want := range tests {
		assert.Equal(t, want, NextActionForEntry(&contracts.StateEntry{Kind: kind}), string(kind))
	}
	assert.Equal(t, NextActionFreshStart, NextActionForEntry(nil))
}

func TestClassifyNextAction_PrefersTerminalBeforeTrailingWarnings(t *testing.T) {
	runID := contracts.RunID("2026-04-21-PR42-abcdef0")
	pr := 42
	step := contracts.FailedStep20
	events := []contracts.StateEntry{
		{
			Kind: contracts.StateKindWarningRescueRetry,
			Value: contracts.StateEntryWarning{
				Kind:  contracts.StateKindWarningRescueRetry,
				PR:    &pr,
				RunID: &runID,
				Step:  &step,
				At:    time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC),
			},
		},
		{
			Kind: contracts.StateKindNeedsManualRecovery,
			Value: contracts.StateEntryNeedsManualRecovery{
				Kind:       contracts.StateKindNeedsManualRecovery,
				PR:         pr,
				RunID:      runID,
				Step:       contracts.FailedStep50,
				Reason:     contracts.RollbackReasonWorktreeRescueLoop,
				FailedStep: contracts.FailedStep50,
				At:         time.Date(2026, 4, 21, 10, 1, 0, 0, time.UTC),
			},
		},
		{
			Kind: contracts.StateKindWarningRescueRetry,
			Value: contracts.StateEntryWarning{
				Kind:  contracts.StateKindWarningRescueRetry,
				PR:    &pr,
				RunID: &runID,
				Step:  &step,
				At:    time.Date(2026, 4, 21, 10, 2, 0, 0, time.UTC),
			},
		},
	}

	assert.Equal(t, NextActionNeedsManualRecovery, ClassifyNextAction(events))

	targets := ResumeTarget(events)
	assert.Nil(t, targets)
}

func TestAppend_WritesDetailOverflowSidecar(t *testing.T) {
	ctx := testRunContext(t, "2026-04-21-PR42-abcdef0", t.TempDir(), t.TempDir())
	detail := strings.Repeat("x", 320)
	entry := contracts.StateEntry{
		Kind: contracts.StateKindInterrupted,
		Value: contracts.StateEntryInterrupted{
			Kind:   contracts.StateKindInterrupted,
			PR:     42,
			RunID:  ctx.RunID,
			Step:   contracts.FailedStep30,
			Reason: contracts.InterruptedReasonUnknown,
			Detail: detail,
			At:     time.Date(2026, 4, 21, 12, 30, 0, 0, time.UTC),
		},
	}

	require.NoError(t, Append(ctx, entry))

	events, err := ScanEventsForRun(ctx, ctx.RunID)
	require.NoError(t, err)
	require.Len(t, events, 1)
	loaded := events[0].Value.(contracts.StateEntryInterrupted)
	assert.Len(t, []rune(loaded.Detail), 300)
	require.NotNil(t, loaded.DetailOverflowRef)
	assert.Equal(t, "processed-details", filepath.Dir(loaded.DetailOverflowRef.Path))

	content, err := internalio.ReadSidecar(ctx, *loaded.DetailOverflowRef)
	require.NoError(t, err)
	assert.Equal(t, detail, content)
}

func TestLastProcessedPRPath_UsesLatestTerminalEntries(t *testing.T) {
	runsBase := t.TempDir()
	worktreeBase := t.TempDir()
	ctx101 := testRunContext(t, "2026-04-21-PR101-abcdef0", runsBase, worktreeBase)
	ctx102 := testRunContext(t, "2026-04-21-PR102-bcdef01", runsBase, worktreeBase)
	ctx103 := testRunContext(t, "2026-04-21-PR103-cdef012", runsBase, worktreeBase)

	require.NoError(t, Append(ctx101, completedEntry(101, ctx101.RunID, contracts.FailedStep70, time.Date(2026, 4, 21, 11, 0, 0, 0, time.UTC))))
	require.NoError(t, Append(ctx102, startedEntry(102, ctx102.RunID, time.Date(2026, 4, 21, 11, 5, 0, 0, time.UTC))))
	require.NoError(t, Append(ctx103, completedEntry(103, ctx103.RunID, contracts.FailedStep70, time.Date(2026, 4, 21, 11, 10, 0, 0, time.UTC))))

	last, err := LastProcessedPRPath(ctx101.ProcessedPath())
	require.NoError(t, err)
	assert.Equal(t, 103, last)

	processed, err := TerminalPRSetPath(ctx101.ProcessedPath())
	require.NoError(t, err)
	assert.Contains(t, processed, 101)
	assert.Contains(t, processed, 103)
	assert.NotContains(t, processed, 102)
}

func testRunContext(t *testing.T, runID string, runsBase string, worktreeBase string) internalio.RunContext {
	t.Helper()
	ctx, err := internalio.NewRunContext(contracts.RunID(runID), runsBase, worktreeBase)
	require.NoError(t, err)
	return ctx
}

func startedEntry(pr int, runID contracts.RunID, at time.Time) contracts.StateEntry {
	return contracts.StateEntry{
		Kind: contracts.StateKindStarted,
		Value: contracts.StateEntryStarted{
			Kind:  contracts.StateKindStarted,
			PR:    pr,
			RunID: runID,
			Step:  contracts.FailedStep10,
			At:    at,
		},
	}
}

func stepDoneEntry(pr int, runID contracts.RunID, step contracts.FailedStep, at time.Time) contracts.StateEntry {
	return contracts.StateEntry{
		Kind: contracts.StateKindStepDone,
		Value: contracts.StateEntryStepDone{
			Kind:  contracts.StateKindStepDone,
			PR:    pr,
			RunID: runID,
			Step:  step,
			At:    at,
		},
	}
}

func completedEntry(pr int, runID contracts.RunID, step contracts.FailedStep, at time.Time) contracts.StateEntry {
	return contracts.StateEntry{
		Kind: contracts.StateKindCompleted,
		Value: contracts.StateEntryCompleted{
			Kind:  contracts.StateKindCompleted,
			PR:    pr,
			RunID: runID,
			Step:  step,
			At:    at,
		},
	}
}
