package step10restorebase

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
)

// LinkedIssue describes a GitHub issue that is closed by the PR (via closing
// keyword references).
type LinkedIssue struct {
	Number int
	Title  string
	Body   string
}

// PRInfo is the subset of `gh pr view` output that step10 consumes.
type PRInfo struct {
	Number                  int
	Title                   string
	Body                    string
	BaseRefOid              string // current base-branch tip; debugging only
	HeadRefOid              string // PR head tip; debugging only
	MergeCommitOID          string
	PotentialMergeCommitOID string
	LinkedIssues            []LinkedIssue
}

// GHClient abstracts the `gh` CLI so tests can stub it.
type GHClient interface {
	PRView(ctx context.Context, pr int, repo string) (PRInfo, error)
}

// ghCLI shells out to the real `gh` binary.
type ghCLI struct {
	run func(ctx context.Context, name string, args ...string) ([]byte, error)
}

// NewGHClient returns a GHClient backed by the real `gh` CLI.
func NewGHClient() GHClient {
	return ghCLI{run: defaultCmdRunner}
}

// NewGHClientWithRunner exposes the subprocess seam for tests.
func NewGHClientWithRunner(runner func(ctx context.Context, name string, args ...string) ([]byte, error)) GHClient {
	if runner == nil {
		runner = defaultCmdRunner
	}
	return ghCLI{run: runner}
}

func defaultCmdRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.CombinedOutput()
}

type ghPRViewRaw struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	BaseRefOid  string `json:"baseRefOid"`
	HeadRefOid  string `json:"headRefOid"`
	MergeCommit *struct {
		OID string `json:"oid"`
	} `json:"mergeCommit"`
	PotentialMergeCommit *struct {
		OID string `json:"oid"`
	} `json:"potentialMergeCommit"`
	ClosingIssuesReferences []struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
	} `json:"closingIssuesReferences"`
}

type ghIssueViewRaw struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
}

// PRView calls `gh pr view` for PR metadata and `gh issue view` for each linked
// issue. `repo` is in `owner/name` form (empty string = let gh infer).
func (c ghCLI) PRView(ctx context.Context, pr int, repo string) (PRInfo, error) {
	prArgs := []string{
		"pr", "view", fmt.Sprintf("%d", pr),
		"--json", "number,title,body,baseRefOid,headRefOid,mergeCommit,potentialMergeCommit,closingIssuesReferences",
	}
	if repo != "" {
		prArgs = append(prArgs, "--repo", repo)
	}
	out, err := c.run(ctx, "gh", prArgs...)
	if err != nil {
		return PRInfo{}, fmt.Errorf("step10: gh pr view #%d: %w: %s", pr, err, string(out))
	}
	var raw ghPRViewRaw
	if err := json.Unmarshal(out, &raw); err != nil {
		return PRInfo{}, fmt.Errorf("step10: gh pr view #%d: decode: %w", pr, err)
	}

	info := PRInfo{
		Number:     raw.Number,
		Title:      raw.Title,
		Body:       raw.Body,
		BaseRefOid: raw.BaseRefOid,
		HeadRefOid: raw.HeadRefOid,
	}
	if raw.MergeCommit != nil {
		info.MergeCommitOID = raw.MergeCommit.OID
	}
	if raw.PotentialMergeCommit != nil {
		info.PotentialMergeCommitOID = raw.PotentialMergeCommit.OID
	}
	for _, ref := range raw.ClosingIssuesReferences {
		issue, err := c.issueView(ctx, ref.Number, repo)
		if err != nil {
			return PRInfo{}, err
		}
		// Prefer the PR-side title if the issue view fails to populate it.
		if issue.Title == "" {
			issue.Title = ref.Title
		}
		info.LinkedIssues = append(info.LinkedIssues, issue)
	}
	return info, nil
}

func (c ghCLI) issueView(ctx context.Context, number int, repo string) (LinkedIssue, error) {
	args := []string{
		"issue", "view", fmt.Sprintf("%d", number),
		"--json", "number,title,body",
	}
	if repo != "" {
		args = append(args, "--repo", repo)
	}
	out, err := c.run(ctx, "gh", args...)
	if err != nil {
		return LinkedIssue{}, fmt.Errorf("step10: gh issue view #%d: %w: %s", number, err, string(out))
	}
	var raw ghIssueViewRaw
	if err := json.Unmarshal(out, &raw); err != nil {
		return LinkedIssue{}, fmt.Errorf("step10: gh issue view #%d: decode: %w", number, err)
	}
	return LinkedIssue{Number: raw.Number, Title: raw.Title, Body: raw.Body}, nil
}
