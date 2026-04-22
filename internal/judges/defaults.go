package judges

import (
	"os"
	"path/filepath"
	"runtime"

	"github.com/nishimoto265/auto-improve/internal/contracts"
)

// DefaultRubricPath returns the immutable repository rubric used by the Phase 0 stubs.
func DefaultRubricPath() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", ErrDefaultRubricUnresolved
	}
	path := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "rubrics", "default.md"))
	if !filepath.IsAbs(path) {
		absPath, err := filepath.Abs(path)
		if err != nil {
			return "", err
		}
		path = absPath
	}
	if err := contracts.EnsureCleanAbsolutePath(path); err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err != nil {
		return "", err
	}
	return path, nil
}

// ExpectedComplianceRuleIDs returns durable fallback rule IDs when the rubric is known.
func ExpectedComplianceRuleIDs(rubricPath string) ([]string, error) {
	if err := contracts.EnsureCleanAbsolutePath(rubricPath); err != nil {
		return nil, err
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
