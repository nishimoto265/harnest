package main

import (
	"context"
	"testing"

	"github.com/nishimoto265/harnest/internal/orchestrator"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubPipelineRunner struct {
	prs   []int
	opts  []orchestrator.RunOptions
	onRun func(pr int, opts orchestrator.RunOptions) error
}

func (r *stubPipelineRunner) Run(_ context.Context, pr int, opts orchestrator.RunOptions) error {
	r.prs = append(r.prs, pr)
	r.opts = append(r.opts, opts)
	if r.onRun != nil {
		return r.onRun(pr, opts)
	}
	return nil
}

type pipelineRunnerFunc func(context.Context, int, orchestrator.RunOptions) error

func (f pipelineRunnerFunc) Run(ctx context.Context, pr int, opts orchestrator.RunOptions) error {
	return f(ctx, pr, opts)
}

func assertCommandExitCode(t *testing.T, err error, code int) {
	t.Helper()
	var exitErr interface{ ExitCode() int }
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, code, exitErr.ExitCode())
}
