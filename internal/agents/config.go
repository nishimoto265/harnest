package agents

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"gopkg.in/yaml.v3"
)

type Role string

const (
	RoleImplementer    Role = "implementer"
	RoleJudgePrimary   Role = "judge_primary"
	RoleJudgeSecondary Role = "judge_secondary"
	RoleJudgeArbiter   Role = "judge_arbiter"
)

type Provider string

const (
	ProviderClaude        Provider = "claude"
	ProviderCodex         Provider = "codex"
	ProviderStub          Provider = "stub"
	ProviderStubViolation Provider = "stub_violation"
	ProviderStubAdopt     Provider = "stub_adopt"
)

const AllowTestStubProvidersEnv = "AUTO_IMPROVE_ALLOW_TEST_STUB_PROVIDERS"

type Profile struct {
	Provider Provider `yaml:"provider"`
	Binary   string   `yaml:"binary"`
	Args     []string `yaml:"args"`
}

type File struct {
	Profiles map[string]Profile `yaml:"profiles"`
	Roles    map[Role]string    `yaml:"roles"`
}

type LegacyDefaults struct {
	ImplementerBinary string
}

func DefaultFileName() string {
	return "agents.yaml"
}

func Load(path string) (File, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return File{}, err
	}
	absPath = filepath.Clean(absPath)
	if err := contracts.EnsureCleanAbsolutePath(absPath); err != nil {
		return File{}, err
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return File{}, err
	}

	var cfg File
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return File{}, err
	}
	var rest any
	if err := dec.Decode(&rest); !errors.Is(err, io.EOF) {
		if err == nil {
			return File{}, errors.New("agents: YAML must contain exactly one document")
		}
		return File{}, err
	}

	if err := cfg.Validate(); err != nil {
		return File{}, err
	}
	return cfg, nil
}

func Legacy(defaults LegacyDefaults) File {
	if defaults.ImplementerBinary == "" {
		defaults.ImplementerBinary = "claude"
	}
	implementerProvider := inferProviderFromBinary(defaults.ImplementerBinary, ProviderClaude)
	implementerArgs := []string(nil)
	if implementerProvider == ProviderClaude {
		implementerArgs = []string{"-p"}
	}
	return File{
		Profiles: map[string]Profile{
			"claude":          {Provider: implementerProvider, Binary: defaults.ImplementerBinary, Args: implementerArgs},
			"judge-primary":   {Provider: ProviderStub},
			"judge-secondary": {Provider: ProviderStub},
			"judge-arbiter":   {Provider: ProviderStub},
		},
		Roles: map[Role]string{
			RoleImplementer:    "claude",
			RoleJudgePrimary:   "judge-primary",
			RoleJudgeSecondary: "judge-secondary",
			RoleJudgeArbiter:   "judge-arbiter",
		},
	}
}

func inferProviderFromBinary(binary string, fallback Provider) Provider {
	base := strings.ToLower(filepath.Base(strings.TrimSpace(binary)))
	switch {
	case strings.Contains(base, "codex"):
		return ProviderCodex
	case strings.Contains(base, "claude"):
		return ProviderClaude
	default:
		return fallback
	}
}

func IsGatedTestStubProvider(provider Provider) bool {
	return provider == ProviderStubViolation || provider == ProviderStubAdopt
}

func AllowTestStubProviders() bool {
	return os.Getenv(AllowTestStubProvidersEnv) == "1"
}

func (f File) Validate() error {
	if len(f.Profiles) == 0 {
		return errors.New("agents: profiles is required")
	}
	if len(f.Roles) == 0 {
		return errors.New("agents: roles is required")
	}
	for name, profile := range f.Profiles {
		if strings.TrimSpace(name) == "" {
			return errors.New("agents: profile name must not be empty")
		}
		switch profile.Provider {
		case ProviderClaude, ProviderCodex:
			if strings.TrimSpace(profile.Binary) == "" {
				return fmt.Errorf("agents: profile %q requires binary", name)
			}
		case ProviderStub, ProviderStubViolation, ProviderStubAdopt:
		default:
			return fmt.Errorf("agents: unsupported provider %q for profile %q", profile.Provider, name)
		}
	}
	for _, role := range []Role{RoleImplementer, RoleJudgePrimary, RoleJudgeSecondary, RoleJudgeArbiter} {
		profileName, ok := f.Roles[role]
		if !ok || strings.TrimSpace(profileName) == "" {
			return fmt.Errorf("agents: role %q is required", role)
		}
		if _, ok := f.Profiles[profileName]; !ok {
			return fmt.Errorf("agents: role %q references unknown profile %q", role, profileName)
		}
	}
	implementer := f.Profiles[f.Roles[RoleImplementer]]
	if implementer.Provider != ProviderClaude && implementer.Provider != ProviderCodex {
		return fmt.Errorf("agents: role %q must use provider %q or %q, got %q", RoleImplementer, ProviderClaude, ProviderCodex, implementer.Provider)
	}
	return nil
}

func (f File) ProfileForRole(role Role) (Profile, error) {
	name, ok := f.Roles[role]
	if !ok {
		return Profile{}, fmt.Errorf("agents: role %q is not configured", role)
	}
	profile, ok := f.Profiles[name]
	if !ok {
		return Profile{}, fmt.Errorf("agents: role %q references unknown profile %q", role, name)
	}
	return profile, nil
}
