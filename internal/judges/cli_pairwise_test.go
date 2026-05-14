package judges

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nishimoto265/harnest/internal/agents"
	"github.com/nishimoto265/harnest/internal/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrepareCLIPairwiseDecisionWorkspaceCodexCopiesWithoutMutatingInput(t *testing.T) {
	dir := t.TempDir()
	rubricPath := filepath.Join(dir, "rubric.md")
	pass1Path := filepath.Join(dir, "pass1.patch")
	pass2Path := filepath.Join(dir, "pass2.patch")
	require.NoError(t, os.WriteFile(rubricPath, []byte("# rubric\n"), 0o644))
	require.NoError(t, os.WriteFile(pass1Path, []byte("diff --git a/a b/a\n"), 0o644))
	require.NoError(t, os.WriteFile(pass2Path, []byte("diff --git a/b b/b\n"), 0o644))

	input := PairwiseDecisionInput{
		RunID:      "2026-04-23-PR1-abcdef0",
		Mode:       PairwiseModeBasic,
		TaskPrompt: "task",
		RubricPath: rubricPath,
		Pairs: []PairwisePair{{
			Agent: "a1",
			A:     PairwiseCandidate{Label: "pass1", OutputPath: pass1Path},
			B:     PairwiseCandidate{Label: "pass2", OutputPath: pass2Path},
		}},
		Comparisons: []PairwiseComparison{{
			Agent:  "a1",
			Order:  "AB",
			Winner: contracts.PairwiseWinnerB,
			Margin: contracts.PairwiseMarginClear,
		}},
	}

	workspace, err := prepareCLIPairwiseDecisionWorkspace(input, agents.ProviderCodex)
	require.NoError(t, err)
	defer workspace.cleanup()

	assert.Equal(t, rubricPath, input.RubricPath)
	assert.Equal(t, pass1Path, input.Pairs[0].A.OutputPath)
	assert.Equal(t, pass2Path, input.Pairs[0].B.OutputPath)
	assert.NotEqual(t, pass1Path, workspace.input.Pairs[0].A.OutputPath)
	assert.NotEqual(t, pass2Path, workspace.input.Pairs[0].B.OutputPath)
	assert.FileExists(t, workspace.input.RubricPath)
	assert.FileExists(t, workspace.input.Pairs[0].A.OutputPath)
	assert.FileExists(t, workspace.input.Pairs[0].B.OutputPath)
}
