package detect

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func realTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	real, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	return real
}

func TestDetectMergedPRsParsesAndFiltersPRs(t *testing.T) {
	processedPath := filepath.Join(realTempDir(t), "processed.jsonl")
	require.NoError(t, internalio.AppendJSONL(processedPath, contracts.StateEntry{
		Kind: contracts.StateKindCompleted,
		Value: contracts.StateEntryCompleted{
			Kind:  contracts.StateKindCompleted,
			PR:    102,
			RunID: contracts.RunID("2026-04-21-PR102-abcdef0"),
			Step:  contracts.FailedStep70,
			At:    time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
		},
	}))

	runner := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name != "gh" {
			return nil, fmt.Errorf("unexpected binary: %s", name)
		}
		return []byte(`[[
			{"number":99,"title":"too old","base":{"ref":"main"},"merged_at":"2026-04-21T10:00:00Z"},
			{"number":101,"title":"wrong branch","base":{"ref":"develop"},"merged_at":"2026-04-21T11:00:00Z"},
			{"number":102,"title":"already processed","base":{"ref":"main"},"merged_at":"2026-04-21T12:00:00Z"},
			{"number":104,"title":"include main","base":{"ref":"main"},"merged_at":"2026-04-21T13:00:00Z"},
			{"number":103,"title":"include master","base":{"ref":"master"},"merged_at":"2026-04-21T12:30:00Z"}
		]]`), nil
	}

	prs, err := NewWithRunner(processedPath, runner).DetectMergedPRs(context.Background(), "owner/repo", "main")
	require.NoError(t, err)
	require.Len(t, prs, 2)
	assert.Equal(t, []int{99, 104}, []int{prs[0].Number, prs[1].Number})
	assert.Equal(t, "main", prs[0].BaseRefName)
	assert.Equal(t, "main", prs[1].BaseRefName)
}

func TestDetectMergedPRsUsesConfiguredDefaultBranch(t *testing.T) {
	processedPath := filepath.Join(realTempDir(t), "processed.jsonl")
	runner := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name != "gh" {
			return nil, fmt.Errorf("unexpected binary: %s", name)
		}
		return []byte(`[[
			{"number":201,"title":"default branch","base":{"ref":"develop"},"merged_at":"2026-04-21T13:00:00Z"},
			{"number":202,"title":"other branch","base":{"ref":"main"},"merged_at":"2026-04-21T13:05:00Z"}
		]]`), nil
	}

	prs, err := NewWithRunner(processedPath, runner).DetectMergedPRs(context.Background(), "owner/repo", "develop")
	require.NoError(t, err)
	require.Len(t, prs, 1)
	assert.Equal(t, 201, prs[0].Number)
	assert.Equal(t, "develop", prs[0].BaseRefName)
}

func TestDetectMergedPRsRejectsMissingDefaultBranch(t *testing.T) {
	processedPath := filepath.Join(realTempDir(t), "processed.jsonl")
	runner := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nil, fmt.Errorf("runner should not be called")
	}

	_, err := NewWithRunner(processedPath, runner).DetectMergedPRs(context.Background(), "owner/repo", "")
	require.Error(t, err)
	assert.ErrorContains(t, err, "default_branch is required")
}

func TestDetectMergedPRsIncludesLateLowerNumberAndPaginates(t *testing.T) {
	processedPath := filepath.Join(realTempDir(t), "processed.jsonl")
	require.NoError(t, internalio.AppendJSONL(processedPath, contracts.StateEntry{
		Kind: contracts.StateKindCompleted,
		Value: contracts.StateEntryCompleted{
			Kind:  contracts.StateKindCompleted,
			PR:    200,
			RunID: contracts.RunID("2026-04-21-PR200-abcdef0"),
			Step:  contracts.FailedStep70,
			At:    time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
		},
	}))

	runner := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name != "gh" {
			return nil, fmt.Errorf("unexpected binary: %s", name)
		}
		return []byte(`[
			[
				{"number":200,"title":"already processed","base":{"ref":"main"},"merged_at":"2026-04-21T12:00:00Z"},
				{"number":205,"title":"newer page one","base":{"ref":"main"},"merged_at":"2026-04-21T13:00:00Z"}
			],
			[
				{"number":199,"title":"late lower number","base":{"ref":"main"},"merged_at":"2026-04-21T12:30:00Z"}
			]
		]`), nil
	}

	prs, err := NewWithRunner(processedPath, runner).DetectMergedPRs(context.Background(), "owner/repo", "main")
	require.NoError(t, err)
	require.Len(t, prs, 2)
	assert.Equal(t, []int{199, 205}, []int{prs[0].Number, prs[1].Number})
}

func TestDefaultRunnerUsesSanitizedNetworkEnv(t *testing.T) {
	t.Setenv("HARNEST_DETECT_HELPER", "1")
	t.Setenv("GH_TOKEN", "gh-secret")
	t.Setenv("BASH_ENV", "/tmp/evil")
	t.Setenv("GIT_CONFIG_GLOBAL", "/tmp/gitconfig")

	originalCommandContext := detectCommandContext
	detectCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, os.Args[0], "-test.run=TestDetectDefaultRunnerHelper", "--")
	}
	t.Cleanup(func() {
		detectCommandContext = originalCommandContext
	})

	prs, err := New("").DetectMergedPRs(context.Background(), "owner/repo", "main")
	require.NoError(t, err)
	assert.Empty(t, prs)
}

func TestDetectDefaultRunnerHelper(t *testing.T) {
	if os.Getenv("HARNEST_DETECT_HELPER") != "1" {
		return
	}
	if os.Getenv("BASH_ENV") != "" {
		fmt.Fprintln(os.Stderr, "BASH_ENV leaked")
		os.Exit(2)
	}
	if os.Getenv("GIT_CONFIG_GLOBAL") != "" {
		fmt.Fprintln(os.Stderr, "GIT_CONFIG_GLOBAL leaked")
		os.Exit(3)
	}
	if os.Getenv("GH_TOKEN") != "gh-secret" {
		fmt.Fprintln(os.Stderr, "GH_TOKEN missing")
		os.Exit(4)
	}
	if !strings.Contains(os.Getenv("PATH"), "/usr/bin:/bin") {
		fmt.Fprintln(os.Stderr, "PATH not sanitized")
		os.Exit(5)
	}
	fmt.Print(`[[]]`)
	os.Exit(0)
}
