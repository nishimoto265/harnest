package step30_score

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/nishimoto265/harnest/internal/judges"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStep30Score_RunSerializesConcurrentWriters(t *testing.T) {
	runCtx, pkg := seedStep30Fixtures(t, []contracts.AgentID{"a1", "a2", "a3"})
	primaryStarted := make(chan struct{})
	releasePrimary := make(chan struct{})
	var blockOnce sync.Once

	provider := &fakePanelProvider{
		outputs: func(input judges.JudgeInput, role contracts.JudgeRole) judges.JudgeOutput {
			if role == contracts.JudgeRolePrimary {
				blockOnce.Do(func() {
					close(primaryStarted)
					<-releasePrimary
				})
			}

			score := 80
			if role == contracts.JudgeRoleSecondary {
				score = 79
			}
			return makeJudgeOutput(input, role, score, []ruleVerdict{
				{ruleID: "rule-a", verdict: contracts.ComplianceVerdictCompliant},
			})
		},
	}

	step := New(WithPanelProvider(provider))
	setStepRubric(t, step, "rule-a")
	errCh := make(chan error, 2)

	go func() {
		errCh <- step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg})
	}()

	<-primaryStarted

	go func() {
		errCh <- step.Run(context.Background(), Request{RunContext: runCtx, TaskPackage: &pkg})
	}()

	select {
	case err := <-errCh:
		t.Fatalf("Run returned before lock release: %v", err)
	case <-time.After(150 * time.Millisecond):
	}

	assert.Equal(t, 1, provider.callCount(contracts.JudgeRolePrimary))

	close(releasePrimary)

	require.NoError(t, <-errCh)
	require.NoError(t, <-errCh)

	assert.Equal(t, 3, provider.callCount(contracts.JudgeRolePrimary))
	assert.Equal(t, 0, provider.callCount(contracts.JudgeRoleSecondary))
	assert.Equal(t, 0, provider.callCount(contracts.JudgeRoleArbiter))

	scoreRawPath, err := runCtx.ResolveRunRelative("30/scores-A-raw.jsonl")
	require.NoError(t, err)
	scoreRaw, err := internalio.ReadJSONL[contracts.RawScoreEntry](scoreRawPath)
	require.NoError(t, err)
	assert.Len(t, scoreRaw, 15)
}
