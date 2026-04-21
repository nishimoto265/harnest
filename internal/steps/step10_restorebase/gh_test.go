package step10restorebase

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGHCLIPRView_RejectsEmptyObject(t *testing.T) {
	client := ghCLI{
		run: func(ctx context.Context, name string, args ...string) (cmdResult, error) {
			return cmdResult{stdout: []byte("{}")}, nil
		},
	}

	_, err := client.PRView(context.Background(), 42, "owner/repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid gh pr view response")
	assert.Contains(t, err.Error(), "number")
	assert.Contains(t, err.Error(), "title")
	assert.Contains(t, err.Error(), "state")
}

func TestGHCLIPRView_RejectsEmptyTitle(t *testing.T) {
	client := ghCLI{
		run: func(ctx context.Context, name string, args ...string) (cmdResult, error) {
			return cmdResult{stdout: []byte(`{"number":42,"title":"","state":"MERGED"}`)}, nil
		},
	}

	_, err := client.PRView(context.Background(), 42, "owner/repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid gh pr view response")
	assert.Contains(t, err.Error(), "title")
}
