package agentrunner

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/nishimoto265/auto-improve/internal/agents"
)

func ImplementerCommand(profile agents.Profile, workdir string) (string, []string, error) {
	switch profile.Provider {
	case agents.ProviderClaude:
		return profile.Binary, append([]string(nil), profile.Args...), nil
	case agents.ProviderCodex:
		binary, prefixArgs, err := PrepareProviderBinary(profile.Provider, profile.Binary)
		if err != nil {
			return "", nil, err
		}
		args := append([]string{}, CodexExecArgs(workdir)...)
		args = append(args, profile.Args...)
		args = append(args, "-")
		return binary, append(prefixArgs, args...), nil
	default:
		return "", nil, fmt.Errorf("agentrunner: unsupported implementer provider %q", profile.Provider)
	}
}

func PrepareProviderBinary(provider agents.Provider, binary string) (string, []string, error) {
	switch provider {
	case agents.ProviderClaude:
		return prepareNodeShebangBinary(binary)
	case agents.ProviderCodex:
		return prepareCodexBinary(binary)
	default:
		return binary, nil, nil
	}
}

func CodexExecArgs(workdir string) []string {
	return []string{
		"exec",
		"--full-auto",
		"--skip-git-repo-check",
		"-C", workdir,
	}
}

func prepareCodexBinary(binary string) (string, []string, error) {
	return prepareNodeShebangBinary(binary)
}

func prepareNodeShebangBinary(binary string) (string, []string, error) {
	resolved, err := resolveBinary(binary)
	if err != nil {
		return "", nil, err
	}
	needsNode, err := scriptNeedsNode(resolved)
	if err != nil || !needsNode {
		return binary, nil, err
	}
	nodeBinary, err := resolveNodeBinary(filepath.Dir(resolved))
	if err != nil {
		return "", nil, err
	}
	return nodeBinary, []string{resolved}, nil
}

func resolveBinary(binary string) (string, error) {
	if binary == "" {
		return "", fmt.Errorf("agentrunner: binary is required")
	}
	if filepath.IsAbs(binary) {
		return binary, nil
	}
	return exec.LookPath(binary)
}

func scriptNeedsNode(path string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	line, err := bufio.NewReader(file).ReadString('\n')
	if err != nil && err != io.EOF {
		return false, err
	}
	return strings.HasPrefix(strings.TrimSpace(line), "#!/usr/bin/env node"), nil
}

func resolveNodeBinary(binaryDir string) (string, error) {
	sibling := filepath.Join(binaryDir, "node")
	if info, err := os.Stat(sibling); err == nil && !info.IsDir() {
		return sibling, nil
	}
	return exec.LookPath("node")
}
