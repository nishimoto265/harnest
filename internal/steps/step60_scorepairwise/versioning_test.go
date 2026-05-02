package step60_scorepairwise

import (
	"context"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/judges"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun_RerunsWhenRubricVersionChanges(t *testing.T) {
	// Pass1 must already share step60's scoring versions; otherwise F8 fails
	// closed before rerun logic is exercised.
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score:    true,
		pass1RubricVersion: "rubric-v1",
		pass1PromptVersion: "prompt-v1",
	})
	now := time.Date(2026, 4, 21, 11, 30, 0, 0, time.UTC)
	agents := []contracts.AgentID{"a1", "a2", "a3"}

	require.NoError(t, Run(context.Background(), Input{
		IO:            runIO,
		TaskPackage:   &pkg,
		RubricVersion: "rubric-v1",
		PromptVersion: "prompt-v1",
		Primary:       judges.NewPrimaryStub(),
		Secondary:     judges.NewSecondaryStub(),
		Arbiter:       judges.NewArbiterStub(),
		Now:           func() time.Time { return now },
	}))

	// Simulate step30 being rerun at the new rubric version before step60
	// reruns — F8 demands matching pass1 version metadata.
	rewritePass1ScoresAt(t, runIO, pkg.RunID, agents, "rubric-v2", "prompt-v1")

	var called bool
	noJudge := unexpectedCallJudge{called: &called}
	err := Run(context.Background(), Input{
		IO:            runIO,
		TaskPackage:   &pkg,
		RubricVersion: "rubric-v2",
		PromptVersion: "prompt-v1",
		Primary:       noJudge,
		Secondary:     noJudge,
		Arbiter:       noJudge,
		Now:           func() time.Time { return now },
	})
	require.ErrorContains(t, err, "unexpected judge call")
	assert.True(t, called)
}

func TestRun_DerivesMissingRubricVersionFromPass1Scores(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score:    true,
		pass1RubricVersion: "rubric-hash-v1",
		pass1PromptVersion: "prompt-hash-v1",
	})
	now := time.Date(2026, 4, 21, 11, 35, 0, 0, time.UTC)

	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     judges.NewPrimaryStub(),
		Secondary:   judges.NewSecondaryStub(),
		Arbiter:     judges.NewArbiterStub(),
		Now:         func() time.Time { return now },
	}))

	scores := mustReadJSONL[contracts.ScoreEntry](t, runIO, "60/scores-B.jsonl")
	require.NotEmpty(t, scores)
	for _, score := range scores {
		assert.Equal(t, "rubric-hash-v1", score.RubricVersion)
		assert.Equal(t, "prompt-hash-v1", score.PromptVersion)
	}
}

// TestRun_FailsClosedOnPass1VersionMismatch is the F8 contract: when pass1
// scores were generated under a different rubric/prompt version than step60
// is running, pairwise winner classification is meaningless, so Run must
// abort with ErrPass1VersionMismatch before invoking any judge.
func TestRun_FailsClosedOnPass1VersionMismatch(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score:    true,
		pass1RubricVersion: "rubric-v1",
		pass1PromptVersion: "prompt-v1",
	})
	now := time.Date(2026, 4, 21, 11, 30, 0, 0, time.UTC)

	var called bool
	noJudge := unexpectedCallJudge{called: &called}
	err := Run(context.Background(), Input{
		IO:            runIO,
		TaskPackage:   &pkg,
		RubricVersion: "rubric-v2",
		PromptVersion: "prompt-v1",
		Primary:       noJudge,
		Secondary:     noJudge,
		Arbiter:       noJudge,
		Now:           func() time.Time { return now },
	})
	require.ErrorIs(t, err, ErrPass1VersionMismatch)
	assert.False(t, called, "judges must not run before pass1 version gate passes")
}

func TestRun_FailsClosedWhenJudgeProviderPromptVersionChanges(t *testing.T) {
	promptV1 := judges.PanelPromptVersion(
		"phase0-stub",
		versionedJudge{delegate: judges.NewPrimaryStub(), version: "provider-a"},
		versionedJudge{delegate: judges.NewSecondaryStub(), version: "provider-a"},
		versionedJudge{delegate: judges.NewArbiterStub(), version: "provider-a"},
	)
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score:    true,
		pass1PromptVersion: promptV1,
	})
	now := time.Date(2026, 4, 21, 11, 30, 0, 0, time.UTC)

	require.NoError(t, Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     versionedJudge{delegate: judges.NewPrimaryStub(), version: "provider-a"},
		Secondary:   versionedJudge{delegate: judges.NewSecondaryStub(), version: "provider-a"},
		Arbiter:     versionedJudge{delegate: judges.NewArbiterStub(), version: "provider-a"},
		Now:         func() time.Time { return now },
	}))

	var called bool
	noJudge := versionedJudge{delegate: unexpectedCallJudge{called: &called}, version: "provider-b"}
	err := Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     noJudge,
		Secondary:   noJudge,
		Arbiter:     noJudge,
		Now:         func() time.Time { return now },
	})
	require.ErrorIs(t, err, ErrPass1VersionMismatch)
	assert.False(t, called)
}

func TestRun_IgnoresHistoricalRawVersionsAfterMigration(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score:    true,
		pass1RubricVersion: "rubric-v1",
		pass1PromptVersion: "prompt-v1",
	})
	now := time.Date(2026, 4, 21, 11, 30, 0, 0, time.UTC)
	agents := []contracts.AgentID{"a1", "a2", "a3"}

	require.NoError(t, Run(context.Background(), Input{
		IO:            runIO,
		TaskPackage:   &pkg,
		RubricVersion: "rubric-v1",
		PromptVersion: "prompt-v1",
		Primary:       judges.NewPrimaryStub(),
		Secondary:     judges.NewSecondaryStub(),
		Arbiter:       judges.NewArbiterStub(),
		Now:           func() time.Time { return now },
	}))

	// Simulate step30 being rerun under the new rubric/prompt before step60
	// migrates forward. Without this step, F8 fails closed.
	rewritePass1ScoresAt(t, runIO, pkg.RunID, agents, "rubric-v2", "prompt-v2")

	require.NoError(t, Run(context.Background(), Input{
		IO:            runIO,
		TaskPackage:   &pkg,
		RubricVersion: "rubric-v2",
		PromptVersion: "prompt-v2",
		Primary:       judges.NewPrimaryStub(),
		Secondary:     judges.NewSecondaryStub(),
		Arbiter:       judges.NewArbiterStub(),
		Now:           func() time.Time { return now.Add(time.Hour) },
	}))

	var called bool
	noJudge := unexpectedCallJudge{called: &called}
	require.NoError(t, Run(context.Background(), Input{
		IO:            runIO,
		TaskPackage:   &pkg,
		RubricVersion: "rubric-v2",
		PromptVersion: "prompt-v2",
		Primary:       noJudge,
		Secondary:     noJudge,
		Arbiter:       noJudge,
		Now:           func() time.Time { return now.Add(2 * time.Hour) },
	}))
	assert.False(t, called)
}

// step30 appends a fresh versioned pass1 row set without truncating the
// historical rows. step60 must check the collapsed effective pass1 rows so
// a valid resume after that bump does not fail closed on the superseded
// old-version entries that still remain on disk.
func TestRun_AcceptsPass1AppendOnlyAfterVersionBump(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score:    true,
		pass1RubricVersion: "rubric-v1",
		pass1PromptVersion: "prompt-v1",
	})
	agents := []contracts.AgentID{"a1", "a2", "a3"}

	scoresPath := mustResolve(t, runIO, "30/scores-A.jsonl")
	for _, agent := range agents {
		for _, entry := range primaryStubScores(pkg.RunID, 1, agent) {
			entry.RubricVersion = "rubric-v2"
			entry.PromptVersion = "prompt-v2"
			require.NoError(t, internalio.AppendJSONL(scoresPath, entry))
		}
	}

	rows, err := internalio.ReadJSONL[contracts.ScoreEntry](scoresPath)
	require.NoError(t, err)
	assert.Equal(t, 2*len(agents)*len(canonicalDimensions), len(rows))

	now := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
	require.NoError(t, Run(context.Background(), Input{
		IO:            runIO,
		TaskPackage:   &pkg,
		RubricVersion: "rubric-v2",
		PromptVersion: "prompt-v2",
		Primary:       judges.NewPrimaryStub(),
		Secondary:     judges.NewSecondaryStub(),
		Arbiter:       judges.NewArbiterStub(),
		Now:           func() time.Time { return now },
	}))
}
