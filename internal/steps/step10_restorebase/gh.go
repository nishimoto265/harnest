package step10restorebase

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/nishimoto265/auto-improve/internal/contracts"
)

type LinkedIssue struct {
	Number int
	Title  string
	Body   string
}

type PRInfo struct {
	Number       int
	Title        string
	Body         string
	BaseRefOid   string
	HeadRefOid   string
	LinkedIssues []LinkedIssue
}

type GHClient interface {
	PRView(ctx context.Context, pr int, repoRoot string) (PRInfo, error)
}

type ghCommandRunner func(context.Context, string, ...string) ([]byte, error)

type RealGHClient struct {
	Repo string
	run  ghCommandRunner
}

func NewRealGHClient(repo string) GHClient {
	return &RealGHClient{
		Repo: repo,
		run: func(ctx context.Context, repoRoot string, args ...string) ([]byte, error) {
			cmd := exec.CommandContext(ctx, "gh", args...)
			cmd.Dir = repoRoot
			return cmd.CombinedOutput()
		},
	}
}

func (c *RealGHClient) PRView(ctx context.Context, pr int, repoRoot string) (PRInfo, error) {
	if pr <= 0 {
		return PRInfo{}, fmt.Errorf("gh pr view: pr must be > 0: pr=%d", pr)
	}
	if err := contracts.EnsureCleanAbsolutePath(repoRoot); err != nil {
		return PRInfo{}, fmt.Errorf("gh pr view: %w", err)
	}

	args := []string{
		"pr",
		"view",
		fmt.Sprintf("%d", pr),
		"--json",
		"number,title,body,baseRefOid,headRefOid,closingIssuesReferences",
	}
	if c.Repo != "" {
		args = append(args, "--repo", c.Repo)
	}
	output, err := c.run(ctx, repoRoot, args...)
	if err != nil {
		return PRInfo{}, wrapCommandError("gh pr view", err, output)
	}

	var raw struct {
		Number                  int    `json:"number"`
		Title                   string `json:"title"`
		Body                    string `json:"body"`
		BaseRefOid              string `json:"baseRefOid"`
		HeadRefOid              string `json:"headRefOid"`
		ClosingIssuesReferences []struct {
			Number int `json:"number"`
		} `json:"closingIssuesReferences"`
	}
	if err := json.Unmarshal(output, &raw); err != nil {
		return PRInfo{}, fmt.Errorf("gh pr view: decode response: %w", err)
	}

	issues := make([]LinkedIssue, 0, len(raw.ClosingIssuesReferences))
	for _, issueRef := range raw.ClosingIssuesReferences {
		issue, err := c.issueView(ctx, issueRef.Number, repoRoot)
		if err != nil {
			return PRInfo{}, err
		}
		issues = append(issues, issue)
	}

	return PRInfo{
		Number:       raw.Number,
		Title:        raw.Title,
		Body:         raw.Body,
		BaseRefOid:   raw.BaseRefOid,
		HeadRefOid:   raw.HeadRefOid,
		LinkedIssues: issues,
	}, nil
}

func (c *RealGHClient) issueView(ctx context.Context, issueNumber int, repoRoot string) (LinkedIssue, error) {
	args := []string{
		"issue",
		"view",
		fmt.Sprintf("%d", issueNumber),
		"--json",
		"number,title,body",
	}
	if c.Repo != "" {
		args = append(args, "--repo", c.Repo)
	}
	output, err := c.run(ctx, repoRoot, args...)
	if err != nil {
		return LinkedIssue{}, wrapCommandError("gh issue view", err, output)
	}

	var raw struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
	}
	if err := json.Unmarshal(output, &raw); err != nil {
		return LinkedIssue{}, fmt.Errorf("gh issue view: decode response: %w", err)
	}
	return LinkedIssue{
		Number: raw.Number,
		Title:  raw.Title,
		Body:   raw.Body,
	}, nil
}

func wrapCommandError(op string, err error, output []byte) error {
	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" {
		return fmt.Errorf("%s: %w", op, err)
	}
	return fmt.Errorf("%s: %w: %s", op, err, trimmed)
}
