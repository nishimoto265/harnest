package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/validation"
	"gopkg.in/yaml.v3"
)

const (
	DefaultRegistryHighThreshold     = 1501
	DefaultRegistryCriticalThreshold = 2001
)

var (
	ErrTrailingYAML = errors.New("config: YAML must contain exactly one document")

	requiredStepTimeoutKeys = []string{
		"step10",
		"step20",
		"step30",
		"step40",
		"step50",
		"step60",
		"step70",
	}
)

type Config struct {
	RunsBase                  string         `yaml:"runs_base" validate:"required"`
	WorktreeBase              string         `yaml:"worktree_base" validate:"required"`
	ClaudeCLIPath             string         `yaml:"claude_cli_path" validate:"required"`
	CodexCLIPath              string         `yaml:"codex_cli_path" validate:"required"`
	PreflightTimeoutSec       int            `yaml:"preflight_timeout_sec" validate:"required,gt=0"`
	RescueMaxRetries          int            `yaml:"rescue_max_retries" validate:"required,gt=0"`
	RegistryHighThreshold     int            `yaml:"registry_high_threshold" validate:"gt=0"`
	RegistryCriticalThreshold int            `yaml:"registry_critical_threshold" validate:"gt=0"`
	StepTimeouts              map[string]int `yaml:"step_timeouts" validate:"required,min=1,dive,keys,oneof=step10 step20 step30 step40 step50 step60 step70,endkeys,gt=0"`
}

func LoadConfig(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("config: open %s: %w", path, err)
	}
	defer f.Close()

	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)

	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("config: decode %s: %w", path, err)
	}

	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("config: decode %s: %w", path, ErrTrailingYAML)
		}
		return nil, fmt.Errorf("config: decode %s: %w", path, err)
	}

	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config: validate %s: %w", path, err)
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.RegistryHighThreshold == 0 {
		c.RegistryHighThreshold = DefaultRegistryHighThreshold
	}
	if c.RegistryCriticalThreshold == 0 {
		c.RegistryCriticalThreshold = DefaultRegistryCriticalThreshold
	}
}

func (c Config) Validate() error {
	if err := validation.Instance().Struct(c); err != nil {
		return err
	}
	if err := contracts.EnsureCleanAbsolutePath(c.RunsBase); err != nil {
		return err
	}
	if err := contracts.EnsureCleanAbsolutePath(c.WorktreeBase); err != nil {
		return err
	}
	if c.RegistryCriticalThreshold <= c.RegistryHighThreshold {
		return fmt.Errorf(
			"config: registry_critical_threshold must be greater than registry_high_threshold: critical=%d high=%d",
			c.RegistryCriticalThreshold,
			c.RegistryHighThreshold,
		)
	}

	missing := missingStepTimeoutKeys(c.StepTimeouts)
	if len(missing) > 0 {
		return fmt.Errorf("config: step_timeouts missing required keys: %s", strings.Join(missing, ", "))
	}
	return nil
}

func missingStepTimeoutKeys(stepTimeouts map[string]int) []string {
	missing := make([]string, 0, len(requiredStepTimeoutKeys))
	for _, key := range requiredStepTimeoutKeys {
		if _, ok := stepTimeouts[key]; !ok {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)
	return missing
}
