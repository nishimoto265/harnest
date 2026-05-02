package implementrescue

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/nishimoto265/auto-improve/internal/steps/agentrunner"
)

func FindExistingDir(agentDir, rescuedDirName, expectedBaseSHA string, nextRetry int, currentHead, currentDirtyFingerprint string, currentDirtyEntries []string, verifyState func(string) error) (string, bool, error) {
	rescueRoot := filepath.Join(agentDir, rescuedDirName)
	entries, err := os.ReadDir(rescueRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}

	var selectedDir string
	var selectedState agentrunner.RescueStateFile
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		candidateDir := filepath.Join(rescueRoot, entry.Name())
		state, err := agentrunner.ReadRescueState(filepath.Join(candidateDir, "state.json"))
		if err != nil {
			continue
		}
		if state.ExpectedBaseSHA != expectedBaseSHA || state.RetryCount != nextRetry {
			continue
		}
		if state.RescuedHeadSHA != currentHead {
			continue
		}
		if state.DirtyFingerprint == "" || state.DirtyFingerprint != currentDirtyFingerprint {
			continue
		}
		if !rescueStateHasRequiredArtifacts(state) {
			continue
		}
		if err := verifyState(candidateDir); err != nil {
			continue
		}
		if !rescueStateCoversCurrentDirty(candidateDir, state, currentDirtyEntries) {
			continue
		}
		if selectedDir == "" || state.CreatedAt.After(selectedState.CreatedAt) {
			selectedDir = candidateDir
			selectedState = state
		}
	}
	if selectedDir == "" {
		return "", false, nil
	}
	return selectedDir, true, nil
}

func rescueStateHasRequiredArtifacts(state agentrunner.RescueStateFile) bool {
	artifacts := rescueArtifactSet(state)
	for _, required := range []string{
		"commits.bundle",
		"tracked.patch",
		"staged.patch",
		"untracked-symlinks.txt",
		"ignored-skipped.txt",
		"ignored.txt",
	} {
		if !artifacts[required] {
			return false
		}
	}
	return true
}

func rescueStateCoversCurrentDirty(rescueDir string, state agentrunner.RescueStateFile, dirtyEntries []string) bool {
	artifacts := rescueArtifactSet(state)
	untrackedSkipped, err := skippedArtifactPaths(filepath.Join(rescueDir, "untracked-symlinks.txt"))
	if err != nil {
		return false
	}
	for _, entry := range dirtyEntries {
		untrackedPath, ok := untrackedStatusPath(entry)
		if !ok {
			continue
		}
		artifactPath, ok := rescueArtifactPath("untracked", untrackedPath)
		if !ok {
			return false
		}
		if !artifacts[artifactPath] && !untrackedSkipped[cleanedArtifactRelPath(untrackedPath)] {
			return false
		}
	}

	ignoredPaths, err := ignoredListPaths(filepath.Join(rescueDir, "ignored.txt"))
	if err != nil {
		return false
	}
	ignoredSkipped, err := skippedArtifactPaths(filepath.Join(rescueDir, "ignored-skipped.txt"))
	if err != nil {
		return false
	}
	for _, ignoredPath := range ignoredPaths {
		artifactPath, ok := rescueArtifactPath("ignored", ignoredPath)
		if !ok {
			return false
		}
		if !artifacts[artifactPath] && !ignoredSkipped[cleanedArtifactRelPath(ignoredPath)] {
			return false
		}
	}
	return true
}

func rescueArtifactSet(state agentrunner.RescueStateFile) map[string]bool {
	artifacts := make(map[string]bool, len(state.Artifacts))
	for _, artifact := range state.Artifacts {
		artifacts[artifact.Path] = true
	}
	return artifacts
}

func untrackedStatusPath(entry string) (string, bool) {
	if !strings.HasPrefix(entry, "?? ") {
		return "", false
	}
	path := entry[len("?? "):]
	return path, path != ""
}

func rescueArtifactPath(prefix, rel string) (string, bool) {
	cleaned := cleanedArtifactRelPath(rel)
	if cleaned == "" {
		return "", false
	}
	return filepath.ToSlash(filepath.Join(prefix, filepath.FromSlash(cleaned))), true
}

func cleanedArtifactRelPath(rel string) string {
	cleaned := filepath.Clean(rel)
	if cleaned == "." || cleaned == ".." || filepath.IsAbs(cleaned) || strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) {
		return ""
	}
	return filepath.ToSlash(cleaned)
}

func ignoredListPaths(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(data), "\n")
	paths := make([]string, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		ignoredPath, err := strconv.Unquote(line)
		if err != nil {
			return nil, err
		}
		if cleaned := cleanedArtifactRelPath(ignoredPath); cleaned != "" {
			paths = append(paths, cleaned)
		}
	}
	return paths, nil
}

func skippedArtifactPaths(path string) (map[string]bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	paths := make(map[string]bool)
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		var rel string
		switch {
		case strings.HasPrefix(line, "symlink:"):
			rel = strings.TrimPrefix(line, "symlink:")
		case strings.HasPrefix(line, "skipped_non_regular:"):
			rel = strings.TrimPrefix(line, "skipped_non_regular:")
		case strings.HasPrefix(line, "skipped_too_large:"):
			payload := strings.TrimPrefix(line, "skipped_too_large:")
			sizeSep := strings.LastIndex(payload, ":")
			if sizeSep < 0 {
				continue
			}
			rel = payload[:sizeSep]
		default:
			continue
		}
		if cleaned := cleanedArtifactRelPath(rel); cleaned != "" {
			paths[cleaned] = true
		}
	}
	return paths, nil
}
