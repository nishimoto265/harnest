package contracts

import (
	"errors"
	"fmt"
	"time"
)

// WorktreeAllocation describes one of the 6 worktrees (pass1 × 3 + pass2 × 3)
// that step10 carves out of the base repository. `task-package.json.worktrees[]`
// is the **canonical metadata** source; step 20/50/70 must read from there and
// not re-derive from on-disk paths (io-contracts.md §run ディレクトリ構造).
type WorktreeAllocation struct {
	// Agent: a1 / a2 / a3 (positive integer, per pass 3 agents).
	Agent AgentID `json:"agent" validate:"required,agent_id_fmt"`
	// Pass: 1 (step20) または 2 (step50).
	Pass int `json:"pass" validate:"required,oneof=1 2"`
	// Path: absolute filesystem path to the worktree dir
	// (`<worktree_base>/<runId>-pass{1,2}-a{1..N}/`).
	Path string `json:"path" validate:"required"`
	// Branch: git branch name checked out at the worktree.
	Branch string `json:"branch" validate:"required"`
	// BaseSHA: commit at which the worktree was carved
	// (step10 で記録、以降 immutable、rescue の expected_base_sha source).
	BaseSHA string `json:"base_sha" validate:"required,sha1_hex"`
	// HeadSHA: HEAD at allocation time (= BaseSHA for fresh worktrees).
	HeadSHA string `json:"head_sha" validate:"required,sha1_hex"`
}

// TaskPackage is the step10 output artifact, written to `<run>/task-package.json`.
//
// 完了マーカー: task-package.json 存在 (io-contracts.md §completion marker).
// worktrees[] must contain exactly 6 entries (2 passes × 3 agents).
type TaskPackage struct {
	// SchemaVersion: forward-compat knob. "1" for Phase 0 (closed).
	SchemaVersion string `json:"schema_version" validate:"required,oneof=1"`

	RunID RunID  `json:"run_id" validate:"required,run_id_fmt"`
	PR    int    `json:"pr" validate:"required,gt=0"`
	Title string `json:"title" validate:"required"`

	// BaseSHA is the PR merge-base (= `<run>/base.sha` ファイル内容).
	BaseSHA string `json:"base_sha" validate:"required,sha1_hex"`

	// BestBranch is the name of the running best rule-set branch that step10
	// applied before carving worktrees.
	BestBranch string `json:"best_branch" validate:"required"`

	// ReconstructedTaskPrompt: step10 で合成した PR task description.
	// 下流 (step20/50) で prompt に埋める前に必ず
	// `internal/io.SanitizeForPromptEmbedding()` を通す (io-contracts.md §5).
	ReconstructedTaskPrompt string `json:"reconstructed_task_prompt" validate:"required"`

	// Worktrees: 2 pass × 3 agent = 6 件の allocation metadata (正本).
	Worktrees []WorktreeAllocation `json:"worktrees" validate:"required,len=6,dive"`

	// CreatedAt: step10 が task-package を書いた時刻.
	CreatedAt time.Time `json:"created_at" validate:"required"`
}

// TaskPackage 3×2 agent matrix invariants (Phase 0-bootstrap-1 gate 2nd-round
// finding #5): step10 が書き出す task-package.json の Worktrees[] は必ず
//   - pass==1 の entry が 3 件、pass==2 の entry が 3 件
//   - 各 pass 内で AgentID が一意
//   - 2 pass の AgentID 集合が完全一致 (例: pass1={a1,a2,a3} ⇒ pass2 も同じ)
//
// を満たす。step20/50 の `validateAgentsAgainstPass` はこの invariant を前提に
// subset 一致ではなく set equality を担保する。
var (
	ErrTaskPackagePassCountMismatch = errors.New("contracts: task_package: worktrees must contain exactly 3 entries per pass")
	ErrTaskPackageAgentDuplicate    = errors.New("contracts: task_package: duplicate agent within a pass")
	ErrTaskPackagePassAgentMismatch = errors.New("contracts: task_package: agent set differs between pass 1 and pass 2")

	// ErrTaskPackageDuplicatePath / ErrTaskPackageDuplicateBranch (Phase 0-bootstrap-1
	// gate 3rd-round finding #4): worktree path / branch must be globally unique
	// across the 6-row matrix. step10 carves one worktree per (pass, agent) to
	// its own <worktree_base>/<run>-pass{1,2}-a{N}/ and checks out a distinct
	// per-row branch; if two rows share a path, both agents would clobber each
	// other's working tree on disk, and shared branches would cross-wire pass1
	// and pass2 commits. Catch this at contract-validation time instead of
	// waiting for runtime IO corruption.
	ErrTaskPackageDuplicatePath   = errors.New("contracts: task_package: duplicate worktree path across allocations")
	ErrTaskPackageDuplicateBranch = errors.New("contracts: task_package: duplicate worktree branch across allocations")
	ErrWorktreePathNotAbsolute    = errors.New("contracts: task_package: worktree path must be an absolute path")
	ErrWorktreePathNotClean       = errors.New("contracts: task_package: worktree path must be a clean absolute path without . or .. elements")
)

// Validate enforces tag-based validation + the 3×2 matrix invariants described
// above. The tag-based `len=6` on Worktrees is a necessary but insufficient
// condition; this method completes the check. decodeStrict auto-chains this
// method whenever TaskPackage flows through a strict decode path.
func (p TaskPackage) Validate() error {
	if err := validateStruct(p); err != nil {
		return err
	}
	for i, w := range p.Worktrees {
		if err := w.Validate(); err != nil {
			return fmt.Errorf("worktrees[%d]: %w", i, err)
		}
	}
	if err := p.validateWorktreeMatrix(); err != nil {
		return err
	}
	return p.validateWorktreePathBranchUniqueness()
}

func (w WorktreeAllocation) Validate() error {
	if err := validateStruct(w); err != nil {
		return err
	}
	if err := EnsureCleanAbsolutePath(w.Path); err != nil {
		switch {
		case errors.Is(err, ErrPathNotAbsolute):
			return fmt.Errorf("%w: path=%q", ErrWorktreePathNotAbsolute, w.Path)
		case errors.Is(err, ErrPathNotClean), errors.Is(err, ErrPathContainsNUL):
			return fmt.Errorf("%w: path=%q", ErrWorktreePathNotClean, w.Path)
		default:
			return err
		}
	}
	return nil
}

// validateWorktreePathBranchUniqueness enforces that every WorktreeAllocation
// in Worktrees[] has a globally unique (path, branch) pair across the matrix.
// Two rows sharing either would corrupt the filesystem or git state at
// step20/50 run-time (finding #4).
func (p TaskPackage) validateWorktreePathBranchUniqueness() error {
	paths := make(map[string]int, len(p.Worktrees))
	branches := make(map[string]int, len(p.Worktrees))
	for i, w := range p.Worktrees {
		canonicalPath, err := CanonicalizePathForUniqueness(w.Path)
		if err != nil {
			return fmt.Errorf("worktrees[%d].path: %w", i, err)
		}
		if prev, dup := paths[canonicalPath]; dup {
			return fmt.Errorf("%w: canonical_path=%q paths=%q,%q indices=%d,%d", ErrTaskPackageDuplicatePath, canonicalPath, p.Worktrees[prev].Path, w.Path, prev, i)
		}
		paths[canonicalPath] = i
		if prev, dup := branches[w.Branch]; dup {
			return fmt.Errorf("%w: branch=%q indices=%d,%d", ErrTaskPackageDuplicateBranch, w.Branch, prev, i)
		}
		branches[w.Branch] = i
	}
	return nil
}

func (p TaskPackage) validateWorktreeMatrix() error {
	pass1 := map[AgentID]struct{}{}
	pass2 := map[AgentID]struct{}{}
	for _, w := range p.Worktrees {
		switch w.Pass {
		case 1:
			if _, dup := pass1[w.Agent]; dup {
				return fmt.Errorf("%w: pass=1 agent=%s", ErrTaskPackageAgentDuplicate, w.Agent)
			}
			pass1[w.Agent] = struct{}{}
		case 2:
			if _, dup := pass2[w.Agent]; dup {
				return fmt.Errorf("%w: pass=2 agent=%s", ErrTaskPackageAgentDuplicate, w.Agent)
			}
			pass2[w.Agent] = struct{}{}
		default:
			// Tag-level `oneof=1 2` should already have caught this; defensive.
			return fmt.Errorf("%w: unknown pass=%d", ErrTaskPackagePassCountMismatch, w.Pass)
		}
	}
	if len(pass1) != 3 || len(pass2) != 3 {
		return fmt.Errorf("%w: pass1=%d pass2=%d", ErrTaskPackagePassCountMismatch, len(pass1), len(pass2))
	}
	if len(pass1) != len(pass2) {
		return ErrTaskPackagePassAgentMismatch
	}
	for a := range pass1 {
		if _, ok := pass2[a]; !ok {
			return fmt.Errorf("%w: agent %s missing from pass 2", ErrTaskPackagePassAgentMismatch, a)
		}
	}
	return nil
}
