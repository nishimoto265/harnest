package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/nishimoto265/auto-improve/internal/gitremote"
	"github.com/nishimoto265/auto-improve/internal/processenv"
	"gopkg.in/yaml.v3"
)

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
