package agentrunner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/processenv"
)

const (
	RescueBundleModeNone     = "none"
	RescueBundleModeRange    = "range"
	RescueBundleModeFullHead = "full_head"
)

type RescueArtifactDigest struct {
	Path   string `json:"path" validate:"required"`
	SHA256 string `json:"sha256" validate:"required,len=64,hexadecimal"`
}

type RescueStateFile struct {
	ExpectedBaseSHA string                 `json:"expected_base_sha" validate:"required,sha1_hex"`
	RescuedHeadSHA  string                 `json:"rescued_head_sha" validate:"required,sha1_hex"`
	RetryCount      int                    `json:"retry_count" validate:"gte=1"`
	CommitCount     int                    `json:"commit_count" validate:"gte=0"`
	BundleMode      string                 `json:"bundle_mode" validate:"required,oneof=none range full_head"`
	CreatedAt       time.Time              `json:"created_at" validate:"required"`
	Artifacts       []RescueArtifactDigest `json:"artifacts" validate:"required,dive"`
	// DirtyFingerprint is a sha256 hex digest of `git status --porcelain=v1 -z`
	// captured at rescue-artifact capture time. It is used at rescue-dir
	// adoption time to detect that the worktree's dirty state has drifted
	// from the stored snapshot (e.g. a crash between state.json write and
	// resume-state finalize left uncaptured new edits on disk). Empty string
	// preserves backward compatibility with rescue dirs written before this
	// field was introduced — callers MUST treat an empty fingerprint as
	// "unknown, do not adopt" when the current worktree is dirty.
	DirtyFingerprint string `json:"dirty_fingerprint,omitempty"`
}

func WriteRescueState(path string, state RescueStateFile) error {
	return internalio.WriteJSONAtomic(path, state)
}

func ReadRescueState(path string) (RescueStateFile, error) {
	return internalio.ReadJSON[RescueStateFile](path)
}

// ComputeDirtyFingerprint returns a sha256 hex digest of the worktree's
// porcelain-v1 dirty status. The digest is stable under re-ordering because
// entries are sorted before hashing, so it can be compared across adoption
// attempts to detect uncaptured worktree changes.
func ComputeDirtyFingerprint(ctx context.Context, worktreePath string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "status", "--porcelain=v1", "-z")
	cmd.Env = processenv.Sanitize()
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("agentrunner: git status --porcelain=v1 -z: %w", err)
	}
	entries := strings.Split(strings.TrimRight(string(out), "\x00"), "\x00")
	filtered := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry == "" {
			continue
		}
		filtered = append(filtered, entry)
	}
	sort.Strings(filtered)
	hash := sha256.New()
	for _, entry := range filtered {
		hash.Write([]byte(entry))
		hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func VerifyRescueState(rescueDir string, fileDigest func(string) (string, error), errPrefix string) error {
	state, err := ReadRescueState(filepath.Join(rescueDir, "state.json"))
	if err != nil {
		return err
	}
	for _, artifact := range state.Artifacts {
		path := filepath.Join(rescueDir, filepath.FromSlash(artifact.Path))
		digest, err := fileDigest(path)
		if err != nil {
			return err
		}
		if digest != artifact.SHA256 {
			return fmt.Errorf("%s: rescue artifact digest mismatch: path=%s", errPrefix, artifact.Path)
		}
	}
	return nil
}
