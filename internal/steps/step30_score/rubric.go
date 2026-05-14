package step30_score

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/nishimoto265/harnest/internal/judges"
)

func (s *Step) resolveRubricPath(runCtx internalio.RunContext) (string, error) {
	if s.rubricPathFn != nil {
		return s.rubricPathFn(runCtx)
	}
	path, err := judges.ResolveRunRubricPath(runCtx)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrRubricPathUnresolved, err)
	}
	return path, nil
}

func effectiveRubricVersion(baseVersion, rubricPath string) (string, error) {
	hash, err := fileSha256(rubricPath)
	if err != nil {
		return "", fmt.Errorf("step30_score: hash rubric: %w", err)
	}
	base := strings.TrimSpace(baseVersion)
	if base == "" {
		base = defaultRubricVersion
	}
	return fmt.Sprintf("%s+sha256:%s", base, hash[:12]), nil
}

func fileSha256(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// snapshotAndHashDiff materializes a read-only snapshot of the manifest diff
// under the run directory and hashes the bytes that were snapshotted. The
// judge receives the snapshot path as OutputPath so a concurrent
// rename/symlink swap between the hash call and the judge read cannot split
// hash-bytes from score-bytes (F16 TOCTOU).
//
// Snapshots are per-agent and content-addressed by sha256 so reruns that
// encounter identical diffs are no-ops. Old snapshots for the same agent
// are removed before the new one is written so the run directory does not
// accumulate stale copies across resume cycles.
func snapshotAndHashDiff(runCtx internalio.RunContext, agent contracts.AgentID, diffAbs string) (string, string, error) {
	if err := contracts.EnsureCleanAbsolutePath(diffAbs); err != nil {
		return "", "", err
	}
	// os.ReadFile follows symlinks — that is acceptable because we pin the
	// exact bytes we read into a snapshot and hash those bytes, not the
	// live path. A post-read swap of the original symlink does not affect
	// subsequent scoring.
	data, err := os.ReadFile(diffAbs)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])

	snapshotDir, err := runCtx.ResolveRunRelative(filepath.Join("30", "snapshots"))
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(snapshotDir, 0o755); err != nil {
		return "", "", err
	}
	// Content-address by sha256 so we never rewrite an already-pinned byte
	// sequence, and so concurrent resume attempts converge.
	fileName := fmt.Sprintf("%s-%s.patch", string(agent), hash)
	snapshotPath := filepath.Join(snapshotDir, fileName)
	if err := contracts.EnsureCleanAbsolutePath(snapshotPath); err != nil {
		return "", "", err
	}

	// Fast path: snapshot already exists with matching content.
	if existing, err := os.ReadFile(snapshotPath); err == nil && bytesEqual(existing, data) {
		return snapshotPath, hash, nil
	}

	// Atomic write into the snapshot path.
	tmp, err := os.CreateTemp(snapshotDir, string(agent)+"-snap-*.tmp")
	if err != nil {
		return "", "", err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return "", "", err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return "", "", err
	}
	if err := os.Rename(tmpName, snapshotPath); err != nil {
		_ = os.Remove(tmpName)
		// Concurrent snapshotter may have landed first.
		if existing, verr := os.ReadFile(snapshotPath); verr == nil && bytesEqual(existing, data) {
			return snapshotPath, hash, nil
		}
		return "", "", err
	}

	// Best-effort cleanup of stale snapshots for the same agent from prior
	// resume cycles (different hash). Failures are non-fatal — a stale file
	// cannot affect correctness because we always name by current hash.
	if entries, rerr := os.ReadDir(snapshotDir); rerr == nil {
		prefix := string(agent) + "-"
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasPrefix(name, prefix) || name == fileName {
				continue
			}
			_ = os.Remove(filepath.Join(snapshotDir, name))
		}
	}

	return snapshotPath, hash, nil
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
