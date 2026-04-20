package preflight

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

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
	failures = appendFailure(failures, c.checkVersion(ctx, "jq", "jq", version{major: 1, minor: 6}, "--version"))
	failures = appendFailure(failures, c.checkVersion(ctx, "yq", "yq", version{major: 4, minor: 0}, "--version"))
	failures = appendFailure(failures, c.checkBinary(cfg.ClaudeBinary(), "claude"))
	failures = appendFailure(failures, c.checkBinary(cfg.CodexBinary(), "codex"))
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
