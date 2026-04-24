package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/nishimoto265/auto-improve/internal/agents"
	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/validation"
	"gopkg.in/yaml.v3"
)

const defaultConfigFile = "config.yaml"

const (
	DefaultRegistryHighThreshold     = 1501
	DefaultRegistryCriticalThreshold = 2001
	defaultRunsDirName               = "runs"
	defaultWorktreesDirName          = "worktrees"
)

var requiredStepTimeoutKeys = []string{
	"step10",
	"step20",
	"step30",
	"step40",
	"step50",
	"step60",
	"step70",
}

var defaultStepTimeouts = map[string]int{
	"step10": 300,
	"step20": 1800,
	"step30": 1800,
	"step40": 900,
	"step50": 1800,
	"step60": 1800,
	"step70": 300,
}

type Config struct {
	Repo       RepoConfig       `yaml:"repo"`
	Worktree   WorktreeConfig   `yaml:"worktree"`
	Agents     AgentsConfig     `yaml:"agents"`
	Paths      PathsConfig      `yaml:"paths"`
	TaskPrompt TaskPromptConfig `yaml:"task_prompt"`

	RunsBasePath              string         `yaml:"runs_base"`
	WorktreeBasePath          string         `yaml:"worktree_base"`
	AgentConfigPath           string         `yaml:"agent_config_path"`
	ClaudeCLIPath             string         `yaml:"claude_cli_path"`
	CodexCLIPath              string         `yaml:"codex_cli_path"`
	PreflightTimeoutSec       int            `yaml:"preflight_timeout_sec"`
	RescueMaxRetries          int            `yaml:"rescue_max_retries"`
	RegistryHighThreshold     int            `yaml:"registry_high_threshold"`
	RegistryCriticalThreshold int            `yaml:"registry_critical_threshold"`
	StepTimeouts              map[string]int `yaml:"step_timeouts"`

	configPath string
	repoRoot   string
	agentFile  agents.File
}

type RepoConfig struct {
	GitHub        string `yaml:"github"`
	Root          string `yaml:"root"`
	DefaultBranch string `yaml:"default_branch"`
	BestBranch    string `yaml:"best_branch"`
	PolicyBranch  string `yaml:"policy_branch"`
}

type WorktreeConfig struct {
	Base string `yaml:"base"`
}

type AgentsConfig struct {
	Implementer    string `yaml:"implementer"`
	JudgePrimary   string `yaml:"judge_primary"`
	JudgeSecondary string `yaml:"judge_secondary"`
}

type PathsConfig struct {
	Runs          string `yaml:"runs"`
	StateFile     string `yaml:"state_file"`
	RulesRegistry string `yaml:"rules_registry"`
}

type TaskPromptConfig struct {
	Source string `yaml:"source"`
}

func LoadDefault() (Config, error) {
	return Load(defaultConfigFile)
}

func LoadConfig(path string) (*Config, error) {
	cfg, err := Load(path)
	if err != nil {
		return nil, err
	}
	return &cfg, nil
}

func Load(path string) (Config, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return Config{}, err
	}
	absPath = filepath.Clean(absPath)
	if err := contracts.EnsureCleanAbsolutePath(absPath); err != nil {
		return Config{}, err
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return Config{}, err
	}

	var cfg Config
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, err
	}
	var rest any
	if err := dec.Decode(&rest); !errors.Is(err, io.EOF) {
		if err == nil {
			return Config{}, errors.New("config: YAML must contain exactly one document")
		}
		return Config{}, err
	}

	cfg.applyDefaults()
	cfg.configPath = absPath
	if err := cfg.loadAgentFile(); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Agents.Implementer == "" {
		c.Agents.Implementer = "claude"
	}
	if c.Agents.JudgePrimary == "" {
		c.Agents.JudgePrimary = "claude"
	}
	if c.Agents.JudgeSecondary == "" {
		c.Agents.JudgeSecondary = "codex"
	}
	if c.TaskPrompt.Source == "" {
		c.TaskPrompt.Source = "auto"
	}
	if c.RegistryHighThreshold == 0 {
		c.RegistryHighThreshold = DefaultRegistryHighThreshold
	}
	if c.RegistryCriticalThreshold == 0 {
		c.RegistryCriticalThreshold = DefaultRegistryCriticalThreshold
	}
	if len(c.StepTimeouts) == 0 {
		c.StepTimeouts = make(map[string]int, len(defaultStepTimeouts))
	}
	for key, value := range defaultStepTimeouts {
		if c.StepTimeouts[key] == 0 {
			c.StepTimeouts[key] = value
		}
	}
}

func (c Config) RepoRoot() (string, error) {
	if c.repoRoot != "" {
		return c.repoRoot, nil
	}
	return c.resolveRepoRoot()
}

func (c Config) RunsBase() (string, error) {
	value := c.Paths.Runs
	if value == "" {
		value = c.RunsBasePath
	}
	if value == "" {
		return "", errors.New("config: RunsBase is required")
	}
	resolved, err := c.resolvePath(value)
	if err != nil {
		return "", err
	}
	return c.namespaceStatePath(resolved, defaultRunsDirName), nil
}

func (c Config) WorktreeBase() (string, error) {
	value := c.Worktree.Base
	if value == "" {
		value = c.WorktreeBasePath
	}
	if value == "" {
		return "", errors.New("config: WorktreeBase is required")
	}
	resolved, err := c.resolvePath(value)
	if err != nil {
		return "", err
	}
	return c.namespaceStatePath(resolved, defaultWorktreesDirName), nil
}

func (c Config) PolicyBranch() (string, bool) {
	if strings.TrimSpace(c.Repo.PolicyBranch) == "" {
		return "", false
	}
	return c.Repo.PolicyBranch, true
}

func (c Config) namespaceStatePath(path, leaf string) string {
	namespace, ok := c.repoStateNamespace()
	if !ok {
		return path
	}
	if filepath.Base(filepath.Dir(path)) == namespace {
		return path
	}
	return filepath.Join(filepath.Dir(path), namespace, filepath.Base(path))
}

func (c Config) repoStateNamespace() (string, bool) {
	repo := strings.TrimSpace(c.Repo.GitHub)
	if repo == "" {
		return "", false
	}
	return sanitizeRepoNamespace(repo), true
}

func sanitizeRepoNamespace(repo string) string {
	var b strings.Builder
	b.Grow(len(repo) + 2)
	lastSep := false
	for _, r := range strings.ToLower(strings.TrimSpace(repo)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
			lastSep = false
		case r == '/':
			b.WriteString("__")
			lastSep = false
		default:
			if !lastSep {
				b.WriteRune('-')
				lastSep = true
			}
		}
	}
	return strings.Trim(b.String(), "-._")
}

func (c Config) ProcessedPath() (string, error) {
	runsBase, err := c.RunsBase()
	if err != nil {
		return "", err
	}
	path := filepath.Join(runsBase, "processed.jsonl")
	if err := contracts.EnsureCleanAbsolutePath(path); err != nil {
		return "", err
	}
	return path, nil
}

func (c Config) PromotionLockPath() (string, error) {
	runsBase, err := c.RunsBase()
	if err != nil {
		return "", err
	}
	path := filepath.Join(runsBase, "promotion.lock")
	if err := contracts.EnsureCleanAbsolutePath(path); err != nil {
		return "", err
	}
	return path, nil
}

func (c Config) ClaudeBinary() string {
	if c.ClaudeCLIPath != "" {
		return c.ClaudeCLIPath
	}
	if c.Agents.Implementer != "" {
		return c.Agents.Implementer
	}
	if c.Agents.JudgePrimary != "" {
		return c.Agents.JudgePrimary
	}
	return "claude"
}

func (c Config) CodexBinary() string {
	if c.CodexCLIPath != "" {
		return c.CodexCLIPath
	}
	if c.Agents.JudgeSecondary != "" {
		return c.Agents.JudgeSecondary
	}
	return "codex"
}

func (c Config) AgentFile() agents.File {
	if len(c.agentFile.Profiles) == 0 || len(c.agentFile.Roles) == 0 {
		return agents.Legacy(agents.LegacyDefaults{
			ImplementerBinary:    c.legacyImplementerBinary(),
			JudgePrimaryBinary:   c.legacyJudgePrimaryBinary(),
			JudgeSecondaryBinary: c.legacyJudgeSecondaryBinary(),
		})
	}
	return c.agentFile
}

func (c Config) AgentProfile(role agents.Role) (agents.Profile, error) {
	return c.AgentFile().ProfileForRole(role)
}

func (c Config) Validate() error {
	if c.Paths.Runs == "" && c.RunsBasePath == "" {
		return errors.New("config: RunsBase is required")
	}
	if c.Worktree.Base == "" && c.WorktreeBasePath == "" {
		return errors.New("config: WorktreeBase is required")
	}
	if err := c.rejectConflictingPathAliases(); err != nil {
		return err
	}
	if c.RegistryCriticalThreshold <= c.RegistryHighThreshold {
		return fmt.Errorf(
			"config: registry_critical_threshold must be greater than registry_high_threshold: critical=%d high=%d",
			c.RegistryCriticalThreshold,
			c.RegistryHighThreshold,
		)
	}
	if len(c.StepTimeouts) > 0 {
		for _, key := range requiredStepTimeoutKeys {
			if _, ok := c.StepTimeouts[key]; !ok {
				return fmt.Errorf("config: step_timeouts missing required key: %s", key)
			}
		}
	}

	if c.RunsBasePath != "" {
		if err := contracts.EnsureCleanAbsolutePath(filepath.Clean(c.RunsBasePath)); err != nil {
			return err
		}
	}
	if c.WorktreeBasePath != "" {
		if err := contracts.EnsureCleanAbsolutePath(filepath.Clean(c.WorktreeBasePath)); err != nil {
			return err
		}
	}
	if c.Repo.Root != "" {
		if err := contracts.EnsureCleanAbsolutePath(filepath.Clean(c.Repo.Root)); err != nil {
			return err
		}
	}
	if c.Paths.StateFile != "" {
		return errors.New("config: paths.state_file override is not supported")
	}
	if c.Paths.RulesRegistry != "" {
		return errors.New("config: paths.rules_registry override is not supported")
	}
	if c.Repo.GitHub != "" && c.Repo.DefaultBranch == "" {
		return errors.New("config: repo.default_branch is required when repo.github is set")
	}
	if err := c.AgentFile().Validate(); err != nil {
		return err
	}

	type validationView struct {
		RegistryHighThreshold     int    `validate:"gt=0"`
		RegistryCriticalThreshold int    `validate:"gt=0"`
		PreflightTimeoutSec       int    `validate:"omitempty,gt=0"`
		RescueMaxRetries          int    `validate:"omitempty,gt=0"`
		TaskPromptSource          string `validate:"required,oneof=auto issue pr diff_synth"`
	}
	return validation.Instance().Struct(validationView{
		RegistryHighThreshold:     c.RegistryHighThreshold,
		RegistryCriticalThreshold: c.RegistryCriticalThreshold,
		PreflightTimeoutSec:       c.PreflightTimeoutSec,
		RescueMaxRetries:          c.RescueMaxRetries,
		TaskPromptSource:          c.TaskPromptSource(),
	})
}

func (c Config) TaskPromptSource() string {
	if strings.TrimSpace(c.TaskPrompt.Source) == "" {
		return "auto"
	}
	return c.TaskPrompt.Source
}

func (c *Config) loadAgentFile() error {
	legacy := agents.Legacy(agents.LegacyDefaults{
		ImplementerBinary:    c.legacyImplementerBinary(),
		JudgePrimaryBinary:   c.legacyJudgePrimaryBinary(),
		JudgeSecondaryBinary: c.legacyJudgeSecondaryBinary(),
	})
	path := c.AgentConfigPath
	if path == "" {
		if c.configPath == "" {
			c.agentFile = legacy
			return nil
		}
		path = filepath.Join(filepath.Dir(c.configPath), agents.DefaultFileName())
	}
	if !filepath.IsAbs(path) {
		if c.configPath != "" {
			path = filepath.Join(filepath.Dir(c.configPath), path)
		}
	}
	path = filepath.Clean(path)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			c.agentFile = legacy
			return nil
		}
		return err
	}
	file, err := agents.Load(path)
	if err != nil {
		return err
	}
	c.agentFile = file
	return nil
}

func (c Config) legacyImplementerBinary() string {
	if c.ClaudeCLIPath != "" {
		return c.ClaudeCLIPath
	}
	if c.Agents.Implementer != "" {
		return c.Agents.Implementer
	}
	return "claude"
}

func (c Config) legacyJudgePrimaryBinary() string {
	if c.ClaudeCLIPath != "" {
		return c.ClaudeCLIPath
	}
	if c.Agents.JudgePrimary != "" {
		return c.Agents.JudgePrimary
	}
	return "claude"
}

func (c Config) legacyJudgeSecondaryBinary() string {
	if c.CodexCLIPath != "" {
		return c.CodexCLIPath
	}
	if c.Agents.JudgeSecondary != "" {
		return c.Agents.JudgeSecondary
	}
	return "codex"
}

func (c Config) rejectConflictingPathAliases() error {
	if c.Paths.Runs != "" && c.RunsBasePath != "" {
		if same, err := c.sameResolvedPath(c.Paths.Runs, c.RunsBasePath); err != nil {
			return err
		} else if !same {
			return errors.New("config: paths.runs and runs_base both set different paths; keep only paths.runs")
		}
	}
	if c.Worktree.Base != "" && c.WorktreeBasePath != "" {
		if same, err := c.sameResolvedPath(c.Worktree.Base, c.WorktreeBasePath); err != nil {
			return err
		} else if !same {
			return errors.New("config: worktree.base and worktree_base both set different paths; keep only worktree.base")
		}
	}
	return nil
}

func (c Config) sameResolvedPath(left, right string) (bool, error) {
	resolvedLeft, err := c.resolvePath(left)
	if err != nil {
		return false, err
	}
	resolvedRight, err := c.resolvePath(right)
	if err != nil {
		return false, err
	}
	return resolvedLeft == resolvedRight, nil
}

func (c Config) resolveRepoRoot() (string, error) {
	if c.Repo.Root != "" {
		return c.resolvePath(c.Repo.Root)
	}
	start := "."
	if c.configPath != "" {
		start = filepath.Dir(c.configPath)
	}
	return findRepoRoot(start)
}

func (c Config) resolvePath(value string) (string, error) {
	if value == "" {
		return "", errors.New("config: path is empty")
	}
	if strings.HasPrefix(value, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		value = filepath.Join(home, value[2:])
	}
	if filepath.IsAbs(value) {
		value = filepath.Clean(value)
		if err := contracts.EnsureCleanAbsolutePath(value); err != nil {
			return "", err
		}
		return value, nil
	}

	root := c.repoRoot
	if root == "" {
		var err error
		root, err = c.resolveRepoRoot()
		if err != nil {
			if c.configPath == "" {
				return "", err
			}
			root = filepath.Dir(c.configPath)
		}
	}
	resolved := filepath.Clean(filepath.Join(root, value))
	if err := contracts.EnsureCleanAbsolutePath(resolved); err != nil {
		return "", err
	}
	return resolved, nil
}

func findRepoRoot(start string) (string, error) {
	current, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	current = filepath.Clean(current)
	for {
		gitPath := filepath.Join(current, ".git")
		if _, err := os.Stat(gitPath); err == nil {
			if err := contracts.EnsureCleanAbsolutePath(current); err != nil {
				return "", err
			}
			return current, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("config: could not find repository root from %q", start)
		}
		current = parent
	}
}
