package agentrunner

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/nishimoto265/auto-improve/internal/agents"
)

type providerRuntime interface {
	PrepareBinary(profile agents.Profile) (string, []string, error)
	ImplementerArgs(profile agents.Profile, workdir string) ([]string, error)
	ReadOnlyArgs(profile agents.Profile, workdir, outputPath string) ([]string, error)
	ReadOnlyResponsePath(sessionPath, outputPath string) string
	CleanupReadOnlyArtifacts(sessionPath, outputPath string)
	PrepareReadOnlyWorkspace(defaultWorkdir, tempPattern string, files []WorkspaceFile) (ProviderWorkspace, error)
}

type claudeRuntime struct{}
type codexRuntime struct{}

type ReadOnlyCommand struct {
	Binary       string
	Args         []string
	Workdir      string
	SessionPath  string
	ResponsePath string
	Env          []string
	Cleanup      func()
}

type WorkspaceFile struct {
	Key        string
	SourcePath string
	TargetName string
}

type ProviderWorkspace struct {
	Workdir string
	Files   map[string]string
	Cleanup func()
}

func runtimeForProvider(provider agents.Provider) (providerRuntime, error) {
	switch provider {
	case agents.ProviderClaude:
		return claudeRuntime{}, nil
	case agents.ProviderCodex:
		return codexRuntime{}, nil
	default:
		return nil, fmt.Errorf("agentrunner: unsupported provider %q", provider)
	}
}

func PrepareReadOnlyCommand(profile agents.Profile, workdir string) (ReadOnlyCommand, error) {
	runtime, err := runtimeForProvider(profile.Provider)
	if err != nil {
		return ReadOnlyCommand{}, fmt.Errorf("agentrunner: unsupported read-only provider %q", profile.Provider)
	}
	sessionPath, err := tempJSONFile("session")
	if err != nil {
		return ReadOnlyCommand{}, err
	}
	outputPath, err := tempJSONFile("output")
	if err != nil {
		_ = os.Remove(sessionPath)
		return ReadOnlyCommand{}, err
	}
	binary, prefixArgs, err := runtime.PrepareBinary(profile)
	if err != nil {
		_ = os.Remove(sessionPath)
		_ = os.Remove(outputPath)
		return ReadOnlyCommand{}, err
	}
	providerArgs, err := runtime.ReadOnlyArgs(profile, workdir, outputPath)
	if err != nil {
		_ = os.Remove(sessionPath)
		_ = os.Remove(outputPath)
		return ReadOnlyCommand{}, err
	}
	args := append([]string{}, prefixArgs...)
	args = append(args, providerArgs...)
	responsePath := runtime.ReadOnlyResponsePath(sessionPath, outputPath)
	return ReadOnlyCommand{
		Binary:       binary,
		Args:         args,
		Workdir:      workdir,
		SessionPath:  sessionPath,
		ResponsePath: responsePath,
		Env:          ProfileEnv(profile),
		Cleanup: func() {
			runtime.CleanupReadOnlyArtifacts(sessionPath, outputPath)
		},
	}, nil
}

func PrepareReadOnlyWorkspace(provider agents.Provider, defaultWorkdir, tempPattern string, files []WorkspaceFile) (ProviderWorkspace, error) {
	runtime, err := runtimeForProvider(provider)
	if err != nil {
		return ProviderWorkspace{}, err
	}
	return runtime.PrepareReadOnlyWorkspace(defaultWorkdir, tempPattern, files)
}

func (claudeRuntime) PrepareBinary(profile agents.Profile) (string, []string, error) {
	return prepareNodeShebangBinary(profile.Binary, profile.NodeBinary)
}

func (claudeRuntime) ImplementerArgs(profile agents.Profile, _ string) ([]string, error) {
	return append([]string(nil), profile.Args...), nil
}

func (claudeRuntime) ReadOnlyArgs(profile agents.Profile, _ string, _ string) ([]string, error) {
	if err := agents.ValidateJudgeProfileArgs(agents.ProviderClaude, profile.Args); err != nil {
		return nil, err
	}
	args := []string{
		"-p",
		"--output-format", "json",
		"--allowedTools", "Read",
	}
	return append(args, profile.Args...), nil
}

func (claudeRuntime) ReadOnlyResponsePath(sessionPath, _ string) string {
	return sessionPath
}

func (claudeRuntime) CleanupReadOnlyArtifacts(_, outputPath string) {
	_ = os.Remove(outputPath)
}

func (claudeRuntime) PrepareReadOnlyWorkspace(defaultWorkdir, _ string, files []WorkspaceFile) (ProviderWorkspace, error) {
	paths := make(map[string]string, len(files))
	for _, file := range files {
		paths[file.Key] = file.SourcePath
	}
	return ProviderWorkspace{
		Workdir: defaultWorkdir,
		Files:   paths,
		Cleanup: func() {},
	}, nil
}

func (codexRuntime) PrepareBinary(profile agents.Profile) (string, []string, error) {
	return prepareCodexBinary(profile.Binary, profile.NodeBinary)
}

func (codexRuntime) ImplementerArgs(profile agents.Profile, workdir string) ([]string, error) {
	args := append([]string{}, CodexExecArgs(workdir)...)
	args = append(args, profile.Args...)
	args = append(args, "-")
	return args, nil
}

func (codexRuntime) ReadOnlyArgs(profile agents.Profile, workdir, outputPath string) ([]string, error) {
	if err := agents.ValidateJudgeProfileArgs(agents.ProviderCodex, profile.Args); err != nil {
		return nil, err
	}
	args := []string{
		"exec",
		"--sandbox", "read-only",
		"--skip-git-repo-check",
		"--ephemeral",
		"-c", `web_search="disabled"`,
		"-C", workdir,
	}
	args = append(args, profile.Args...)
	args = append(args, "-o", outputPath, "-")
	return args, nil
}

func (codexRuntime) ReadOnlyResponsePath(_, outputPath string) string {
	return outputPath
}

func (codexRuntime) CleanupReadOnlyArtifacts(sessionPath, _ string) {
	_ = os.Remove(sessionPath)
}

func (codexRuntime) PrepareReadOnlyWorkspace(_ string, tempPattern string, files []WorkspaceFile) (ProviderWorkspace, error) {
	dir, err := os.MkdirTemp("", tempPattern)
	if err != nil {
		return ProviderWorkspace{}, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	paths := make(map[string]string, len(files))
	for _, file := range files {
		targetName := file.TargetName
		if targetName == "" {
			targetName = filepath.Base(file.SourcePath)
		}
		targetPath := filepath.Join(dir, targetName)
		if err := copyWorkspaceFile(file.SourcePath, targetPath); err != nil {
			cleanup()
			return ProviderWorkspace{}, err
		}
		paths[file.Key] = targetPath
	}
	return ProviderWorkspace{
		Workdir: dir,
		Files:   paths,
		Cleanup: cleanup,
	}, nil
}

func tempJSONFile(prefix string) (string, error) {
	file, err := os.CreateTemp("", "auto-improve-"+prefix+"-*.json")
	if err != nil {
		return "", err
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		return "", err
	}
	return path, nil
}

func copyWorkspaceFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("agentrunner: copy workspace file read %s: %w", src, err)
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		return fmt.Errorf("agentrunner: copy workspace file write %s: %w", dst, err)
	}
	return nil
}
