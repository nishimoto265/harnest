package rescuetest

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/nishimoto265/auto-improve/internal/steps/agentrunner"
)

type FileDigestFunc func(string) (string, error)

func WriteCompleteCaptureArtifacts(rescueDir string, fileDigest FileDigestFunc, extraPaths ...string) ([]agentrunner.RescueArtifactDigest, error) {
	paths := []string{
		"commits.bundle",
		"tracked.patch",
		"staged.patch",
		"untracked-symlinks.txt",
		"ignored-skipped.txt",
		"ignored.txt",
	}
	paths = append(paths, extraPaths...)
	return writeCoverageArtifacts(rescueDir, fileDigest, paths)
}

func WritePartialIgnoredCoverageArtifacts(rescueDir string, fileDigest FileDigestFunc) ([]agentrunner.RescueArtifactDigest, error) {
	return writeCoverageArtifacts(rescueDir, fileDigest, []string{"ignored.txt"})
}

func WriteArtifact(rescueDir, rel string, body []byte, fileDigest FileDigestFunc) (agentrunner.RescueArtifactDigest, error) {
	path := filepath.Join(rescueDir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return agentrunner.RescueArtifactDigest{}, err
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return agentrunner.RescueArtifactDigest{}, err
	}
	digest, err := fileDigest(path)
	if err != nil {
		return agentrunner.RescueArtifactDigest{}, err
	}
	return agentrunner.RescueArtifactDigest{Path: rel, SHA256: digest}, nil
}

func FreshRescueArtifactSet(agentDir, rescuedDirName, skipDir string) (map[string]bool, string, error) {
	entries, err := os.ReadDir(filepath.Join(agentDir, rescuedDirName))
	if err != nil {
		return nil, "", err
	}
	for _, entry := range entries {
		if entry.Name() == skipDir {
			continue
		}
		state, err := agentrunner.ReadRescueState(filepath.Join(agentDir, rescuedDirName, entry.Name(), "state.json"))
		if err != nil {
			continue
		}
		artifacts := make(map[string]bool, len(state.Artifacts))
		for _, artifact := range state.Artifacts {
			artifacts[artifact.Path] = true
		}
		return artifacts, entry.Name(), nil
	}
	return nil, "", fmt.Errorf("fresh rescue state not found under %s", filepath.Join(agentDir, rescuedDirName))
}

func writeCoverageArtifacts(rescueDir string, fileDigest FileDigestFunc, paths []string) ([]agentrunner.RescueArtifactDigest, error) {
	artifacts := make([]agentrunner.RescueArtifactDigest, 0, len(paths))
	for _, rel := range paths {
		artifact, err := WriteArtifact(rescueDir, rel, nil, fileDigest)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, artifact)
	}
	return artifacts, nil
}
