package implementrescue

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/nishimoto265/harnest/internal/steps/agentrunner"
)

func FindExistingDir(agentDir, rescuedDirName, expectedBaseSHA string, nextRetry int, currentHead, currentDirtyFingerprint string, currentDirtyEntries []string, verifyState func(string, agentrunner.RescueStateFile) error) (string, bool, error) {
	rescueRoot := filepath.Join(agentDir, rescuedDirName)
	if err := internalio.EnsureNoSymlinkPathComponents(rescueRoot); err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
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
		if err := verifyState(candidateDir, state); err != nil {
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
	if rescueStateHasDestructiveSkipMarkers(rescueDir, state) {
		return false
	}
	artifacts := rescueArtifactSet(state)
	untrackedSkipped, err := skippedArtifactPathsFromState(rescueDir, state, "untracked-symlinks.txt")
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

	ignoredPaths, err := ignoredListPathsFromState(rescueDir, state, "ignored.txt")
	if err != nil {
		return false
	}
	ignoredSkipped, err := skippedArtifactPathsFromState(rescueDir, state, "ignored-skipped.txt")
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

func ignoredListPathsFromState(rescueDir string, state agentrunner.RescueStateFile, rel string) ([]string, error) {
	data, err := readVerifiedRescueArtifact(rescueDir, state, rel)
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

func rescueStateHasDestructiveSkipMarkers(rescueDir string, state agentrunner.RescueStateFile) bool {
	return artifactContainsAnyLinePrefix(rescueDir, state, "ignored-skipped.txt", "skipped_ignored_content:") ||
		artifactContainsAnyLinePrefix(
			rescueDir,
			state,
			"untracked-symlinks.txt",
			"symlink:",
			"skipped_non_regular:",
			"skipped_sensitive_path:",
			"skipped_too_large:",
		)
}

func artifactContainsAnyLinePrefix(rescueDir string, state agentrunner.RescueStateFile, rel string, prefixes ...string) bool {
	data, err := readVerifiedRescueArtifact(rescueDir, state, rel)
	if err != nil {
		return true
	}
	for _, line := range strings.Split(string(data), "\n") {
		for _, prefix := range prefixes {
			if strings.HasPrefix(strings.TrimSpace(line), prefix) {
				return true
			}
		}
	}
	return false
}

func skippedArtifactPathsFromState(rescueDir string, state agentrunner.RescueStateFile, rel string) (map[string]bool, error) {
	data, err := readVerifiedRescueArtifact(rescueDir, state, rel)
	if err != nil {
		return nil, err
	}
	return skippedArtifactPathsFromBytes(data), nil
}

func skippedArtifactPathsFromBytes(data []byte) map[string]bool {
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
		case strings.HasPrefix(line, "skipped_ignored_content:"):
			payload := strings.TrimPrefix(line, "skipped_ignored_content:")
			if unquoted, err := strconv.Unquote(payload); err == nil {
				rel = unquoted
			} else {
				rel = payload
			}
		case strings.HasPrefix(line, "skipped_sensitive_path:"):
			payload := strings.TrimPrefix(line, "skipped_sensitive_path:")
			if unquoted, err := strconv.Unquote(payload); err == nil {
				rel = unquoted
			} else {
				rel = payload
			}
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
	return paths
}

func readVerifiedRescueArtifact(rescueDir string, state agentrunner.RescueStateFile, rel string) ([]byte, error) {
	expectedDigest, ok := rescueArtifactDigestMap(state)[filepath.ToSlash(rel)]
	if !ok {
		return nil, fmt.Errorf("implementrescue: rescue artifact missing from state: %s", rel)
	}
	path := filepath.Join(rescueDir, filepath.FromSlash(rel))
	file, _, size, err := agentrunner.OpenValidatedRegularFile(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if size > agentrunner.RescueDiffLimitBytes {
		return nil, fmt.Errorf("%w: path=%s bytes=%d limit=%d", agentrunner.ErrRescueDiffOverLimit, path, size, agentrunner.RescueDiffLimitBytes)
	}
	data, err := io.ReadAll(io.LimitReader(file, agentrunner.RescueDiffLimitBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > agentrunner.RescueDiffLimitBytes {
		return nil, fmt.Errorf("%w: path=%s bytes=%d limit=%d", agentrunner.ErrRescueDiffOverLimit, path, len(data), agentrunner.RescueDiffLimitBytes)
	}
	sum := sha256.Sum256(data)
	if actualDigest := hex.EncodeToString(sum[:]); actualDigest != expectedDigest {
		return nil, fmt.Errorf("implementrescue: rescue artifact digest mismatch: path=%s", rel)
	}
	return data, nil
}

func rescueArtifactDigestMap(state agentrunner.RescueStateFile) map[string]string {
	artifacts := make(map[string]string, len(state.Artifacts))
	for _, artifact := range state.Artifacts {
		artifacts[artifact.Path] = artifact.SHA256
	}
	return artifacts
}
