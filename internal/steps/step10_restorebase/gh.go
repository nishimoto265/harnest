package step10restorebase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// LinkedIssue describes a GitHub issue that is closed by the PR (via closing
// keyword references).
type LinkedIssue struct {
	Number int
	Title  string
	Body   string
}

const issueBodyMaxBytes = 8 * 1024

// PRInfo is the subset of `gh pr view` output that step10 consumes.
type PRInfo struct {
	Number                  int
	Title                   string
	Body                    string
	State                   string
	BaseRefOid              string // debug: current tip of the base branch
	HeadRefOid              string // debug: current tip of the head branch
	MergeCommitOID          string
	PotentialMergeCommitOID string
	LinkedIssues            []LinkedIssue
}

// GHClient abstracts the `gh` CLI so tests can stub it.
type GHClient interface {
	PRView(ctx context.Context, pr int, repo string) (PRInfo, error)
}

type cmdRunner func(ctx context.Context, name string, args ...string) (stdout []byte, stderr []byte, err error)

// ghCLI shells out to the real `gh` binary.
type ghCLI struct {
	run cmdRunner
}

// NewGHClient returns a GHClient backed by the real `gh` CLI.
func NewGHClient() GHClient {
	return ghCLI{run: defaultCmdRunner}
}

// NewGHClientWithRunner exposes the subprocess seam for tests.
func NewGHClientWithRunner(runner cmdRunner) GHClient {
	if runner == nil {
		runner = defaultCmdRunner
	}
	return ghCLI{run: runner}
}

func defaultCmdRunner(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	stdout, err := cmd.Output()
	if err == nil {
		return stdout, nil, nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return stdout, exitErr.Stderr, err
	}
	return stdout, nil, err
}

func formatCommandFailure(op string, err error, stdout, stderr []byte) error {
	parts := make([]string, 0, 2)
	if msg := strings.TrimSpace(string(stderr)); msg != "" {
		parts = append(parts, "stderr="+msg)
	}
	if msg := strings.TrimSpace(string(stdout)); msg != "" {
		parts = append(parts, "stdout="+msg)
	}
	if len(parts) == 0 {
		return fmt.Errorf("%s: %w", op, err)
	}
	return fmt.Errorf("%s: %w: %s", op, err, strings.Join(parts, "; "))
}

func validatePRInfo(pr int, info PRInfo) error {
	missing := make([]string, 0, 3)
	if info.Number <= 0 {
		missing = append(missing, "number")
	}
	if info.Title == "" {
		missing = append(missing, "title")
	}
	if info.State == "" {
		missing = append(missing, "state")
	}
	if len(missing) > 0 {
		return fmt.Errorf("step10: gh pr view #%d: missing required fields: %s", pr, strings.Join(missing, ", "))
	}
	return nil
}

type ghPRViewRaw struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	State       string `json:"state"`
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
		"--json", "number,title,body,state,baseRefOid,headRefOid,mergeCommit,potentialMergeCommit,closingIssuesReferences",
	}
	if repo != "" {
		prArgs = append(prArgs, "--repo", repo)
	}
	out, stderr, err := c.run(ctx, "gh", prArgs...)
	if err != nil {
		return PRInfo{}, formatCommandFailure(fmt.Sprintf("step10: gh pr view #%d", pr), err, out, stderr)
	}
	var raw ghPRViewRaw
	if err := json.Unmarshal(out, &raw); err != nil {
		return PRInfo{}, fmt.Errorf("step10: gh pr view #%d: decode: %w", pr, err)
	}

	info := PRInfo{
		Number:     raw.Number,
		Title:      raw.Title,
		Body:       raw.Body,
		State:      raw.State,
		BaseRefOid: raw.BaseRefOid,
		HeadRefOid: raw.HeadRefOid,
	}
	if raw.MergeCommit != nil {
		info.MergeCommitOID = raw.MergeCommit.OID
	}
	if raw.PotentialMergeCommit != nil {
		info.PotentialMergeCommitOID = raw.PotentialMergeCommit.OID
	}
	if err := validatePRInfo(pr, info); err != nil {
		return PRInfo{}, err
	}
	for _, ref := range raw.ClosingIssuesReferences {
		issue, err := c.issueView(ctx, ref.Number, repo)
		if err != nil {
			continue
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
	out, stderr, err := c.run(ctx, "gh", args...)
	if err != nil {
		return LinkedIssue{}, formatCommandFailure(fmt.Sprintf("step10: gh issue view #%d", number), err, out, stderr)
	}
	var raw ghIssueViewRaw
	if err := json.Unmarshal(out, &raw); err != nil {
		return LinkedIssue{}, fmt.Errorf("step10: gh issue view #%d: decode: %w", number, err)
	}
	return LinkedIssue{
		Number: raw.Number,
		Title:  raw.Title,
		Body:   truncateUTF8Bytes(raw.Body, issueBodyMaxBytes),
	}, nil
}
