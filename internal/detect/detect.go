package detect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"sort"
	"time"

	"github.com/nishimoto265/auto-improve/internal/state"
)

type MergedPR struct {
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	BaseRefName string    `json:"base_ref_name"`
	MergedAt    time.Time `json:"merged_at"`
}

type commandRunner func(context.Context, string, ...string) ([]byte, error)

type Detector struct {
	processedPath string
	run           commandRunner
}

func New(processedPath string) Detector {
	return NewWithRunner(processedPath, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		return cmd.CombinedOutput()
	})
}

func NewWithRunner(processedPath string, runner commandRunner) Detector {
	if runner == nil {
		runner = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			cmd := exec.CommandContext(ctx, name, args...)
			return cmd.CombinedOutput()
		}
	}
	return Detector{
		processedPath: processedPath,
		run:           runner,
	}
}

func (d Detector) DetectMergedPRs(ctx context.Context, repo string, defaultBranch string) ([]MergedPR, error) {
	if repo == "" {
		return nil, errors.New("detect: repo is required")
	}
	if defaultBranch == "" {
		return nil, errors.New("detect: default_branch is required")
	}

	endpoint := fmt.Sprintf(
		"repos/%s/pulls?state=closed&base=%s&per_page=100&sort=updated&direction=desc",
		repo,
		url.QueryEscape(defaultBranch),
	)
	output, err := d.run(
		ctx,
		"gh",
		"api",
		"--paginate",
		"--slurp",
		endpoint,
	)
	if err != nil {
		return nil, fmt.Errorf("detect: gh api pulls failed: %w", err)
	}

	var pages [][]struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Base   struct {
			Ref string `json:"ref"`
		} `json:"base"`
		MergedAt *time.Time `json:"merged_at"`
	}
	if err := json.Unmarshal(output, &pages); err != nil {
		return nil, err
	}

	processed, err := d.processedPRSet()
	if err != nil {
		return nil, err
	}

	prs := make([]MergedPR, 0, len(pages)*100)
	seen := make(map[int]struct{})
	for _, page := range pages {
		for _, pr := range page {
			if pr.MergedAt == nil {
				continue
			}
			if pr.Base.Ref != defaultBranch {
				continue
			}
			if _, ok := processed[pr.Number]; ok {
				continue
			}
			if _, ok := seen[pr.Number]; ok {
				continue
			}
			seen[pr.Number] = struct{}{}
			prs = append(prs, MergedPR{
				Number:      pr.Number,
				Title:       pr.Title,
				BaseRefName: pr.Base.Ref,
				MergedAt:    *pr.MergedAt,
			})
		}
	}

	sort.Slice(prs, func(i, j int) bool {
		if !prs[i].MergedAt.Equal(prs[j].MergedAt) {
			return prs[i].MergedAt.Before(prs[j].MergedAt)
		}
		return prs[i].Number < prs[j].Number
	})
	return prs, nil
}

func (d Detector) processedPRSet() (map[int]struct{}, error) {
	if d.processedPath == "" {
		return nil, nil
	}
	return state.TerminalPRSetPath(d.processedPath)
}
