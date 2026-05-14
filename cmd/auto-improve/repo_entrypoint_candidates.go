package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/nishimoto265/harnest/internal/config"
	"github.com/nishimoto265/harnest/internal/detect"
)

func selectRepoEntrypointCandidates(ctx context.Context, cfg config.Config, processedPath string) ([]repoCandidate, []repoSkippedPR, error) {
	prs, err := repoEntrypointMergedPRs(ctx, cfg.Repo.GitHub, cfg.Repo.DefaultBranch, processedPath)
	if err != nil {
		return nil, nil, err
	}
	candidates := make([]repoCandidate, 0, len(prs))
	skipped := make([]repoSkippedPR, 0)
	for _, pr := range prs {
		files, err := repoEntrypointPRFiles(ctx, cfg.Repo.GitHub, pr.Number)
		if err != nil {
			return nil, nil, err
		}
		pr.Files = files
		if len(files) > 0 && filesAreDocsOnly(files) {
			skipped = append(skipped, repoSkippedPR{Number: pr.Number, Title: pr.Title, Reason: "docs_only"})
			continue
		}
		candidates = append(candidates, pr)
	}
	return candidates, skipped, nil
}

func resolveExplicitRepoEntrypointPRs(ctx context.Context, cfg config.Config, prs []int) ([]repoCandidate, []repoSkippedPR, error) {
	selected := make([]repoCandidate, 0, len(prs))
	skipped := make([]repoSkippedPR, 0)
	for _, pr := range prs {
		files, err := repoEntrypointPRFiles(ctx, cfg.Repo.GitHub, pr)
		if err != nil {
			return nil, nil, err
		}
		if len(files) > 0 && filesAreDocsOnly(files) {
			skipped = append(skipped, repoSkippedPR{Number: pr, Reason: "docs_only"})
			continue
		}
		selected = append(selected, repoCandidate{Number: pr, Files: files})
	}
	return selected, skipped, nil
}

func listMergedPRsForRepoEntrypoint(ctx context.Context, repo, defaultBranch, processedPath string) ([]repoCandidate, error) {
	prs, err := detect.New(processedPath).DetectMergedPRs(ctx, repo, defaultBranch)
	if err != nil {
		return nil, err
	}
	out := make([]repoCandidate, 0, len(prs))
	for _, pr := range prs {
		out = append(out, repoCandidate{
			Number:      pr.Number,
			Title:       pr.Title,
			BaseRefName: pr.BaseRefName,
			MergedAt:    pr.MergedAt,
		})
	}
	return out, nil
}

func repoPRFiles(ctx context.Context, repo string, pr int) ([]string, error) {
	output, err := runGhAPI(ctx, "--paginate", "--slurp", fmt.Sprintf("repos/%s/pulls/%d/files?per_page=100", repo, pr))
	if err != nil {
		return nil, err
	}
	var pages [][]struct {
		Filename string `json:"filename"`
	}
	if err := json.Unmarshal(output, &pages); err != nil {
		return nil, err
	}
	var files []string
	for _, page := range pages {
		for _, file := range page {
			if strings.TrimSpace(file.Filename) != "" {
				files = append(files, file.Filename)
			}
		}
	}
	return files, nil
}

func parsePRList(value string) ([]int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parts := strings.Split(value, ",")
	out := make([]int, 0, len(parts))
	seen := map[int]struct{}{}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("%s --pr contains an empty PR number", cliErrorPrefix())
		}
		n, err := strconv.Atoi(part)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("%s invalid --pr value %q", cliErrorPrefix(), part)
		}
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	return out, nil
}

func limitCandidates(candidates []repoCandidate, limit int) []repoCandidate {
	if limit <= 0 || len(candidates) <= limit {
		return candidates
	}
	return candidates[:limit]
}

func filesAreDocsOnly(files []string) bool {
	if len(files) == 0 {
		return false
	}
	for _, file := range files {
		if !isDocsPath(file) {
			return false
		}
	}
	return true
}

func isDocsPath(path string) bool {
	clean := filepath.ToSlash(strings.TrimSpace(path))
	lower := strings.ToLower(clean)
	switch {
	case lower == "readme.md", lower == "readme":
		return true
	case strings.HasPrefix(lower, "docs/"):
		return true
	case strings.HasPrefix(lower, "doc/"):
		return true
	case strings.HasPrefix(lower, "adr/"), strings.HasPrefix(lower, "adrs/"):
		return true
	case strings.HasPrefix(lower, "memo/"), strings.HasPrefix(lower, "memos/"):
		return true
	case strings.HasSuffix(lower, ".md"), strings.HasSuffix(lower, ".mdx"), strings.HasSuffix(lower, ".rst"), strings.HasSuffix(lower, ".txt"):
		return true
	default:
		return false
	}
}
