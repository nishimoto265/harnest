package agents

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/nishimoto265/harnest/internal/contracts"
	"gopkg.in/yaml.v3"
)

type Role string

const (
	RoleImplementer   Role = "implementer"
	RoleJudgePrimary  Role = "judge_primary"
	RoleTaskGenerator Role = "task_generator"
)

type Provider string

const (
	ProviderClaude        Provider = "claude"
	ProviderCodex         Provider = "codex"
	ProviderStub          Provider = "stub"
	ProviderStubViolation Provider = "stub_violation"
	ProviderStubAdopt     Provider = "stub_adopt"
)

const AllowTestStubProvidersEnv = "HARNEST_ALLOW_TEST_STUB_PROVIDERS"

type Profile struct {
	Provider   Provider `yaml:"provider"`
	Binary     string   `yaml:"binary"`
	NodeBinary string   `yaml:"node_binary"`
	Args       []string `yaml:"args"`
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
			"claude":        {Provider: implementerProvider, Binary: defaults.ImplementerBinary, Args: implementerArgs},
			"judge-primary": {Provider: ProviderStub},
		},
		Roles: map[Role]string{
			RoleImplementer:  "claude",
			RoleJudgePrimary: "judge-primary",
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
		if strings.TrimSpace(profile.NodeBinary) != "" && profile.Provider != ProviderClaude && profile.Provider != ProviderCodex {
			return fmt.Errorf("agents: profile %q node_binary is only supported for provider %q or %q", name, ProviderClaude, ProviderCodex)
		}
	}
	for _, role := range []Role{RoleImplementer, RoleJudgePrimary} {
		profileName, ok := f.Roles[role]
		if !ok || strings.TrimSpace(profileName) == "" {
			return fmt.Errorf("agents: role %q is required", role)
		}
		if _, ok := f.Profiles[profileName]; !ok {
			return fmt.Errorf("agents: role %q references unknown profile %q", role, profileName)
		}
		if err := ValidateProfileArgsForRole(role, f.Profiles[profileName]); err != nil {
			return err
		}
	}
	if profileName := strings.TrimSpace(f.Roles[RoleTaskGenerator]); profileName != "" {
		profile, ok := f.Profiles[profileName]
		if !ok {
			return fmt.Errorf("agents: role %q references unknown profile %q", RoleTaskGenerator, profileName)
		}
		if err := ValidateProfileArgsForRole(RoleTaskGenerator, profile); err != nil {
			return err
		}
		if profile.Provider != ProviderClaude && profile.Provider != ProviderCodex {
			return fmt.Errorf("agents: role %q must use provider %q or %q, got %q", RoleTaskGenerator, ProviderClaude, ProviderCodex, profile.Provider)
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

func ValidateProfileArgsForRole(role Role, profile Profile) error {
	switch role {
	case RoleJudgePrimary, RoleTaskGenerator:
		return ValidateJudgeProfileArgs(profile.Provider, profile.Args)
	default:
		return nil
	}
}

func ValidateJudgeProfileArgs(provider Provider, args []string) error {
	switch provider {
	case ProviderClaude:
		return validateClaudeJudgeProfileArgs(args)
	case ProviderCodex:
		return validateCodexJudgeProfileArgs(args)
	default:
		return nil
	}
}

func validateClaudeJudgeProfileArgs(args []string) error {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if claudeJudgeProfileArgIsBlocked(arg) {
			return fmt.Errorf("agents: claude judge profile arg %q is not allowed", arg)
		}
		if claudeJudgeProfileArgRequiresValue(arg) {
			if i+1 >= len(args) {
				return fmt.Errorf("agents: claude judge profile arg %q requires a value", arg)
			}
			i++
		}
	}
	return nil
}

func claudeJudgeProfileArgIsBlocked(arg string) bool {
	name, _, hasValue := strings.Cut(arg, "=")
	if hasValue {
		arg = name
	}
	switch arg {
	case "--allowedTools",
		"--allowed-tools",
		"--disallowedTools",
		"--disallowed-tools",
		"--output-format",
		"--cwd",
		"--add-dir",
		"--permission-mode",
		"--permission-prompt-tool",
		"--dangerously-skip-permissions",
		"--mcp-config",
		"--settings",
		"--profile":
		return true
	default:
		return false
	}
}

func claudeJudgeProfileArgRequiresValue(arg string) bool {
	if strings.Contains(arg, "=") {
		return false
	}
	switch arg {
	case "--model",
		"--append-system-prompt",
		"--fallback-model",
		"--max-turns":
		return true
	default:
		return false
	}
}

func validateCodexJudgeProfileArgs(args []string) error {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--model" || arg == "-m":
			if i+1 >= len(args) {
				return fmt.Errorf("agents: codex judge profile arg %q requires a value", arg)
			}
			if !codexJudgeProfileArgValueIsSafe(args[i+1]) {
				return fmt.Errorf("agents: codex judge profile arg %q has invalid value %q", arg, args[i+1])
			}
			i++
		case strings.HasPrefix(arg, "--model="):
			if !codexJudgeProfileArgValueIsSafe(strings.TrimPrefix(arg, "--model=")) {
				return fmt.Errorf("agents: codex judge profile arg %q requires a value", arg)
			}
		case strings.HasPrefix(arg, "-m="):
			if !codexJudgeProfileArgValueIsSafe(strings.TrimPrefix(arg, "-m=")) {
				return fmt.Errorf("agents: codex judge profile arg %q requires a value", arg)
			}
		default:
			return fmt.Errorf("agents: codex judge profile arg %q is not allowed", arg)
		}
	}
	return nil
}

func codexJudgeProfileArgValueIsSafe(value string) bool {
	return value != "" && !strings.HasPrefix(value, "-")
}
