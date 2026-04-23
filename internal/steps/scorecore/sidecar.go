package scorecore

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
)

// WriteOverflowSidecar stores `content` under
// `<run>/<stepDir>/reasons/<sha256>.txt` and returns a run-relative OverflowRef
// pointing at it. stepDir must be "30" or "60". Safe to call multiple times —
// identical content produces the same sha256 filename so repeat writes are
// idempotent (WriteAtomic replaces in place).
func WriteOverflowSidecar(runCtx internalio.RunContext, stepDir string, content string) (contracts.OverflowRef, error) {
	if stepDir != "30" && stepDir != "60" {
		return contracts.OverflowRef{}, fmt.Errorf("%w: step_dir=%q", ErrPanelStepDir, stepDir)
	}
	sum := sha256.Sum256([]byte(content))
	digest := hex.EncodeToString(sum[:])
	rel := path.Join(stepDir, "reasons", digest+".txt")
	absPath, err := runCtx.ResolveRunRelative(rel)
	if err != nil {
		return contracts.OverflowRef{}, err
	}
	if err := internalio.WriteAtomic(absPath, []byte(content)); err != nil {
		return contracts.OverflowRef{}, err
	}
	ref := contracts.OverflowRef{Path: rel, Sha256: digest}
	if err := ref.Validate(); err != nil {
		return contracts.OverflowRef{}, err
	}
	return ref, nil
}
