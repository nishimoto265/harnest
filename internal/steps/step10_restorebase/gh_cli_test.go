package step10restorebase

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGHCLIPRView_EmptyObjectRejected(t *testing.T) {
	gh := ghCLI{
		run: func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
			require.Equal(t, "gh", name)
			return []byte(`{}`), nil, nil
		},
	}

	_, err := gh.PRView(context.Background(), 42, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing required fields")
	assert.Contains(t, err.Error(), "number")
	assert.Contains(t, err.Error(), "title")
	assert.Contains(t, err.Error(), "state")
}

func TestGHCLIPRView_EmptyTitleRejected(t *testing.T) {
	gh := ghCLI{
		run: func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
			require.Equal(t, "gh", name)
			return []byte(`{"number":42,"title":"","state":"MERGED"}`), nil, nil
		},
	}

	_, err := gh.PRView(context.Background(), 42, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing required fields")
	assert.Contains(t, err.Error(), "title")
}

func TestGHCLIPRView_IssueViewFailureIsBestEffort(t *testing.T) {
	gh := ghCLI{
		run: func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
			require.Equal(t, "gh", name)
			switch {
			case slices.Equal(args[:2], []string{"pr", "view"}):
				return []byte(`{"number":42,"title":"PR","body":"body","state":"MERGED","closingIssuesReferences":[{"number":7,"title":"Issue title"}]}`), nil, nil
			case slices.Equal(args[:2], []string{"issue", "view"}):
				return nil, []byte("boom"), errors.New("exit status 1")
			default:
				t.Fatalf("unexpected gh args: %v", args)
				return nil, nil, nil
			}
		},
	}

	info, err := gh.PRView(context.Background(), 42, "")
	require.NoError(t, err)
	require.Len(t, info.LinkedIssues, 1)
	assert.Equal(t, 7, info.LinkedIssues[0].Number)
	assert.Equal(t, "Issue title", info.LinkedIssues[0].Title)
	assert.Equal(t, "[issue #7: fetch failed]", info.LinkedIssues[0].Body)
}

func TestGHCLIPRView_CapsIssueBodySize(t *testing.T) {
	largeBody := strings.Repeat("x", issueBodyMaxBytes+1024)
	gh := ghCLI{
		run: func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
			require.Equal(t, "gh", name)
			switch {
			case slices.Equal(args[:2], []string{"pr", "view"}):
				return []byte(`{"number":42,"title":"PR","body":"body","state":"MERGED","closingIssuesReferences":[{"number":7,"title":"Issue title"}]}`), nil, nil
			case slices.Equal(args[:2], []string{"issue", "view"}):
				return []byte(fmt.Sprintf(`{"number":7,"title":"Issue title","body":"%s"}`, largeBody)), nil, nil
			default:
				t.Fatalf("unexpected gh args: %v", args)
				return nil, nil, nil
			}
		},
	}

	info, err := gh.PRView(context.Background(), 42, "")
	require.NoError(t, err)
	require.Len(t, info.LinkedIssues, 1)
	assert.LessOrEqual(t, len(info.LinkedIssues[0].Body), issueBodyMaxBytes)
}

func TestLinkedIssuePromptBytesIsPositiveForShortIssues(t *testing.T) {
	got := linkedIssuePromptBytes(LinkedIssue{Number: 7, Title: "t", Body: "b"})

	assert.Greater(t, got, 0)
}

func TestGHCLIPRView_CapsLinkedIssueFetchesAtTen(t *testing.T) {
	var issueCalls int
	refs := make([]string, 0, 12)
	for i := 1; i <= 12; i++ {
		refs = append(refs, fmt.Sprintf(`{"number":%d,"title":"Issue %d"}`, i, i))
	}
	gh := ghCLI{
		run: func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
			require.Equal(t, "gh", name)
			switch {
			case slices.Equal(args[:2], []string{"pr", "view"}):
				return []byte(fmt.Sprintf(`{"number":42,"title":"PR","body":"body","state":"MERGED","closingIssuesReferences":[%s]}`, strings.Join(refs, ","))), nil, nil
			case slices.Equal(args[:2], []string{"issue", "view"}):
				issueCalls++
				return []byte(`{"number":7,"title":"Issue","body":"body"}`), nil, nil
			default:
				t.Fatalf("unexpected gh args: %v", args)
				return nil, nil, nil
			}
		},
	}

	info, err := gh.PRView(context.Background(), 42, "")
	require.NoError(t, err)
	assert.Len(t, info.LinkedIssues, 10)
	assert.Equal(t, 10, issueCalls)
}

func TestGHCLIPRView_StopsFetchingWhenPromptBudgetIsExhausted(t *testing.T) {
	var issueCalls int
	gh := ghCLI{
		run: func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
			require.Equal(t, "gh", name)
			switch {
			case slices.Equal(args[:2], []string{"pr", "view"}):
				return []byte(fmt.Sprintf(`{"number":42,"title":"PR","body":"%s","state":"MERGED","closingIssuesReferences":[{"number":7,"title":"Issue title"}]}`, strings.Repeat("x", reconstructedPromptMaxBytes))), nil, nil
			case slices.Equal(args[:2], []string{"issue", "view"}):
				issueCalls++
				return []byte(`{"number":7,"title":"Issue","body":"body"}`), nil, nil
			default:
				t.Fatalf("unexpected gh args: %v", args)
				return nil, nil, nil
			}
		},
	}

	info, err := gh.PRView(context.Background(), 42, "")
	require.NoError(t, err)
	assert.Empty(t, info.LinkedIssues)
	assert.Zero(t, issueCalls)
}
