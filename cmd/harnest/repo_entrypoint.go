package main

import (
	"context"
	"time"

	"github.com/nishimoto265/harnest/internal/config"
	"github.com/spf13/cobra"
)

const (
	defaultRepoEntrypointInterval = time.Hour
	defaultBestBranch             = "harnest/best"
	defaultPolicyBranch           = "harnest/policy"
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

func runRepoEntrypoint(cmd *cobra.Command, repoURL string, opts repoEntrypointOptions, outputOpts cliOutputOptions) error {
	if opts.Limit < 0 {
		return commandExitError{code: 2, msg: cliErrorPrefix() + " --limit must be >= 0"}
	}
	if err := validateOutputOptions(outputOpts); err != nil {
		return err
	}
	prs, err := parsePRList(opts.PRList)
	if err != nil {
		return commandExitError{code: 2, msg: err.Error()}
	}
	if len(prs) > 0 && opts.Limit > 0 {
		return commandExitError{code: 2, msg: cliErrorPrefix() + " --pr and --limit are mutually exclusive"}
	}

	ctx, stopSignals := signalAwareContext(cmd.Context())
	defer stopSignals()

	runtime, err := repoEntrypointBootstrap(ctx, repoURL)
	if err != nil {
		return err
	}
	reporter := newCLIProgressReporter(cmd, outputOpts)
	defer reporter.Close()

	switch {
	case opts.DryRun:
		return outputRepoEntrypointDryRun(ctx, cmd, runtime, opts, prs)
	case len(prs) > 0:
		return runRepoEntrypointPRs(ctx, runtime, prs, reporter)
	case opts.Limit > 0:
		return runRepoEntrypointBatch(ctx, runtime, opts.Limit, reporter)
	default:
		return runRepoEntrypointWatch(ctx, runtime, reporter)
	}
}
