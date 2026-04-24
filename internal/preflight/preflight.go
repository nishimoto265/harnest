package preflight

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/nishimoto265/auto-improve/internal/agents"
	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/nishimoto265/auto-improve/internal/gitremote"
	"github.com/nishimoto265/auto-improve/internal/processenv"
	"github.com/nishimoto265/auto-improve/internal/steps/agentrunner"
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
	LookPath              func(string) (string, error)
	Run                   func(context.Context, string, ...string) ([]byte, error)
	RunGitLocal           func(context.Context, string, ...string) ([]byte, error)
	RunGitNetwork         func(context.Context, string, string, ...string) ([]byte, error)
	RunProviderSmoke      func(context.Context, string, ...string) ([]byte, error)
	PrepareProviderBinary func(agents.Provider, string) (string, []string, error)
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

const providerSmokeTimeout = 5 * time.Second

func New() Checker {
	return NewWithDependencies(Dependencies{
		LookPath:      processenv.TrustedLookPath,
		Run:           runSanitizedNetworkCommand,
		RunGitLocal:   runSanitizedGitLocalCommand,
		RunGitNetwork: runSanitizedGitNetworkCommand,
	})
}

func NewWithDependencies(deps Dependencies) Checker {
	customRun := deps.Run != nil
	if deps.LookPath == nil {
		deps.LookPath = processenv.TrustedLookPath
	}
	if deps.Run == nil {
		deps.Run = runSanitizedNetworkCommand
	}
	if deps.RunGitLocal == nil {
		if customRun {
			deps.RunGitLocal = deps.Run
		} else {
			deps.RunGitLocal = runSanitizedGitLocalCommand
		}
	}
	if deps.RunGitNetwork == nil {
		if customRun {
			deps.RunGitNetwork = func(ctx context.Context, remoteURL, name string, args ...string) ([]byte, error) {
				return deps.Run(ctx, name, args...)
			}
		} else {
			deps.RunGitNetwork = runSanitizedGitNetworkCommand
		}
	}
	if deps.RunProviderSmoke == nil {
		if customRun {
			deps.RunProviderSmoke = deps.Run
		} else {
			deps.RunProviderSmoke = runSanitizedLocalCommand
		}
	}
	if deps.PrepareProviderBinary == nil {
		deps.PrepareProviderBinary = agentrunner.PrepareProviderBinary
	}
	return Checker{deps: deps}
}

func runSanitizedNetworkCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd, err := processenv.TrustedCommandContext(ctx, name, args...)
	if err != nil {
		return nil, err
	}
	cmd.Env = processenv.SanitizeForNetworkExec()
	return cmd.CombinedOutput()
}

func runSanitizedGitLocalCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd, err := processenv.TrustedCommandContext(ctx, name, args...)
	if err != nil {
		return nil, err
	}
	cmd.Env = processenv.GitLocalEnv()
	return cmd.CombinedOutput()
}

func runSanitizedLocalCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd, err := processenv.TrustedCommandContext(ctx, name, args...)
	if err != nil {
		return nil, err
	}
	cmd.Env = processenv.SanitizeForLocalExec()
	return cmd.CombinedOutput()
}

func runSanitizedGitNetworkCommand(ctx context.Context, remoteURL, name string, args ...string) ([]byte, error) {
	cmd, err := processenv.TrustedCommandContext(ctx, name, args...)
	if err != nil {
		return nil, err
	}
	cmd.Env = processenv.GitNetworkEnvForRemoteURL(remoteURL)
	return cmd.CombinedOutput()
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
	failures = append(failures, c.checkAgentBinaries(ctx, cfg)...)
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
	if policyBranch, ok := cfg.PolicyBranch(); ok {
		if policyBranch == cfg.Repo.BestBranch {
			failures = append(failures, Failure{Name: "repo.policy_branch", Detail: "config: repo.policy_branch must be distinct from repo.best_branch"})
		}
		if policyBranch == cfg.Repo.DefaultBranch {
			failures = append(failures, Failure{Name: "repo.policy_branch", Detail: "config: repo.policy_branch must be distinct from repo.default_branch"})
		}
	}
	repoRoot, err := cfg.RepoRoot()
	if err != nil {
		failures = append(failures, Failure{Name: "repo.root", Detail: err.Error()})
	} else if cfg.Repo.BestBranch != "" {
		remoteURL, failure := c.originConfiguredURL(ctx, repoRoot)
		failures = appendFailure(failures, failure)
		if failure == nil {
			failures = appendFailure(failures, c.checkRepoSlugMatches(remoteURL, cfg.Repo.GitHub))
			pushURLs, pushFailure := c.originConfiguredPushURLs(ctx, repoRoot, remoteURL)
			failures = appendFailure(failures, pushFailure)
			if pushFailure == nil {
				failures = append(failures, c.checkOriginPushURLs(remoteURL, pushURLs, cfg.Repo.GitHub)...)
			}
			failures = appendFailure(failures, c.checkRemoteBranch(ctx, repoRoot, remoteURL, cfg.Repo.BestBranch))
			if policyBranch, ok := cfg.PolicyBranch(); ok {
				failures = appendFailure(failures, c.checkRemoteBranchNamed(ctx, repoRoot, remoteURL, policyBranch, "repo.policy_branch"))
			}
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

func (c Checker) checkAgentBinaries(ctx context.Context, cfg config.Config) []Failure {
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
		if agents.IsGatedTestStubProvider(profile.Provider) {
			if !agents.AllowTestStubProviders() {
				failures = append(failures, Failure{
					Name:   string(role),
					Detail: fmt.Sprintf("agents: provider %q requires %s=1", profile.Provider, agents.AllowTestStubProvidersEnv),
				})
			}
			continue
		}
		if profile.Provider == agents.ProviderStub || profile.Binary == "" {
			continue
		}
		key := string(profile.Provider) + ":" + profile.Binary
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		binary, prefixArgs, err := c.deps.PrepareProviderBinary(profile.Provider, profile.Binary)
		if err != nil {
			failures = append(failures, Failure{Name: profile.Binary, Detail: err.Error()})
			continue
		}
		if failure := c.smokeProviderVersion(ctx, profile.Binary, binary, prefixArgs); failure != nil {
			failures = append(failures, *failure)
		}
	}
	return failures
}

func (c Checker) smokeProviderVersion(ctx context.Context, failureName, binary string, prefixArgs []string) *Failure {
	smokeCtx, cancel := context.WithTimeout(ctx, providerSmokeTimeout)
	defer cancel()
	args := append([]string{}, prefixArgs...)
	args = append(args, "--version")
	output, err := c.deps.RunProviderSmoke(smokeCtx, binary, args...)
	if err == nil {
		return nil
	}
	detail := strings.TrimSpace(string(output))
	if detail == "" {
		detail = err.Error()
	}
	if smokeCtx.Err() == context.DeadlineExceeded {
		detail = fmt.Sprintf("provider smoke timed out after %s", providerSmokeTimeout)
	}
	return &Failure{Name: failureName, Detail: detail}
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

func (c Checker) originConfiguredURL(ctx context.Context, repoRoot string) (string, *Failure) {
	resolved, err := c.deps.LookPath("git")
	if err != nil {
		return "", &Failure{Name: "repo.github", Detail: err.Error()}
	}
	output, err := c.deps.RunGitLocal(ctx, resolved, "-C", repoRoot, "config", "--get", "remote.origin.url")
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = err.Error()
		}
		return "", &Failure{Name: "repo.github", Detail: detail}
	}
	return strings.TrimSpace(string(output)), nil
}

func (c Checker) originConfiguredPushURLs(ctx context.Context, repoRoot, originURL string) ([]string, *Failure) {
	resolved, err := c.deps.LookPath("git")
	if err != nil {
		return nil, &Failure{Name: "repo.github", Detail: err.Error()}
	}
	output, err := c.deps.RunGitLocal(ctx, resolved, "-C", repoRoot, "config", "--get-all", "remote.origin.pushurl")
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			return []string{originURL}, nil
		}
		return nil, &Failure{Name: "repo.github", Detail: detail}
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	pushURLs := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pushURLs = append(pushURLs, line)
	}
	if len(pushURLs) == 0 {
		return []string{originURL}, nil
	}
	return pushURLs, nil
}

func (c Checker) checkRemoteBranch(ctx context.Context, repoRoot, remoteURL, branch string) *Failure {
	return c.checkRemoteBranchNamed(ctx, repoRoot, remoteURL, branch, "repo.best_branch")
}

func (c Checker) checkRemoteBranchNamed(ctx context.Context, repoRoot, remoteURL, branch, failureName string) *Failure {
	resolved, err := c.deps.LookPath("git")
	if err != nil {
		return &Failure{Name: failureName, Detail: err.Error()}
	}
	output, err := c.deps.RunGitNetwork(ctx, remoteURL, resolved, "-C", repoRoot, "ls-remote", "--heads", "origin", branch)
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

func (c Checker) checkRepoSlugMatches(remoteURL, configured string) *Failure {
	if strings.TrimSpace(configured) == "" {
		return nil
	}
	info, err := gitremote.ParseGitHubRemote(remoteURL, gitremote.AllowedGitHubHostsFromEnv(processenv.SanitizeForNetworkExec()))
	if err != nil {
		return &Failure{Name: "repo.github", Detail: err.Error()}
	}
	if !strings.EqualFold(info.Slug, configured) {
		return &Failure{Name: "repo.github", Detail: fmt.Sprintf("config: repo.github=%q does not match origin=%q", configured, info.Slug)}
	}
	return nil
}

func (c Checker) checkOriginPushURLs(fetchURL string, pushURLs []string, configured string) []Failure {
	allowedHosts := gitremote.AllowedGitHubHostsFromEnv(processenv.SanitizeForNetworkExec())
	fetchInfo, err := gitremote.ParseGitHubRemote(fetchURL, allowedHosts)
	if err != nil {
		return []Failure{{Name: "repo.github", Detail: err.Error()}}
	}

	failures := make([]Failure, 0)
	for _, pushURL := range pushURLs {
		pushInfo, err := gitremote.ParseGitHubRemote(pushURL, allowedHosts)
		if err != nil {
			failures = append(failures, Failure{Name: "repo.github", Detail: fmt.Sprintf("origin push url %q: %s", pushURL, err.Error())})
			continue
		}
		if !strings.EqualFold(pushInfo.Host, fetchInfo.Host) {
			failures = append(failures, Failure{Name: "repo.github", Detail: fmt.Sprintf("origin push url %q host=%q does not match origin fetch host=%q", pushURL, pushInfo.Host, fetchInfo.Host)})
			continue
		}
		if !strings.EqualFold(pushInfo.Slug, fetchInfo.Slug) {
			failures = append(failures, Failure{Name: "repo.github", Detail: fmt.Sprintf("origin push url %q repo=%q does not match origin fetch repo=%q", pushURL, pushInfo.Slug, fetchInfo.Slug)})
			continue
		}
		if strings.TrimSpace(configured) != "" && !strings.EqualFold(pushInfo.Slug, configured) {
			failures = append(failures, Failure{Name: "repo.github", Detail: fmt.Sprintf("origin push url %q repo=%q does not match config repo.github=%q", pushURL, pushInfo.Slug, configured)})
		}
	}
	return failures
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
