package detect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"time"

	"github.com/nishimoto265/auto-improve/internal/state"
)

const ghListLimit = 200

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

func (d Detector) DetectMergedPRs(ctx context.Context, lastProcessedPR int, repo string) ([]MergedPR, error) {
	if repo == "" {
		return nil, errors.New("detect: repo is required")
	}

	output, err := d.run(
		ctx,
		"gh",
		"pr",
		"list",
		"--repo", repo,
		"--state", "merged",
		"--limit", fmt.Sprintf("%d", ghListLimit),
		"--json", "number,title,baseRefName,mergedAt",
	)
	if err != nil {
		return nil, fmt.Errorf("detect: gh pr list failed: %w", err)
	}

	var raw []struct {
		Number      int       `json:"number"`
		Title       string    `json:"title"`
		BaseRefName string    `json:"baseRefName"`
		MergedAt    time.Time `json:"mergedAt"`
	}
	if err := json.Unmarshal(output, &raw); err != nil {
		return nil, err
	}

	processed, err := d.processedPRSet()
	if err != nil {
		return nil, err
	}

	prs := make([]MergedPR, 0, len(raw))
	for _, pr := range raw {
		if pr.Number <= lastProcessedPR {
			continue
		}
		if pr.BaseRefName != "main" && pr.BaseRefName != "master" {
			continue
		}
		if _, ok := processed[pr.Number]; ok {
			continue
		}
		prs = append(prs, MergedPR{
			Number:      pr.Number,
			Title:       pr.Title,
			BaseRefName: pr.BaseRefName,
			MergedAt:    pr.MergedAt,
		})
	}

	sort.Slice(prs, func(i, j int) bool {
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
