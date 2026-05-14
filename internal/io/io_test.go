package io

import (
	"fmt"
	stdio "io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nishimoto265/harnest/internal/contracts"
	"github.com/stretchr/testify/require"
)

type testJSONLRecord struct {
	Name string `json:"name"`
}

func realTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	real, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	return real
}

func newTestRunContext(t *testing.T) RunContext {
	t.Helper()

	runsBase := realTempDir(t)
	worktreeBase := realTempDir(t)
	ctx, err := NewRunContext("2026-04-21-PR42-abcdef0", runsBase, worktreeBase)
	require.NoError(t, err)
	return ctx
}

func testTaskPackage(t *testing.T, runsBase, worktreeBase string) contracts.TaskPackage {
	t.Helper()

	runID := contracts.RunID("2026-04-21-PR42-abcdef0")
	worktrees := make([]contracts.WorktreeAllocation, 0, 6)
	for pass := 1; pass <= 2; pass++ {
		for agentNum := 1; agentNum <= 3; agentNum++ {
			agent := contracts.AgentID(fmt.Sprintf("a%d", agentNum))
			worktrees = append(worktrees, contracts.WorktreeAllocation{
				Agent:   agent,
				Pass:    pass,
				Path:    filepath.Join(worktreeBase, fmt.Sprintf("%s-pass%d-%s", runID, pass, agent)),
				Branch:  fmt.Sprintf("run/%s/pass%d", agent, pass),
				BaseSHA: strings.Repeat("1", 40),
				HeadSHA: strings.Repeat("1", 40),
			})
		}
	}

	pkg := contracts.TaskPackage{
		SchemaVersion:           "1",
		RunID:                   runID,
		PR:                      42,
		Title:                   "test",
		BaseSHA:                 strings.Repeat("1", 40),
		BestBranch:              "best/main",
		ReconstructedTaskPrompt: "do thing",
		Worktrees:               worktrees,
		CreatedAt:               time.Unix(100, 0).UTC(),
	}
	require.NoError(t, pkg.Validate())
	return pkg
}

type failingAppendFile struct {
	*os.File
	remaining     int
	err           error
	syncCalls     int
	truncateCalls int
}

func (f *failingAppendFile) Write(p []byte) (int, error) {
	if f.remaining <= 0 {
		return 0, f.err
	}
	if len(p) > f.remaining {
		n, err := f.File.Write(p[:f.remaining])
		f.remaining -= n
		if err != nil {
			return n, err
		}
		return n, f.err
	}
	n, err := f.File.Write(p)
	f.remaining -= n
	if err != nil {
		return n, err
	}
	if f.remaining == 0 {
		return n, f.err
	}
	return n, nil
}

func (f *failingAppendFile) Sync() error {
	f.syncCalls++
	return f.File.Sync()
}

func (f *failingAppendFile) Truncate(size int64) error {
	f.truncateCalls++
	return f.File.Truncate(size)
}

var _ appendJSONLFile = (*failingAppendFile)(nil)

var _ stdio.Writer = (*failingAppendFile)(nil)
