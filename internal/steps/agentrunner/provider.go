package agentrunner

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/nishimoto265/auto-improve/internal/agents"
	"github.com/nishimoto265/auto-improve/internal/processenv"
)

func ImplementerCommand(profile agents.Profile, workdir string) (string, []string, error) {
	runtime, err := runtimeForProvider(profile.Provider)
	if err != nil {
		return "", nil, fmt.Errorf("agentrunner: unsupported implementer provider %q", profile.Provider)
	}
	binary, prefixArgs, err := runtime.PrepareBinary(profile)
	if err != nil {
		return "", nil, err
	}
	args, err := runtime.ImplementerArgs(profile, workdir)
	if err != nil {
		return "", nil, err
	}
	return binary, append(prefixArgs, args...), nil
}

func PrepareProfileBinary(profile agents.Profile) (string, []string, error) {
	runtime, err := runtimeForProvider(profile.Provider)
	if err != nil {
		return "", nil, err
	}
	return runtime.PrepareBinary(profile)
}

func ProfileEnv(profile agents.Profile) []string {
	if version := nodenvVersionFromNodeBinary(profile.NodeBinary); version != "" {
		return []string{"NODENV_VERSION=" + version}
	}
	return nil
}

func CurrentExecutableEnv() []string {
	executable, err := os.Executable()
	if err != nil || executable == "" {
		return nil
	}
	return []string{"AUTO_IMPROVE_BIN=" + executable}
}

func PrepareProviderBinary(provider agents.Provider, binary string) (string, []string, error) {
	return PrepareProviderBinaryWithNode(provider, binary, "")
}

func PrepareProviderBinaryWithNode(provider agents.Provider, binary, nodeBinary string) (string, []string, error) {
	runtime, err := runtimeForProvider(provider)
	if err != nil {
		return binary, nil, nil
	}
	return runtime.PrepareBinary(agents.Profile{
		Provider:   provider,
		Binary:     binary,
		NodeBinary: nodeBinary,
	})
}

func CodexExecArgs(workdir string) []string {
	return []string{
		"exec",
		"--full-auto",
		"--skip-git-repo-check",
		"-C", workdir,
	}
}

func prepareCodexBinary(binary, nodeBinary string) (string, []string, error) {
	return prepareNodeShebangBinary(binary, nodeBinary)
}

func prepareNodeShebangBinary(binary, nodeBinary string) (string, []string, error) {
	resolved, err := resolveBinary(binary)
	if err != nil {
		return "", nil, err
	}
	needsNode, err := scriptNeedsNode(resolved)
	if err != nil || !needsNode {
		if nodeBinary != "" && err == nil {
			return "", nil, fmt.Errorf("agentrunner: node_binary is set but provider binary does not use a node shebang: %s", resolved)
		}
		return resolved, nil, err
	}
	if nodeBinary != "" {
		node, err := resolveBinary(nodeBinary)
		if err != nil {
			return "", nil, err
		}
		return node, []string{resolved}, nil
	}
	nodeBinary, err = resolveNodeBinary(filepath.Dir(resolved))
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
		return processenv.TrustedLookPath(binary)
	}
	return processenv.TrustedLookPath(binary)
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
	return processenv.TrustedLookPath("node")
}

func nodenvVersionFromNodeBinary(nodeBinary string) string {
	nodeBinary = strings.TrimSpace(nodeBinary)
	if nodeBinary == "" {
		return ""
	}
	clean := filepath.Clean(nodeBinary)
	if filepath.Base(clean) != "node" {
		return ""
	}
	binDir := filepath.Dir(clean)
	if filepath.Base(binDir) != "bin" {
		return ""
	}
	versionDir := filepath.Dir(binDir)
	if filepath.Base(filepath.Dir(versionDir)) != "versions" {
		return ""
	}
	return filepath.Base(versionDir)
}
