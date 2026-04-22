package step70_decide

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMaterializeRuleSidecar_RejectsSymlinkedBody covers H2. Before the fix
// materializeRuleSidecar called plain os.ReadFile, which happily followed a
// symlink, so a malicious candidate.proposed_body_path pointing at
// /etc/passwd would have had its contents staged verbatim under
// runs/<id>/staging/rules/.
func TestMaterializeRuleSidecar_RejectsSymlinkedBody(t *testing.T) {
	runCtx := newResolverRunContext(t)

	// Write a genuine-looking SHA so we don't short-circuit before the
	// symlink read. The symlink points at a file outside the run tree.
	targetContents := []byte("pretend this is /etc/passwd\n")
	targetDir := t.TempDir()
	realFile := filepath.Join(targetDir, "private.txt")
	require.NoError(t, os.WriteFile(realFile, targetContents, 0o600))

	// Put the symlink where candidate.ProposedBodyPath expects.
	bodyDir := filepath.Join(runCtx.RunDir(), "40", "candidates")
	require.NoError(t, os.MkdirAll(bodyDir, 0o755))
	symlinkPath := filepath.Join(bodyDir, "cand-001.md")
	require.NoError(t, os.Symlink(realFile, symlinkPath))

	sum := sha256.Sum256(targetContents)
	candidate := contracts.Candidate{
		CandidateID:        "cand-001",
		Kind:               contracts.CandidateKindNew,
		Title:              "demo",
		Problem:            "demo",
		Rationale:          "demo",
		ProposedBodyPath:   "40/candidates/cand-001.md",
		ProposedBodySha256: hex.EncodeToString(sum[:]),
	}
	err := materializeRuleSidecar(runCtx, candidate, "rules/demo.md")
	require.Error(t, err, "symlinked candidate body must be rejected")
	require.True(t, errors.Is(err, internalio.ErrNotRegularFile),
		"expected ErrNotRegularFile, got: %v", err)

	// And the staged path must never have been created.
	stagedPath, rerr := stagedRuleSidecarPath(runCtx, "rules/demo.md")
	require.NoError(t, rerr)
	_, statErr := os.Stat(stagedPath)
	assert.True(t, os.IsNotExist(statErr), "staged sidecar must not exist on reject; err=%v", statErr)
}

// TestPromoteRuleSidecar_RejectsSymlinkDestination covers M2. A pre-existing
// symlink at the destination path must be refused — previously os.Stat
// followed the link and IsRegular() returned true for the target, so the
// promoter would have compared the target's content hash to the staged file's
// hash (potentially letting an attacker silently accept a foreign file).
func TestPromoteRuleSidecar_RejectsSymlinkDestination(t *testing.T) {
	tmp := t.TempDir()
	// The staged file never needs to exist for this test because we expect
	// to fail before reading it.
	stagedPath := filepath.Join(tmp, "staged.md")
	// Write an unrelated plain file as the symlink target.
	targetPath := filepath.Join(tmp, "target.md")
	require.NoError(t, os.WriteFile(targetPath, []byte("body"), 0o600))

	dstPath := filepath.Join(tmp, "dst", "rule.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(dstPath), 0o755))
	require.NoError(t, os.Symlink(targetPath, dstPath))

	sum := sha256.Sum256([]byte("body"))
	err := promoteRuleSidecar(stagedPath, dstPath, hex.EncodeToString(sum[:]))
	require.Error(t, err, "symlink dst must be refused even when target content hashes match")
	require.True(t, errors.Is(err, errRulePublishDestinationType),
		"expected errRulePublishDestinationType, got: %v", err)
}

// TestPromoteRuleSidecar_AcceptsRegularDestinationMatchingSHA is the positive
// control: matching regular destination is still a valid no-op promotion.
func TestPromoteRuleSidecar_AcceptsRegularDestinationMatchingSHA(t *testing.T) {
	tmp := t.TempDir()
	stagedPath := filepath.Join(tmp, "staged.md")
	dstPath := filepath.Join(tmp, "dst.md")
	body := []byte("body")
	require.NoError(t, os.WriteFile(stagedPath, body, 0o600))
	require.NoError(t, os.WriteFile(dstPath, body, 0o600))

	sum := sha256.Sum256(body)
	err := promoteRuleSidecar(stagedPath, dstPath, hex.EncodeToString(sum[:]))
	require.NoError(t, err)
	// Staged should have been removed after a matching no-op.
	_, statErr := os.Stat(stagedPath)
	assert.True(t, os.IsNotExist(statErr))
}

func init() {
	// Prevent unused-import warnings when only some tests are compiled.
	_ = time.Second
}
