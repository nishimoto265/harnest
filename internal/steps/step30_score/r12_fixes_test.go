package step30_score

import (
	"os"
	"path/filepath"
	"testing"

	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/stretchr/testify/require"
)

// TestResolveRubricPath_RejectsSymlink covers L3. A pre-seeded symlink at the
// stub rubric path would previously have been followed by os.WriteFile,
// allowing arbitrary writes outside runs_base.
func TestResolveRubricPath_RejectsSymlink(t *testing.T) {
	root := t.TempDir()
	runsBase := filepath.Join(root, "runs")
	worktreeBase := filepath.Join(root, "worktrees")
	require.NoError(t, os.MkdirAll(runsBase, 0o755))
	require.NoError(t, os.MkdirAll(worktreeBase, 0o755))

	runCtx, err := internalio.NewRunContext("2026-04-21-PR42-abcdef0", runsBase, worktreeBase)
	require.NoError(t, err)

	// Pre-seed a symlink at the expected rubric path pointing at a
	// temporary target outside runs_base.
	attackerTarget := filepath.Join(root, "outside", "target.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(attackerTarget), 0o755))
	require.NoError(t, os.WriteFile(attackerTarget, []byte("original\n"), 0o600))

	rubricDir := filepath.Join(runsBase, ".rubrics")
	require.NoError(t, os.MkdirAll(rubricDir, 0o755))
	rubricPath := filepath.Join(rubricDir, "default.md")
	require.NoError(t, os.Symlink(attackerTarget, rubricPath))

	step := New()
	_, err = step.resolveRubricPath(runCtx)
	require.Error(t, err, "symlinked rubric path must be refused")
	require.Contains(t, err.Error(), "symlink")

	// And the attacker's file must remain unmodified (no write-through).
	data, rerr := os.ReadFile(attackerTarget)
	require.NoError(t, rerr)
	require.Equal(t, "original\n", string(data))
}
