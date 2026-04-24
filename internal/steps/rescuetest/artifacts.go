package rescuetest

import (
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

func WriteIgnoredCoverageArtifacts(rescueDir string, fileDigest FileDigestFunc) ([]agentrunner.RescueArtifactDigest, error) {
	return writeCoverageArtifacts(rescueDir, fileDigest, []string{"ignored-skipped.txt", "ignored.txt"})
}

func WritePartialIgnoredCoverageArtifacts(rescueDir string, fileDigest FileDigestFunc) ([]agentrunner.RescueArtifactDigest, error) {
	return writeCoverageArtifacts(rescueDir, fileDigest, []string{"ignored.txt"})
}

func writeCoverageArtifacts(rescueDir string, fileDigest FileDigestFunc, paths []string) ([]agentrunner.RescueArtifactDigest, error) {
	artifacts := make([]agentrunner.RescueArtifactDigest, 0, len(paths))
	for _, rel := range paths {
		path := filepath.Join(rescueDir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			return nil, err
		}
		digest, err := fileDigest(path)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, agentrunner.RescueArtifactDigest{Path: rel, SHA256: digest})
	}
	return artifacts, nil
}
