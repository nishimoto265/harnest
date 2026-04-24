package preflight

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/nishimoto265/auto-improve/internal/agents"
	"github.com/nishimoto265/auto-improve/internal/config"
)

type Failure struct {
	Name   string `json:"name"`
	Detail string `json:"detail"`
}

type PreflightResult struct {
	OK       bool      `json:"ok"`
	Failures []Failure `json:"failures,omitempty"`
}

type Dependencies struct {
	LookPath func(string) (string, error)
	Run      func(context.Context, string, ...string) ([]byte, error)
}

type Checker struct {
	deps Dependencies
}

type version struct {
	major int
	minor int
	patch int
}

var versionPattern = regexp.MustCompile(`(\d+)\.(\d+)(?:\.(\d+))?`)

func New() Checker {
	return NewWithDependencies(Dependencies{
		LookPath: exec.LookPath,
		Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			cmd := exec.CommandContext(ctx, name, args...)
			return cmd.CombinedOutput()
		},
	})
}

func NewWithDependencies(deps Dependencies) Checker {
	if deps.LookPath == nil {
		deps.LookPath = exec.LookPath
	}
	if deps.Run == nil {
		deps.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			cmd := exec.CommandContext(ctx, name, args...)
			return cmd.CombinedOutput()
		}
	}
	return Checker{deps: deps}
}

func (c Checker) Check(ctx context.Context, cfg config.Config) PreflightResult {
	failures := make([]Failure, 0, 8)

	runsBase, err := cfg.RunsBase()
	if err != nil {
		failures = append(failures, Failure{Name: "runs_base", Detail: err.Error()})
	}
	worktreeBase, err := cfg.WorktreeBase()
	if err != nil {
		failures = append(failures, Failure{Name: "worktree_base", Detail: err.Error()})
	}
	promotionLockPath, err := cfg.PromotionLockPath()
	if err != nil {
		failures = append(failures, Failure{Name: "promotion.lock", Detail: err.Error()})
	}

	failures = appendFailure(failures, c.checkVersion(ctx, "git", "git", version{major: 2, minor: 35}, "--version"))
	failures = appendFailure(failures, c.checkVersion(ctx, "gh", "gh", version{major: 2, minor: 40}, "--version"))
	failures = appendFailure(failures, c.checkBinary("curl", "curl"))
	failures = appendFailure(failures, c.checkVersion(ctx, "jq", "jq", version{major: 1, minor: 6}, "--version"))
	failures = appendFailure(failures, c.checkVersion(ctx, "yq", "yq", version{major: 4, minor: 0}, "--version"))
	failures = appendFailure(failures, c.checkBinary("lsof", "lsof"))
	failures = append(failures, c.checkAgentBinaries(cfg)...)
	failures = appendFailure(failures, c.checkGHAuth(ctx))

	if runsBase != "" {
		failures = appendFailure(failures, checkWritableDirectory("runs_base", runsBase))
	}
	if worktreeBase != "" {
		failures = appendFailure(failures, checkWritableDirectory("worktree_base", worktreeBase))
	}
	if promotionLockPath != "" {
		failures = appendFailure(failures, checkCreatableFile("promotion.lock", promotionLockPath))
	}
	if cfg.Repo.GitHub == "" {
		failures = append(failures, Failure{Name: "repo.github", Detail: "config: repo.github is required"})
	}
	if cfg.Repo.DefaultBranch == "" {
		failures = append(failures, Failure{Name: "repo.default_branch", Detail: "config: repo.default_branch is required"})
	}
	if cfg.Repo.BestBranch == "" {
		failures = append(failures, Failure{Name: "repo.best_branch", Detail: "config: repo.best_branch is required"})
	}
	if policyBranch, ok := cfg.PolicyBranch(); ok && policyBranch == cfg.Repo.BestBranch {
		failures = append(failures, Failure{Name: "repo.policy_branch", Detail: "config: repo.policy_branch must be distinct from repo.best_branch"})
	}
	repoRoot, err := cfg.RepoRoot()
	if err != nil {
		failures = append(failures, Failure{Name: "repo.root", Detail: err.Error()})
	} else if cfg.Repo.BestBranch != "" {
		failures = appendFailure(failures, c.checkRepoSlugMatches(ctx, repoRoot, cfg.Repo.GitHub))
		failures = appendFailure(failures, c.checkRemoteBranch(ctx, repoRoot, cfg.Repo.BestBranch))
		if policyBranch, ok := cfg.PolicyBranch(); ok {
			failures = appendFailure(failures, c.checkRemoteBranchNamed(ctx, repoRoot, policyBranch, "repo.policy_branch"))
		}
	}

	return PreflightResult{
		OK:       len(failures) == 0,
		Failures: failures,
	}
}

func (c Checker) checkVersion(ctx context.Context, failureName string, binary string, min version, args ...string) *Failure {
	resolved, err := c.deps.LookPath(binary)
	if err != nil {
		return &Failure{Name: failureName, Detail: err.Error()}
	}
	output, err := c.deps.Run(ctx, resolved, args...)
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = err.Error()
		}
		return &Failure{Name: failureName, Detail: detail}
	}
	parsed, err := parseVersion(string(output))
	if err != nil {
		return &Failure{Name: failureName, Detail: err.Error()}
	}
	if parsed.lessThan(min) {
		return &Failure{
			Name:   failureName,
			Detail: fmt.Sprintf("version %s is below required %s", parsed.String(), min.String()),
		}
	}
	return nil
}

func (c Checker) checkBinary(binary string, failureName string) *Failure {
	if _, err := c.deps.LookPath(binary); err != nil {
		return &Failure{Name: failureName, Detail: err.Error()}
	}
	return nil
}

func (c Checker) checkAgentBinaries(cfg config.Config) []Failure {
	failures := make([]Failure, 0, 4)
	seen := make(map[string]struct{})
	for _, role := range []agents.Role{
		agents.RoleImplementer,
		agents.RoleJudgePrimary,
		agents.RoleJudgeSecondary,
		agents.RoleJudgeArbiter,
	} {
		profile, err := cfg.AgentProfile(role)
		if err != nil {
			failures = append(failures, Failure{Name: string(role), Detail: err.Error()})
			continue
		}
		if profile.Provider == agents.ProviderStub || profile.Provider == agents.ProviderStubViolation || profile.Provider == agents.ProviderStubAdopt || profile.Binary == "" {
			continue
		}
		key := string(profile.Provider) + ":" + profile.Binary
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if failure := c.checkBinary(profile.Binary, profile.Binary); failure != nil {
			failures = append(failures, *failure)
		}
	}
	return failures
}

func (c Checker) checkGHAuth(ctx context.Context) *Failure {
	resolved, err := c.deps.LookPath("gh")
	if err != nil {
		return &Failure{Name: "gh-auth", Detail: err.Error()}
	}
	output, err := c.deps.Run(ctx, resolved, "auth", "status")
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = err.Error()
		}
		return &Failure{Name: "gh-auth", Detail: detail}
	}
	return nil
}

func (c Checker) checkRemoteBranch(ctx context.Context, repoRoot, branch string) *Failure {
	return c.checkRemoteBranchNamed(ctx, repoRoot, branch, "repo.best_branch")
}

func (c Checker) checkRemoteBranchNamed(ctx context.Context, repoRoot, branch, failureName string) *Failure {
	resolved, err := c.deps.LookPath("git")
	if err != nil {
		return &Failure{Name: failureName, Detail: err.Error()}
	}
	output, err := c.deps.Run(ctx, resolved, "-C", repoRoot, "ls-remote", "--heads", "origin", branch)
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = err.Error()
		}
		return &Failure{Name: failureName, Detail: detail}
	}
	if strings.TrimSpace(string(output)) == "" {
		return &Failure{Name: failureName, Detail: fmt.Sprintf("config: %s %q was not found on origin", failureName, branch)}
	}
	return nil
}

func (c Checker) checkRepoSlugMatches(ctx context.Context, repoRoot, configured string) *Failure {
	if strings.TrimSpace(configured) == "" {
		return nil
	}
	resolved, err := c.deps.LookPath("git")
	if err != nil {
		return &Failure{Name: "repo.github", Detail: err.Error()}
	}
	output, err := c.deps.Run(ctx, resolved, "-C", repoRoot, "remote", "get-url", "origin")
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = err.Error()
		}
		return &Failure{Name: "repo.github", Detail: detail}
	}
	slug, err := repoSlugFromRemoteURL(strings.TrimSpace(string(output)))
	if err != nil {
		return &Failure{Name: "repo.github", Detail: err.Error()}
	}
	if !strings.EqualFold(slug, configured) {
		return &Failure{Name: "repo.github", Detail: fmt.Sprintf("config: repo.github=%q does not match origin=%q", configured, slug)}
	}
	return nil
}

func repoSlugFromRemoteURL(remoteURL string) (string, error) {
	if remoteURL == "" {
		return "", fmt.Errorf("origin remote url is empty")
	}
	if strings.HasPrefix(remoteURL, "git@") {
		parts := strings.SplitN(remoteURL, ":", 2)
		if len(parts) != 2 || parts[1] == "" {
			return "", fmt.Errorf("could not parse git remote url: %q", remoteURL)
		}
		return strings.TrimSuffix(strings.Trim(parts[1], "/"), ".git"), nil
	}
	parsed, err := url.Parse(remoteURL)
	if err != nil {
		return "", fmt.Errorf("could not parse git remote url %q: %w", remoteURL, err)
	}
	path := strings.Trim(parsed.Path, "/")
	path = strings.TrimSuffix(path, ".git")
	if path == "" {
		return "", fmt.Errorf("could not parse git remote url: %q", remoteURL)
	}
	return path, nil
}

func checkWritableDirectory(name string, path string) *Failure {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return &Failure{Name: name, Detail: err.Error()}
	}
	file, err := os.CreateTemp(path, ".preflight-*")
	if err != nil {
		return &Failure{Name: name, Detail: err.Error()}
	}
	tempPath := file.Name()
	if closeErr := file.Close(); closeErr != nil {
		_ = os.Remove(tempPath)
		return &Failure{Name: name, Detail: closeErr.Error()}
	}
	if err := os.Remove(tempPath); err != nil && !os.IsNotExist(err) {
		return &Failure{Name: name, Detail: err.Error()}
	}
	return nil
}

func checkCreatableFile(name string, path string) *Failure {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return &Failure{Name: name, Detail: err.Error()}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return &Failure{Name: name, Detail: err.Error()}
	}
	if err := file.Close(); err != nil {
		return &Failure{Name: name, Detail: err.Error()}
	}
	return nil
}

func parseVersion(output string) (version, error) {
	matches := versionPattern.FindStringSubmatch(output)
	if len(matches) == 0 {
		return version{}, fmt.Errorf("preflight: could not parse version from %q", strings.TrimSpace(output))
	}
	v := version{}
	if _, err := fmt.Sscanf(matches[0], "%d.%d.%d", &v.major, &v.minor, &v.patch); err == nil {
		return v, nil
	}
	var major, minor int
	if _, err := fmt.Sscanf(matches[0], "%d.%d", &major, &minor); err != nil {
		return version{}, err
	}
	return version{major: major, minor: minor}, nil
}

func (v version) lessThan(other version) bool {
	if v.major != other.major {
		return v.major < other.major
	}
	if v.minor != other.minor {
		return v.minor < other.minor
	}
	return v.patch < other.patch
}

func (v version) String() string {
	return fmt.Sprintf("%d.%d.%d", v.major, v.minor, v.patch)
}

func appendFailure(failures []Failure, failure *Failure) []Failure {
	if failure == nil {
		return failures
	}
	return append(failures, *failure)
}
