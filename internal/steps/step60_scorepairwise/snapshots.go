package step60_scorepairwise

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
)

// snapshotAndHashPass2Diff pins the pass2 manifest diff bytes under the run
// directory and returns the hash of those pinned bytes. Judges receive the
// snapshot path so live manifest-diff mutations cannot split hash bytes from
// score bytes.
func snapshotAndHashPass2Diff(runIO internalio.RunContext, agent contracts.AgentID, diffAbs string) (string, string, error) {
	return snapshotAndHashStep60Diff(runIO, "pass2-"+string(agent), diffAbs)
}

func snapshotAndHashPass1Diff(runIO internalio.RunContext, agent contracts.AgentID, diffAbs string) (string, string, error) {
	return snapshotAndHashStep60Diff(runIO, "pass1-"+string(agent), diffAbs)
}

func snapshotAndHashStep60Diff(runIO internalio.RunContext, snapshotPrefix, diffAbs string) (string, string, error) {
	if err := contracts.EnsureCleanAbsolutePath(diffAbs); err != nil {
		return "", "", err
	}
	data, err := os.ReadFile(diffAbs)
	if err != nil {
		return "", "", err
	}
	hash := sha256Hex(data)

	snapshotDir, err := runIO.ResolveRunRelative(filepath.Join("60", "snapshots"))
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(snapshotDir, 0o755); err != nil {
		return "", "", err
	}
	fileName := fmt.Sprintf("%s-%s.patch", snapshotPrefix, hash)
	snapshotPath := filepath.Join(snapshotDir, fileName)
	if err := contracts.EnsureCleanAbsolutePath(snapshotPath); err != nil {
		return "", "", err
	}

	if existing, err := os.ReadFile(snapshotPath); err == nil && bytesEqual(existing, data) {
		return snapshotPath, hash, nil
	}

	tmp, err := os.CreateTemp(snapshotDir, snapshotPrefix+"-snap-*.tmp")
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
		if existing, verr := os.ReadFile(snapshotPath); verr == nil && bytesEqual(existing, data) {
			return snapshotPath, hash, nil
		}
		return "", "", err
	}

	if entries, rerr := os.ReadDir(snapshotDir); rerr == nil {
		prefix := snapshotPrefix + "-"
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
