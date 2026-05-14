package judges

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/nishimoto265/harnest/internal/contracts"
)

// defaultRubricContent holds the authoritative Phase 0 rubric shipped with the
// binary. Using go:embed removes the runtime dependency on a source-relative
// path (previously resolved via runtime.Caller), so the binary keeps working
// when installed outside the repository.
//
//go:embed rubrics/default.md
var defaultRubricContent []byte

// ErrDefaultRubricEmpty indicates the embedded rubric was empty, which should
// be impossible at build time.
var ErrDefaultRubricEmpty = errors.New("judges: embedded default rubric is empty")

// defaultRubricDir overrides the base directory used to materialize the
// embedded rubric. Empty string means "use HARNEST_HOME or ~/.harnest with a
// TempDir fallback". Tests set this via SetDefaultRubricDirForTest.
var (
	defaultRubricDirMu    sync.Mutex
	defaultRubricDir      string
	defaultRubricOnce     sync.Once
	defaultRubricPathOnce string
	defaultRubricErrOnce  error
)

// DefaultRubricPath returns the absolute path to the embedded Phase 0 rubric
// after materializing it into a stable cache directory. The materialized file
// is content-addressed by sha256(embeddedRubric) so concurrent processes
// converge on the same path and so the file is refreshed whenever the binary
// ships a new rubric. The returned path is safe to pass as
// JudgeInput.RubricPath.
func DefaultRubricPath() (string, error) {
	defaultRubricOnce.Do(func() {
		defaultRubricPathOnce, defaultRubricErrOnce = materializeDefaultRubric()
	})
	if defaultRubricErrOnce != nil {
		// Reset once on error so retries after transient fs issues can succeed.
		defaultRubricOnce = sync.Once{}
		return "", defaultRubricErrOnce
	}
	return defaultRubricPathOnce, nil
}

func materializeDefaultRubric() (string, error) {
	if len(defaultRubricContent) == 0 {
		return "", ErrDefaultRubricEmpty
	}
	sum := sha256.Sum256(defaultRubricContent)
	digest := hex.EncodeToString(sum[:])

	baseDir, err := resolveRubricCacheDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return "", fmt.Errorf("judges: mkdir rubric cache: %w", err)
	}
	// Content-addressed filename makes materialization idempotent across
	// concurrent processes, even if the binary is upgraded between runs.
	path := filepath.Join(baseDir, "default-"+digest+".md")
	if err := writeFileIfNeeded(path, defaultRubricContent); err != nil {
		return "", err
	}
	if err := contracts.EnsureCleanAbsolutePath(path); err != nil {
		return "", err
	}
	return path, nil
}

func resolveRubricCacheDir() (string, error) {
	defaultRubricDirMu.Lock()
	override := defaultRubricDir
	defaultRubricDirMu.Unlock()
	if override != "" {
		return cleanAbsoluteDir(override)
	}
	if override := strings.TrimSpace(os.Getenv("HARNEST_RUBRIC_CACHE_DIR")); override != "" {
		return cleanAbsoluteDir(override)
	}
	if home := strings.TrimSpace(os.Getenv("HARNEST_HOME")); home != "" {
		return cleanAbsoluteDir(filepath.Join(home, "cache", "rubrics"))
	}
	if userHome, err := os.UserHomeDir(); err == nil && userHome != "" {
		return cleanAbsoluteDir(filepath.Join(userHome, ".harnest", "cache", "rubrics"))
	}
	if userCache, err := os.UserCacheDir(); err == nil && userCache != "" {
		candidate := filepath.Join(userCache, "harnest", "rubrics")
		return cleanAbsoluteDir(candidate)
	}
	// Fallback to TempDir when the user cache is unavailable (e.g. sandboxed
	// CI with $HOME unset). TempDir is always absolute.
	return filepath.Join(os.TempDir(), "harnest-rubrics"), nil
}

func cleanAbsoluteDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	clean := filepath.Clean(abs)
	if err := contracts.EnsureCleanAbsolutePath(clean); err != nil {
		return "", err
	}
	return clean, nil
}

// writeFileIfNeeded writes `data` to `path` atomically when the file does not
// already contain the exact same bytes. This keeps materialization idempotent
// and robust to concurrent processes.
func writeFileIfNeeded(path string, data []byte) error {
	existing, err := os.ReadFile(path)
	if err == nil && bytesEqual(existing, data) {
		return nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "default-rubric-*.tmp")
	if err != nil {
		return fmt.Errorf("judges: create temp rubric: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("judges: write temp rubric: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("judges: close temp rubric: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		// A concurrent materializer may have already renamed its copy in.
		// Re-check the final path for byte-identity before surfacing the
		// rename error so we stay idempotent across processes.
		if verify, verr := os.ReadFile(path); verr == nil && bytesEqual(verify, data) {
			return nil
		}
		return fmt.Errorf("judges: rename rubric: %w", err)
	}
	return nil
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// SetDefaultRubricDirForTest overrides the cache directory used by
// DefaultRubricPath. Must be an absolute clean path. Pass "" to clear.
// Resets the memoized once so the override takes effect.
func SetDefaultRubricDirForTest(dir string) {
	defaultRubricDirMu.Lock()
	defaultRubricDir = dir
	defaultRubricDirMu.Unlock()
	defaultRubricOnce = sync.Once{}
}

// ExpectedComplianceRuleIDs returns durable fallback rule IDs when the rubric is known.
func ExpectedComplianceRuleIDs(rubricPath string) ([]string, error) {
	if err := contracts.EnsureCleanAbsolutePath(rubricPath); err != nil {
		return nil, err
	}
	if ruleIDs, err := parseActiveRuleIDs(rubricPath); err != nil {
		return nil, err
	} else if len(ruleIDs) > 0 {
		return ruleIDs, nil
	}
	defaultPath, err := DefaultRubricPath()
	if err != nil {
		return nil, nil
	}
	if filepath.Clean(rubricPath) != defaultPath {
		return nil, nil
	}
	return []string{stubRuleID}, nil
}

// ActiveComplianceRuleIDs returns rule IDs explicitly declared by the current
// rubric without applying stub fallback behavior.
func ActiveComplianceRuleIDs(rubricPath string) ([]string, error) {
	if err := contracts.EnsureCleanAbsolutePath(rubricPath); err != nil {
		return nil, err
	}
	return parseActiveRuleIDs(rubricPath)
}

func parseActiveRuleIDs(rubricPath string) ([]string, error) {
	data, err := os.ReadFile(rubricPath)
	if err != nil {
		return nil, fmt.Errorf("judges: read rubric for active rule ids: %w", err)
	}
	const heading = "## Active Rule IDs"
	lines := strings.Split(string(data), "\n")
	collecting := false
	seen := make(map[string]struct{})
	ruleIDs := make([]string, 0)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == heading:
			collecting = true
			continue
		case collecting && strings.HasPrefix(trimmed, "## "):
			collecting = false
		}
		if !collecting || !strings.HasPrefix(trimmed, "- ") {
			continue
		}
		ruleID := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
		if ruleID == "" {
			continue
		}
		if err := contracts.ValidateRuleID(ruleID); err != nil {
			return nil, fmt.Errorf("judges: parse active rule id %q: %w", ruleID, err)
		}
		if _, ok := seen[ruleID]; ok {
			continue
		}
		seen[ruleID] = struct{}{}
		ruleIDs = append(ruleIDs, ruleID)
	}
	sort.Strings(ruleIDs)
	return ruleIDs, nil
}
