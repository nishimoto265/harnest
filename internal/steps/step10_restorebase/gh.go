package step10restorebase

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
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

type subprocessRunner func(ctx context.Context, dir string, name string, args ...string) ([]byte, error)

type realGHClient struct {
	repo string
	run  subprocessRunner
}

func NewRealGHClient(repo string) GHClient {
	return &realGHClient{
		repo: repo,
		run:  runSubprocess,
	}
}

func (c *realGHClient) PRView(ctx context.Context, pr int, repoRoot string) (PRInfo, error) {
	args := []string{
		"pr",
		"view",
		strconv.Itoa(pr),
		"--json",
		"number,title,body,baseRefOid,headRefOid,closingIssuesReferences",
	}
	if c.repo != "" {
		args = append(args, "--repo", c.repo)
	}

	output, err := c.run(ctx, repoRoot, "gh", args...)
	if err != nil {
		return PRInfo{}, err
	}

	var raw struct {
		Number                  int    `json:"number"`
		Title                   string `json:"title"`
		Body                    string `json:"body"`
		BaseRefOid              string `json:"baseRefOid"`
		HeadRefOid              string `json:"headRefOid"`
		ClosingIssuesReferences []struct {
			Number int    `json:"number"`
			Title  string `json:"title"`
		} `json:"closingIssuesReferences"`
	}
	if err := json.Unmarshal(output, &raw); err != nil {
		return PRInfo{}, err
	}

	info := PRInfo{
		Number:     raw.Number,
		Title:      raw.Title,
		Body:       raw.Body,
		BaseRefOid: raw.BaseRefOid,
		HeadRefOid: raw.HeadRefOid,
	}
	for _, issueRef := range raw.ClosingIssuesReferences {
		issue, err := c.issueView(ctx, repoRoot, issueRef.Number)
		if err != nil {
			return PRInfo{}, err
		}
		info.LinkedIssues = append(info.LinkedIssues, issue)
	}

	return info, nil
}

func (c *realGHClient) issueView(ctx context.Context, repoRoot string, issueNumber int) (LinkedIssue, error) {
	args := []string{
		"issue",
		"view",
		strconv.Itoa(issueNumber),
		"--json",
		"number,title,body",
	}
	if c.repo != "" {
		args = append(args, "--repo", c.repo)
	}

	output, err := c.run(ctx, repoRoot, "gh", args...)
	if err != nil {
		return LinkedIssue{}, fmt.Errorf("gh issue view #%d: %w", issueNumber, err)
	}

	var raw struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
	}
	if err := json.Unmarshal(output, &raw); err != nil {
		return LinkedIssue{}, err
	}

	return LinkedIssue{
		Number: raw.Number,
		Title:  raw.Title,
		Body:   raw.Body,
	}, nil
}

func runSubprocess(ctx context.Context, dir string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err == nil {
		return output, nil
	}
	message := strings.TrimSpace(string(output))
	if message == "" {
		return nil, err
	}
	return nil, fmt.Errorf("%w: %s", err, message)
}
