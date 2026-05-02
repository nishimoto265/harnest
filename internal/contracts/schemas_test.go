package contracts

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/validation"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TaskPackage: validator タグが正しく効くかの基本カバレッジ。
func TestTaskPackage_Valid(t *testing.T) {
	pkg := TaskPackage{
		SchemaVersion:           "1",
		RunID:                   "2026-04-20-PR42-abcdef0",
		PR:                      42,
		Title:                   "fix: example",
		BaseSHA:                 "1111111111111111111111111111111111111111",
		BestBranch:              "auto-improve/best",
		ReconstructedTaskPrompt: "hello",
		Worktrees:               make([]WorktreeAllocation, 6),
		CreatedAt:               time.Now(),
	}
	// Populate 6 worktrees minimally.
	for i := range pkg.Worktrees {
		pass := 1
		if i >= 3 {
			pass = 2
		}
		agent := AgentID([]string{"a1", "a2", "a3", "a1", "a2", "a3"}[i])
		pkg.Worktrees[i] = WorktreeAllocation{
			Agent:   agent,
			Pass:    pass,
			Path:    fmt.Sprintf("/tmp/wt/pass%d-%s", pass, agent),
			Branch:  fmt.Sprintf("b-pass%d-%s", pass, agent),
			BaseSHA: "1111111111111111111111111111111111111111",
			HeadSHA: "1111111111111111111111111111111111111111",
		}
	}
	assert.NoError(t, validation.Instance().Struct(pkg))
}

func TestTaskPackage_Reject_BadRunID(t *testing.T) {
	pkg := TaskPackage{
		SchemaVersion: "1",
		RunID:         "not-a-valid-run-id",
		PR:            1,
		Title:         "x",
		BaseSHA:       "1111111111111111111111111111111111111111",
		BestBranch:    "b",
		Worktrees:     []WorktreeAllocation{},
		CreatedAt:     time.Now(),
	}
	assert.Error(t, validation.Instance().Struct(pkg))
}

// finding #5: TaskPackage.Validate() が 3×2 matrix invariant を強制する。
func validTaskPackage() TaskPackage {
	pkg := TaskPackage{
		SchemaVersion:           "1",
		RunID:                   "2026-04-20-PR42-abcdef0",
		PR:                      42,
		Title:                   "fix: example",
		BaseSHA:                 "1111111111111111111111111111111111111111",
		BestBranch:              "auto-improve/best",
		ReconstructedTaskPrompt: "hello",
		Worktrees:               make([]WorktreeAllocation, 6),
		CreatedAt:               time.Now(),
	}
	agents := []AgentID{"a1", "a2", "a3", "a1", "a2", "a3"}
	for i := range pkg.Worktrees {
		pass := 1
		if i >= 3 {
			pass = 2
		}
		pkg.Worktrees[i] = WorktreeAllocation{
			Agent:   agents[i],
			Pass:    pass,
			Path:    fmt.Sprintf("/tmp/wt/pass%d-%s", pass, agents[i]),
			Branch:  fmt.Sprintf("b-pass%d-%s", pass, agents[i]),
			BaseSHA: "1111111111111111111111111111111111111111",
			HeadSHA: "1111111111111111111111111111111111111111",
		}
	}
	return pkg
}

func TestTaskPackage_Validate_Valid(t *testing.T) {
	assert.NoError(t, validTaskPackage().Validate())
}

func TestTaskPackage_Validate_RejectsRelativeWorktreePath(t *testing.T) {
	pkg := validTaskPackage()
	pkg.Worktrees[0].Path = "tmp/wt/pass1-a1"

	err := pkg.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrWorktreePathNotAbsolute)
}

func TestWorktreeAllocation_Validate_AcceptsAbsolutePath(t *testing.T) {
	w := validTaskPackage().Worktrees[0]

	assert.NoError(t, w.Validate())
}

func TestWorktreeAllocation_Validate_PathHardening(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		wantErr    error
		wantAnyErr bool
	}{
		{name: "clean absolute", path: "/a/b"},
		{name: "parent escape", path: "/a/../b", wantErr: ErrWorktreePathNotClean},
		{name: "dot segment", path: "/a/./b", wantErr: ErrWorktreePathNotClean},
		{name: "relative", path: "a/b", wantErr: ErrWorktreePathNotAbsolute},
		{name: "nul byte", path: "/a/\x00/b", wantErr: ErrWorktreePathNotClean},
		{name: "empty", path: "", wantAnyErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := validTaskPackage().Worktrees[0]
			w.Path = tt.path

			err := w.Validate()
			if tt.wantErr == nil && !tt.wantAnyErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
			}
		})
	}
}

func TestTaskPackage_Validate_RejectsCanonicalDuplicateSymlinkPath(t *testing.T) {
	tmp := t.TempDir()
	actual := filepath.Join(tmp, "actual")
	alias := filepath.Join(tmp, "alias")
	require.NoError(t, os.Mkdir(actual, 0o755))
	require.NoError(t, os.Symlink(actual, alias))

	pkg := validTaskPackage()
	pkg.Worktrees[0].Path = actual
	pkg.Worktrees[3].Path = alias

	err := pkg.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTaskPackageDuplicatePath)
}

func TestTaskPackage_Validate_RejectsCanonicalDuplicateSymlinkAncestorWithMissingLeaf(t *testing.T) {
	tmp := t.TempDir()
	realRoot := filepath.Join(tmp, "real")
	aliasRoot := filepath.Join(tmp, "alias")
	require.NoError(t, os.Mkdir(realRoot, 0o755))
	require.NoError(t, os.Symlink(realRoot, aliasRoot))

	pkg := validTaskPackage()
	pkg.Worktrees[0].Path = filepath.Join(realRoot, "new-leaf")
	pkg.Worktrees[3].Path = filepath.Join(aliasRoot, "new-leaf")

	err := pkg.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTaskPackageDuplicatePath)
}

func TestCanonicalizePathForUniqueness_DarwinCaseInsensitiveKey(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin-only uniqueness key behavior")
	}

	tmp := t.TempDir()
	upper, err := CanonicalizePathForUniqueness(filepath.Join(tmp, "Case", "Leaf"))
	require.NoError(t, err)
	lower, err := CanonicalizePathForUniqueness(filepath.Join(tmp, "case", "leaf"))
	require.NoError(t, err)
	assert.Equal(t, upper, lower)
}

func TestTaskPackage_Validate_Reject_PassCountMismatch(t *testing.T) {
	// pass==1 が 4 (distinct agents)、pass==2 が 2 → len=6 は満たすが matrix invariant 違反。
	pkg := validTaskPackage()
	// worktrees[3] is the pass2/a1 row. Move it to pass=1 with a new agent a4
	// (避: 重複判定が先に走らないよう distinct agent に置く).
	pkg.Worktrees[3].Pass = 1
	pkg.Worktrees[3].Agent = "a4"
	err := pkg.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTaskPackagePassCountMismatch)
}

func TestTaskPackage_Validate_Reject_AllPass1(t *testing.T) {
	pkg := validTaskPackage()
	for i := range pkg.Worktrees {
		pkg.Worktrees[i].Pass = 1
	}
	err := pkg.Validate()
	require.Error(t, err)
	// All-pass-1 causes tag validation (oneof=1 2) to pass but matrix enforces
	// per-pass count == 3 → pass=1 has 6, pass=2 has 0.
	// With current implementation: duplicate detection triggers first
	// (3 agents × 2 copies within pass 1).
	assert.Truef(t, errors.Is(err, ErrTaskPackageAgentDuplicate) || errors.Is(err, ErrTaskPackagePassCountMismatch), "err=%v", err)
}

func TestTaskPackage_Validate_Reject_DuplicateAgentWithinPass(t *testing.T) {
	pkg := validTaskPackage()
	// pass1 の worktrees[0..2] を全て a1 に → duplicate.
	pkg.Worktrees[0].Agent = "a1"
	pkg.Worktrees[1].Agent = "a1"
	pkg.Worktrees[2].Agent = "a1"
	err := pkg.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTaskPackageAgentDuplicate)
}

func TestTaskPackage_Validate_Reject_PassAgentSetDiffer(t *testing.T) {
	pkg := validTaskPackage()
	// pass2 の agent set を {a4,a5,a6} に置換 → pass1 = {a1,a2,a3} と不一致.
	pkg.Worktrees[3].Agent = "a4"
	pkg.Worktrees[4].Agent = "a5"
	pkg.Worktrees[5].Agent = "a6"
	err := pkg.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTaskPackagePassAgentMismatch)
}

func TestTaskPackage_Reject_WrongWorktreeCount(t *testing.T) {
	pkg := TaskPackage{
		SchemaVersion:           "1",
		RunID:                   "2026-04-20-PR42-abcdef0",
		PR:                      1,
		Title:                   "x",
		BaseSHA:                 "1111111111111111111111111111111111111111",
		BestBranch:              "b",
		ReconstructedTaskPrompt: "p",
		Worktrees:               []WorktreeAllocation{}, // len != 6
		CreatedAt:               time.Now(),
	}
	assert.Error(t, validation.Instance().Struct(pkg))
}
