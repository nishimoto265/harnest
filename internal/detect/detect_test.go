package detect

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
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
		return []byte(`[
			{"number":99,"title":"too old","baseRefName":"main","mergedAt":"2026-04-21T10:00:00Z"},
			{"number":101,"title":"wrong branch","baseRefName":"develop","mergedAt":"2026-04-21T11:00:00Z"},
			{"number":102,"title":"already processed","baseRefName":"main","mergedAt":"2026-04-21T12:00:00Z"},
			{"number":104,"title":"include main","baseRefName":"main","mergedAt":"2026-04-21T13:00:00Z"},
			{"number":103,"title":"include master","baseRefName":"master","mergedAt":"2026-04-21T12:30:00Z"}
		]`), nil
	}

	prs, err := NewWithRunner(processedPath, runner).DetectMergedPRs(context.Background(), 100, "owner/repo")
	require.NoError(t, err)
	require.Len(t, prs, 2)
	assert.Equal(t, []int{103, 104}, []int{prs[0].Number, prs[1].Number})
	assert.Equal(t, "master", prs[0].BaseRefName)
	assert.Equal(t, "main", prs[1].BaseRefName)
}
