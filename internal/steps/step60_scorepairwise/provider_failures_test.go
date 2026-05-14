package step60_scorepairwise

import (
	"context"
	"testing"
	"time"

	"github.com/nishimoto265/harnest/internal/contracts"
	"github.com/nishimoto265/harnest/internal/judges"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun_NoScorableAgentsReturnsTypedError(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		nonScorablePass2Agents: map[contracts.AgentID]bool{"a1": true, "a2": true, "a3": true},
	})

	err := Run(context.Background(), Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary:     judges.NewPrimaryStub(),
		Secondary:   judges.NewSecondaryStub(),
		Arbiter:     judges.NewArbiterStub(),
		Now:         func() time.Time { return time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC) },
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNoScorablePass2Agents)
	assert.NoFileExists(t, mustResolve(t, runIO, "60/done.marker"))
	assert.NoFileExists(t, mustResolve(t, runIO, "60/scores-B-raw.jsonl"))
}

func TestRun_SerializesConcurrentWriters(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score: true,
	})
	started := make(chan struct{})
	release := make(chan struct{})
	primary := &blockingJudge{
		delegate: scriptedJudge{
			score:        80,
			reasonPrefix: "primary",
			compliance:   map[string]contracts.ComplianceVerdict{"shared": contracts.ComplianceVerdictCompliant},
		},
		started: started,
		release: release,
	}

	errCh := make(chan error, 2)
	go func() {
		errCh <- Run(context.Background(), Input{
			IO:          runIO,
			TaskPackage: &pkg,
			Primary:     primary,
			Secondary: scriptedJudge{
				score:        80,
				reasonPrefix: "secondary",
				compliance:   map[string]contracts.ComplianceVerdict{"shared": contracts.ComplianceVerdictCompliant},
			},
			Arbiter: scriptedJudge{
				score:        80,
				reasonPrefix: "arbiter",
				compliance:   map[string]contracts.ComplianceVerdict{"shared": contracts.ComplianceVerdictCompliant},
			},
		})
	}()

	<-started

	go func() {
		errCh <- Run(context.Background(), Input{
			IO:          runIO,
			TaskPackage: &pkg,
			Primary:     primary,
			Secondary: scriptedJudge{
				score:        80,
				reasonPrefix: "secondary",
				compliance:   map[string]contracts.ComplianceVerdict{"shared": contracts.ComplianceVerdictCompliant},
			},
			Arbiter: scriptedJudge{
				score:        80,
				reasonPrefix: "arbiter",
				compliance:   map[string]contracts.ComplianceVerdict{"shared": contracts.ComplianceVerdictCompliant},
			},
		})
	}()

	select {
	case err := <-errCh:
		t.Fatalf("Run returned before lock release: %v", err)
	case <-time.After(150 * time.Millisecond):
	}

	assert.EqualValues(t, 1, primary.callCount())

	close(release)

	require.NoError(t, <-errCh)
	require.NoError(t, <-errCh)
	assert.GreaterOrEqual(t, primary.callCount(), int32(3))
}

func TestRun_StopsBeforeSecondaryJudgeWhenContextIsCanceled(t *testing.T) {
	runIO, pkg := seedStep60Fixture(t, fixtureOptions{
		writePass1Score:        true,
		nonScorablePass2Agents: map[contracts.AgentID]bool{"a2": true, "a3": true},
	})

	ctx, cancel := context.WithCancel(context.Background())
	secondaryCalled := false

	err := Run(ctx, Input{
		IO:          runIO,
		TaskPackage: &pkg,
		Primary: cancelingJudge{
			delegate: scriptedJudge{
				score:        80,
				reasonPrefix: "primary",
				compliance:   map[string]contracts.ComplianceVerdict{"shared": contracts.ComplianceVerdictCompliant},
			},
			cancel: cancel,
		},
		Secondary: unexpectedCallJudge{called: &secondaryCalled},
		Arbiter:   unexpectedCallJudge{},
		Now:       func() time.Time { return time.Date(2026, 4, 21, 20, 0, 0, 0, time.UTC) },
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.False(t, secondaryCalled)
	assert.NoFileExists(t, mustResolve(t, runIO, "60/done.marker"))
}
