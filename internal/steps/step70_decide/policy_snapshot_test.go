package step70_decide

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/nishimoto265/harnest/internal/policyrepo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPolicySnapshotMetadataForBranch_TrimsAndRejectsMismatch(t *testing.T) {
	runCtx, _, _, _, _ := newFixtureWithResolver(t, "PR114")
	policyDir := filepath.Join(runCtx.RunDir(), "policy")
	require.NoError(t, os.MkdirAll(policyDir, 0o755))
	require.NoError(t, internalio.WriteJSONAtomic(filepath.Join(policyDir, "snapshot.json"), policyrepo.SnapshotMetadata{
		SchemaVersion: "1",
		PolicyBranch:  " harnest/policy ",
		PolicyHead:    strings.Repeat("1", 40),
		RegistryHead:  "",
	}))

	meta, ok, err := policySnapshotMetadataForBranch(runCtx, "harnest/policy")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, strings.Repeat("1", 40), meta.PolicyHead)

	require.NoError(t, internalio.WriteJSONAtomic(filepath.Join(policyDir, "snapshot.json"), policyrepo.SnapshotMetadata{
		SchemaVersion: "1",
		PolicyBranch:  "harnest/other-policy",
		PolicyHead:    strings.Repeat("1", 40),
		RegistryHead:  "",
	}))

	_, _, err = policySnapshotMetadataForBranch(runCtx, "harnest/policy")
	require.ErrorContains(t, err, "policy snapshot branch mismatch")
}

func TestLocalPolicySnapshotPreAdoptBlockReason_RejectsBranchMetadata(t *testing.T) {
	runCtx, _, _, _, _ := newFixtureWithResolver(t, "PR115")
	policyDir := filepath.Join(runCtx.RunDir(), "policy")
	require.NoError(t, os.MkdirAll(policyDir, 0o755))
	require.NoError(t, internalio.WriteJSONAtomic(filepath.Join(policyDir, "snapshot.json"), policyrepo.SnapshotMetadata{
		SchemaVersion: "1",
		PolicyBranch:  "harnest/policy",
		PolicyHead:    strings.Repeat("1", 40),
		RegistryHead:  "",
	}))

	_, err := localPolicySnapshotPreAdoptBlockReason(runCtx)
	require.ErrorContains(t, err, "local policy snapshot has branch metadata")
}
