package judges

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDefaultRubricPath_EmbeddedMaterializesOutsideRepo verifies the rubric
// resolver survives an "installed binary" scenario where the current working
// directory and the repo source tree are unrelated — runtime.Caller would
// historically point at a path that does not exist at runtime.
func TestDefaultRubricPath_EmbeddedMaterializesOutsideRepo(t *testing.T) {
	dir := t.TempDir()
	SetDefaultRubricDirForTest(dir)
	t.Cleanup(func() { SetDefaultRubricDirForTest("") })

	path, err := DefaultRubricPath()
	require.NoError(t, err)

	assert.True(t, filepath.IsAbs(path), "rubric path must be absolute: %s", path)
	assert.True(t, strings.HasPrefix(path, dir), "rubric path must live under override dir: dir=%s path=%s", dir, path)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, defaultRubricContent, data, "materialized rubric must match embedded bytes")
}

// TestDefaultRubricPath_IsIdempotent ensures repeated calls return the same
// content-addressed path and do not rewrite the file when it already matches.
func TestDefaultRubricPath_IsIdempotent(t *testing.T) {
	dir := t.TempDir()
	SetDefaultRubricDirForTest(dir)
	t.Cleanup(func() { SetDefaultRubricDirForTest("") })

	first, err := DefaultRubricPath()
	require.NoError(t, err)
	info1, err := os.Stat(first)
	require.NoError(t, err)

	// Touch nothing, invoke again — should return the same path.
	second, err := DefaultRubricPath()
	require.NoError(t, err)
	assert.Equal(t, first, second)

	info2, err := os.Stat(second)
	require.NoError(t, err)
	// Atomic-rename avoidance keeps the on-disk file untouched when content matches.
	assert.Equal(t, info1.ModTime(), info2.ModTime())
}

// TestEmbeddedRubricMatchesRepoCopy ensures the embedded rubric in
// internal/judges/rubrics/default.md stays byte-identical to the canonical
// repository rubric at rubrics/default.md so edits to one do not silently
// bypass the other.
func TestEmbeddedRubricMatchesRepoCopy(t *testing.T) {
	// Walk up from cwd until we find a directory containing rubrics/default.md.
	// Skips cleanly when the test runs outside a repo checkout (installed binary).
	cwd, err := os.Getwd()
	require.NoError(t, err)
	dir := cwd
	for {
		candidate := filepath.Join(dir, "rubrics", "default.md")
		if data, err := os.ReadFile(candidate); err == nil {
			assert.Equal(t, data, defaultRubricContent, "internal/judges/rubrics/default.md drifted from rubrics/default.md")
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Skip("repo-root rubrics/default.md not reachable from cwd; installed-binary layout")
		}
		dir = parent
	}
}

// TestExpectedComplianceRuleIDs_MatchesDefault ensures the fallback rule-id
// set still resolves against the embedded rubric path.
func TestExpectedComplianceRuleIDs_MatchesDefault(t *testing.T) {
	dir := t.TempDir()
	SetDefaultRubricDirForTest(dir)
	t.Cleanup(func() { SetDefaultRubricDirForTest("") })

	path, err := DefaultRubricPath()
	require.NoError(t, err)

	ruleIDs, err := ExpectedComplianceRuleIDs(path)
	require.NoError(t, err)
	assert.Equal(t, []string{stubRuleID}, ruleIDs)
}
