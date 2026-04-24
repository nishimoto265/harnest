package agentrunner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
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
	// DirtyFingerprint is a sha256 hex digest of tracked/staged diffs plus
	// untracked and ignored file content captured at rescue-artifact capture
	// time. It is used at rescue-dir adoption time to detect that the
	// worktree's dirty state has drifted from the stored snapshot (e.g. a
	// crash between state.json write and resume-state finalize left uncaptured
	// new edits on disk). Empty string preserves backward compatibility with
	// rescue dirs written before this field was introduced — callers MUST
	// treat an empty fingerprint as "unknown" unless they can prove the rescue
	// snapshot fully covers every file class that reset/clean will remove.
	DirtyFingerprint string `json:"dirty_fingerprint,omitempty"`
}

func WriteRescueState(path string, state RescueStateFile) error {
	return internalio.WriteJSONAtomic(path, state)
}

func ReadRescueState(path string) (RescueStateFile, error) {
	return internalio.ReadJSON[RescueStateFile](path)
}

// ComputeDirtyState returns a content-aware sha256 hex digest plus normalized
// porcelain-v1 status entries. The digest covers every worktree class that
// rescue reset/clean can discard: tracked changes, staged changes, untracked
// files, and ignored files.
func ComputeDirtyState(ctx context.Context, worktreePath string) (string, []string, error) {
	statusOut, err := gitOutput(ctx, worktreePath, "agentrunner", "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return "", nil, err
	}
	entries := normalizeNULList(statusOut)
	components := make([]string, 0, len(entries)+4)
	for _, entry := range entries {
		components = append(components, "status:"+entry)
	}
	for _, spec := range []struct {
		label string
		args  []string
	}{
		{label: "tracked.patch", args: []string{"diff", "HEAD", "--binary", "--no-ext-diff", "--no-textconv"}},
		{label: "staged.patch", args: []string{"diff", "--cached", "--binary", "--no-ext-diff", "--no-textconv"}},
	} {
		digest, err := gitOutputSHA256(ctx, worktreePath, spec.label, spec.args...)
		if err != nil {
			return "", nil, err
		}
		components = append(components, spec.label+":"+digest)
	}
	for _, spec := range []struct {
		label string
		args  []string
	}{
		{label: "untracked", args: []string{"ls-files", "--others", "--exclude-standard", "-z"}},
		{label: "ignored", args: []string{"ls-files", "--others", "-i", "--exclude-standard", "-z"}},
	} {
		fileComponents, err := dirtyFileComponents(ctx, worktreePath, spec.label, spec.args...)
		if err != nil {
			return "", nil, err
		}
		components = append(components, fileComponents...)
	}
	sort.Strings(components)
	hash := sha256.New()
	for _, component := range components {
		hash.Write([]byte(component))
		hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)), entries, nil
}

func normalizeNULList(out []byte) []string {
	entries := strings.Split(strings.TrimRight(string(out), "\x00"), "\x00")
	filtered := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry == "" {
			continue
		}
		filtered = append(filtered, entry)
	}
	sort.Strings(filtered)
	return filtered
}

func gitOutputSHA256(ctx context.Context, worktreePath, label string, args ...string) (string, error) {
	cmd, err := processenv.TrustedCommandContext(ctx, "git", append([]string{"-C", worktreePath}, args...)...)
	if err != nil {
		return "", fmt.Errorf("agentrunner: resolve git: %w", err)
	}
	cmd.Env = processenv.GitLocalEnv()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return "", err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, stdout)
	waitErr := cmd.Wait()
	if copyErr != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", copyErr
	}
	if waitErr != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("agentrunner: git %s for %s: %w: %s", strings.Join(args, " "), label, waitErr, strings.TrimSpace(stderr.String()))
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func dirtyFileComponents(ctx context.Context, worktreePath, label string, listArgs ...string) ([]string, error) {
	out, err := gitOutput(ctx, worktreePath, "agentrunner", listArgs...)
	if err != nil {
		return nil, err
	}
	entries := normalizeNULList(out)
	components := make([]string, 0, len(entries))
	for _, entry := range entries {
		component, err := dirtyFileComponent(ctx, worktreePath, label, entry)
		if err != nil {
			return nil, err
		}
		components = append(components, component)
	}
	return components, nil
}

func dirtyFileComponent(ctx context.Context, worktreePath, label, rel string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	cleaned, ok := cleanDirtyRelPath(rel)
	if !ok {
		return "", fmt.Errorf("agentrunner: dirty path escapes worktree: %s", rel)
	}
	path := filepath.Join(worktreePath, filepath.FromSlash(cleaned))
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s:%s:symlink:%s", label, cleaned, target), nil
	}
	if !info.Mode().IsRegular() {
		return fmt.Sprintf("%s:%s:non_regular:%s:%d:%d", label, cleaned, info.Mode().String(), info.Size(), info.ModTime().UnixNano()), nil
	}
	if info.Size() > RescueDiffLimitBytes {
		return fmt.Sprintf("%s:%s:too_large:%d:%d", label, cleaned, info.Size(), info.ModTime().UnixNano()), nil
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", err
	}
	return fmt.Sprintf("%s:%s:regular:%o:%d:%s", label, cleaned, info.Mode().Perm(), info.Size(), hex.EncodeToString(hash.Sum(nil))), nil
}

func cleanDirtyRelPath(rel string) (string, bool) {
	cleaned := filepath.Clean(rel)
	if cleaned == "." || cleaned == ".." || filepath.IsAbs(cleaned) || strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) {
		return "", false
	}
	return filepath.ToSlash(cleaned), true
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
