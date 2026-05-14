package main

import (
	"context"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSunsetCommandInvokesRunner(t *testing.T) {
	originalRunSunsetTick := runSunsetTick
	called := false
	runSunsetTick = func(context.Context, bool) error {
		called = true
		return nil
	}
	t.Cleanup(func() { runSunsetTick = originalRunSunsetTick })

	cmd := newRootCmd()
	cmd.SetArgs([]string{"sunset"})
	require.NoError(t, cmd.Execute())
	assert.True(t, called)
}
func TestSunsetCommandPassesForce(t *testing.T) {
	originalRunSunsetTick := runSunsetTick
	var gotForce bool
	runSunsetTick = func(_ context.Context, force bool) error {
		gotForce = force
		return nil
	}
	t.Cleanup(func() { runSunsetTick = originalRunSunsetTick })

	cmd := newRootCmd()
	cmd.SetArgs([]string{"sunset", "--force"})
	require.NoError(t, cmd.Execute())
	assert.True(t, gotForce)
}
