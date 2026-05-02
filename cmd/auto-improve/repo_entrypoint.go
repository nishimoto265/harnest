package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/nishimoto265/auto-improve/internal/detect"
	"github.com/nishimoto265/auto-improve/internal/gitremote"
	"github.com/nishimoto265/auto-improve/internal/orchestrator"
	"github.com/nishimoto265/auto-improve/internal/processenv"
	"github.com/nishimoto265/auto-improve/internal/state"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

const (
	defaultRepoEntrypointInterval = time.Hour
	defaultBestBranch             = "auto-improve/best"
	defaultPolicyBranch           = "auto-improve/policy"
)

type repoEntrypointOptions struct {
	Limit  int
	PRList string
	DryRun bool
}

type repoEntrypointRuntime struct {
	Config        config.Config `json:"-"`
	RepoURL       string        `json:"repo_url"`
	Repo          string        `json:"repo"`
	DefaultBranch string        `json:"default_branch"`
	RepoRoot      string        `json:"repo_root"`
	RunsBase      string        `json:"runs_base"`
	WorktreeBase  string        `json:"worktree_base"`
	Home          string        `json:"-"`
}

type repoCandidate struct {
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	BaseRefName string    `json:"base_ref_name"`
	MergedAt    time.Time `json:"merged_at"`
	Files       []string  `json:"files,omitempty"`
}

type repoSkippedPR struct {
	Number int    `json:"number"`
	Title  string `json:"title,omitempty"`
	Reason string `json:"reason"`
}

type repoEntrypointPlan struct {
	Event        string          `json:"event"`
	Mode         string          `json:"mode"`
	Repo         string          `json:"repo"`
	RepoURL      string          `json:"repo_url"`
	RepoRoot     string          `json:"repo_root"`
	RunsBase     string          `json:"runs_base"`
	WorktreeBase string          `json:"worktree_base"`
	Candidates   []repoCandidate `json:"candidates,omitempty"`
	Skipped      []repoSkippedPR `json:"skipped,omitempty"`
	Selected     []repoCandidate `json:"selected,omitempty"`
	PRs          []int           `json:"prs,omitempty"`
	DryRun       bool            `json:"dry_run"`
}

type repoCandidateSelector func(context.Context, config.Config, string) ([]repoCandidate, []repoSkippedPR, error)

var repoEntrypointBootstrap = bootstrapRepoEntrypoint
var repoEntrypointEnsureClone = ensureRepoEntrypointClone
var repoEntrypointCandidates repoCandidateSelector = selectRepoEntrypointCandidates
var repoEntrypointMergedPRs = listMergedPRsForRepoEntrypoint
var repoEntrypointPRFiles = repoPRFiles
var repoEntrypointSleep = func(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func runRepoEntrypoint(cmd *cobra.Command, repoURL string, opts repoEntrypointOptions) error {
	if opts.Limit < 0 {
		return commandExitError{code: 2, msg: "auto-improve: --limit must be >= 0"}
	}
	prs, err := parsePRList(opts.PRList)
	if err != nil {
		return commandExitError{code: 2, msg: err.Error()}
	}
	if len(prs) > 0 && opts.Limit > 0 {
		return commandExitError{code: 2, msg: "auto-improve: --pr and --limit are mutually exclusive"}
	}

	ctx, stopSignals := signalAwareContext(cmd.Context())
	defer stopSignals()

	runtime, err := repoEntrypointBootstrap(ctx, repoURL)
	if err != nil {
		return err
	}

	switch {
	case opts.DryRun:
		return outputRepoEntrypointDryRun(ctx, cmd, runtime, opts, prs)
	case len(prs) > 0:
		return runRepoEntrypointPRs(ctx, runtime, prs)
	case opts.Limit > 0:
		return runRepoEntrypointBatch(ctx, runtime, opts.Limit)
	default:
		return runRepoEntrypointWatch(ctx, runtime)
	}
}

func outputRepoEntrypointDryRun(ctx context.Context, cmd *cobra.Command, runtime repoEntrypointRuntime, opts repoEntrypointOptions, prs []int) error {
	plan := repoEntrypointPlan{
		Event:        "repo_entrypoint_dry_run",
		Repo:         runtime.Repo,
		RepoURL:      runtime.RepoURL,
		RepoRoot:     runtime.RepoRoot,
		RunsBase:     runtime.RunsBase,
		WorktreeBase: runtime.WorktreeBase,
		PRs:          prs,
		DryRun:       true,
	}
	switch {
	case len(prs) > 0:
		plan.Mode = "pr"
		selected, skipped, err := resolveExplicitRepoEntrypointPRs(ctx, runtime.Config, prs)
		if err != nil {
			return err
		}
		plan.Selected = selected
		plan.Skipped = skipped
	case opts.Limit > 0:
		plan.Mode = "limit"
		candidates, skipped, err := repoEntrypointCandidates(ctx, runtime.Config, runtime.processedPath())
		if err != nil {
			return err
		}
		plan.Candidates = candidates
		plan.Skipped = skipped
		plan.Selected = limitCandidates(candidates, opts.Limit)
	default:
		plan.Mode = "watch"
		candidates, skipped, err := repoEntrypointCandidates(ctx, runtime.Config, runtime.processedPath())
		if err != nil {
			return err
		}
		plan.Candidates = candidates
		plan.Skipped = skipped
		plan.Selected = candidates
	}
	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetIndent("", "  ")
	return encoder.Encode(plan)
}

func runRepoEntrypointPRs(ctx context.Context, runtime repoEntrypointRuntime, prs []int) error {
	if err := registerRepoEntrypoint(runtime); err != nil {
		return err
	}
	if err := repoEntrypointEnsureClone(ctx, runtime); err != nil {
		return err
	}
	if err := checkCLIRecoveryGate(runtime.Config); err != nil {
		return err
	}
	selected, _, err := resolveExplicitRepoEntrypointPRs(ctx, runtime.Config, prs)
	if err != nil {
		return err
	}
	runner, err := newPipelineRunner(&runtime.Config)
	if err != nil {
		return err
	}
	for _, pr := range selected {
		if err := runner.Run(ctx, pr.Number, orchestrator.RunOptions{}); err != nil {
			if commandErr := recoveryGateExitError(err); commandErr != nil {
				return commandErr
			}
			return err
		}
	}
	return nil
}

func runRepoEntrypointBatch(ctx context.Context, runtime repoEntrypointRuntime, limit int) error {
	if err := registerRepoEntrypoint(runtime); err != nil {
		return err
	}
	if err := repoEntrypointEnsureClone(ctx, runtime); err != nil {
		return err
	}
	if err := checkDetectLoopRecoveryGate(ctx, runtime.Config); err != nil {
		return err
	}
	runner, err := newPipelineRunner(&runtime.Config)
	if err != nil {
		return err
	}
	return runRepoEntrypointTick(ctx, runtime, runner, limit, false)
}

func runRepoEntrypointWatch(ctx context.Context, runtime repoEntrypointRuntime) error {
	if err := registerRepoEntrypoint(runtime); err != nil {
		return err
	}
	if err := repoEntrypointEnsureClone(ctx, runtime); err != nil {
		return err
	}
	if err := checkDetectLoopRecoveryGate(ctx, runtime.Config); err != nil {
		return err
	}
	runner, err := newPipelineRunner(&runtime.Config)
	if err != nil {
		return err
	}
	for {
		if err := runRepoEntrypointTick(ctx, runtime, runner, 0, true); err != nil {
			return err
		}
		if err := repoEntrypointSleep(ctx, defaultRepoEntrypointInterval); err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
	}
}

func runRepoEntrypointTick(ctx context.Context, runtime repoEntrypointRuntime, runner pipelineRunner, limit int, drainResume bool) error {
	if err := checkDetectLoopRecoveryGateForRunsBase(ctx, runtime.RunsBase); err != nil {
		return err
	}
	if drainResume {
		paused, err := drainRepoEntrypointResumeQueue(ctx, runtime, runner)
		if err != nil {
			return err
		}
		if paused {
			return nil
		}
	}
	candidates, _, err := repoEntrypointCandidates(ctx, runtime.Config, runtime.processedPath())
	if err != nil {
		return err
	}
	for _, pr := range limitCandidates(candidates, limit) {
		if err := runner.Run(ctx, pr.Number, orchestrator.RunOptions{}); err != nil {
			if commandErr := recoveryGateExitError(err); commandErr != nil {
				return commandErr
			}
			return err
		}
		if err := checkDetectLoopRecoveryGateForRunsBase(ctx, runtime.RunsBase); err != nil {
			return err
		}
	}
	return nil
}

func drainRepoEntrypointResumeQueue(ctx context.Context, runtime repoEntrypointRuntime, runner pipelineRunner) (bool, error) {
	for {
		resumeTargets, err := stateResumeTargets(runtime.processedPath())
		if err != nil {
			return false, err
		}
		if len(resumeTargets) == 0 {
			return false, nil
		}
		item := resumeTargets[0]
		if err := runner.Run(ctx, item.PR, orchestrator.RunOptions{RunID: item.RunID}); err != nil {
			if commandErr := recoveryGateExitError(err); commandErr != nil {
				return false, commandErr
			}
			return false, err
		}
		if err := checkDetectLoopRecoveryGateForRunsBase(ctx, runtime.RunsBase); err != nil {
			return false, err
		}
		remaining, err := stateResumeTargets(runtime.processedPath())
		if err != nil {
			return false, err
		}
		if resumeTargetStillPending(remaining, item) {
			return true, nil
		}
	}
}

func (r repoEntrypointRuntime) processedPath() string {
	path, err := r.Config.ProcessedPath()
	if err != nil {
		return filepath.Join(r.RunsBase, "processed.jsonl")
	}
	return path
}

func bootstrapRepoEntrypoint(ctx context.Context, repoURL string) (repoEntrypointRuntime, error) {
	info, err := gitremote.ParseGitHubRemote(repoURL, gitremote.AllowedGitHubHostsFromEnv(processenv.SanitizeForNetworkExec()))
	if err != nil {
		return repoEntrypointRuntime{}, commandExitError{code: 2, msg: err.Error()}
	}
	home, err := autoImproveHome()
	if err != nil {
		return repoEntrypointRuntime{}, err
	}
	namespace := repoNamespace(info.Slug)
	repoRoot := filepath.Join(home, "repos", filepath.FromSlash(info.Slug))
	runsPath := filepath.Join(home, "runs", namespace, "runs")
	worktreePath := filepath.Join(home, "worktrees", namespace, "worktrees")
	defaultBranch, err := repoDefaultBranch(ctx, info.Slug)
	if err != nil {
		return repoEntrypointRuntime{}, err
	}
	baseCfg, err := loadRepoEntrypointBaseConfig()
	if err != nil {
		return repoEntrypointRuntime{}, err
	}
	bestBranch := strings.TrimSpace(baseCfg.Repo.BestBranch)
	if bestBranch == "" {
		bestBranch = defaultBestBranch
	}
	policyBranch := strings.TrimSpace(baseCfg.Repo.PolicyBranch)
	if policyBranch == "" {
		policyBranch = defaultPolicyBranch
	}
	cfg := baseCfg.ForRepository(config.RepoConfig{
		GitHub:        info.Slug,
		Root:          repoRoot,
		DefaultBranch: defaultBranch,
		BestBranch:    bestBranch,
		PolicyBranch:  policyBranch,
	}, runsPath, worktreePath)
	if err := cfg.Validate(); err != nil {
		return repoEntrypointRuntime{}, commandExitError{code: 2, msg: err.Error()}
	}
	runsBase, err := cfg.RunsBase()
	if err != nil {
		return repoEntrypointRuntime{}, err
	}
	worktreeBase, err := cfg.WorktreeBase()
	if err != nil {
		return repoEntrypointRuntime{}, err
	}
	return repoEntrypointRuntime{
		Config:        cfg,
		RepoURL:       repoURL,
		Repo:          info.Slug,
		DefaultBranch: defaultBranch,
		RepoRoot:      repoRoot,
		RunsBase:      runsBase,
		WorktreeBase:  worktreeBase,
		Home:          home,
	}, nil
}

func registerRepoEntrypoint(runtime repoEntrypointRuntime) error {
	return writeRepositoryRegistration(runtime.Home, repoRegistration{
		Slug:          runtime.Repo,
		URL:           runtime.RepoURL,
		Root:          runtime.RepoRoot,
		DefaultBranch: runtime.DefaultBranch,
		RunsBase:      runtime.RunsBase,
		WorktreeBase:  runtime.WorktreeBase,
		UpdatedAt:     time.Now().UTC(),
	})
}

func loadRepoEntrypointBaseConfig() (config.Config, error) {
	if _, err := os.Stat("config.yaml"); err == nil {
		return config.LoadDefault()
	} else if err != nil && !os.IsNotExist(err) {
		return config.Config{}, err
	}
	return config.Default(), nil
}

func ensureRepoEntrypointClone(ctx context.Context, runtime repoEntrypointRuntime) error {
	if _, err := os.Stat(filepath.Join(runtime.RepoRoot, ".git")); err == nil {
		return nil
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(runtime.RepoRoot), 0o755); err != nil {
		return err
	}
	cmd, err := processenv.TrustedCommandContext(ctx, "git", "clone", runtime.RepoURL, runtime.RepoRoot)
	if err != nil {
		return err
	}
	cmd.Env = processenv.GitNetworkEnvForRemoteURL(runtime.RepoURL)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("auto-improve: git clone failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

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

func repoDefaultBranch(ctx context.Context, repo string) (string, error) {
	output, err := runGhAPI(ctx, fmt.Sprintf("repos/%s", repo))
	if err != nil {
		return "", err
	}
	var payload struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := json.Unmarshal(output, &payload); err != nil {
		return "", err
	}
	if strings.TrimSpace(payload.DefaultBranch) == "" {
		return "", fmt.Errorf("auto-improve: could not resolve default branch for %s", repo)
	}
	return payload.DefaultBranch, nil
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

func runGhAPI(ctx context.Context, args ...string) ([]byte, error) {
	cmd, err := processenv.TrustedCommandContext(ctx, "gh", append([]string{"api"}, args...)...)
	if err != nil {
		return nil, err
	}
	cmd.Env = processenv.SanitizeForNetworkExec()
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("auto-improve: gh api failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return output, nil
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
			return nil, fmt.Errorf("auto-improve: --pr contains an empty PR number")
		}
		n, err := strconv.Atoi(part)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("auto-improve: invalid --pr value %q", part)
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

func repoNamespace(slug string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(slug)), "/", "__")
}

func autoImproveHome() (string, error) {
	if value := strings.TrimSpace(os.Getenv("AUTO_IMPROVE_HOME")); value != "" {
		return filepath.Abs(value)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".auto-improve"), nil
}

type repoRegistration struct {
	Slug          string    `json:"slug" yaml:"slug"`
	URL           string    `json:"url" yaml:"url"`
	Root          string    `json:"root" yaml:"root"`
	DefaultBranch string    `json:"default_branch" yaml:"default_branch"`
	RunsBase      string    `json:"runs_base" yaml:"runs_base"`
	WorktreeBase  string    `json:"worktree_base" yaml:"worktree_base"`
	UpdatedAt     time.Time `json:"updated_at" yaml:"updated_at"`
}

func writeRepositoryRegistration(home string, registration repoRegistration) error {
	if err := os.MkdirAll(home, 0o755); err != nil {
		return err
	}
	path := filepath.Join(home, "repositories.yaml")
	var registrations []repoRegistration
	if data, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(data))) > 0 {
		if err := yaml.Unmarshal(data, &registrations); err != nil {
			return err
		}
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	replaced := false
	for i := range registrations {
		if registrations[i].Slug == registration.Slug {
			registrations[i] = registration
			replaced = true
			break
		}
	}
	if !replaced {
		registrations = append(registrations, registration)
	}
	data, err := yaml.Marshal(registrations)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

var stateResumeTargets = func(processedPath string) ([]state.ResumeRequest, error) {
	return state.ResumeTargetPath(processedPath)
}
