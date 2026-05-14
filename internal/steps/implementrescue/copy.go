package implementrescue

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/steps/agentrunner"
)

const (
	MaxUntrackedBytes = 32 << 20
)

type CopyOpenFileFunc func(context.Context, *os.File, string, os.FileMode, int64) error

func CopyUntrackedFilesWithBudget(ctx context.Context, stepName, repoPath, rescueDir string, budget *agentrunner.RescueArtifactBudget, gitOutputBytes GitOutputBytesFunc, ensureDir func(string) error, copyOpenFile CopyOpenFileFunc, fileDigest func(string) (string, error)) ([]agentrunner.RescueArtifactDigest, error) {
	return copyOtherFilesWithBudget(ctx, stepName, repoPath, rescueDir, "untracked", "untracked-symlinks.txt", []string{"ls-files", "--others", "--exclude-standard", "-z"}, budget, gitOutputBytes, ensureDir, copyOpenFile, fileDigest)
}

func CopyIgnoredFilesWithBudget(ctx context.Context, stepName, repoPath, rescueDir string, budget *agentrunner.RescueArtifactBudget, gitOutputBytes GitOutputBytesFunc, ensureDir func(string) error, copyOpenFile CopyOpenFileFunc, fileDigest func(string) (string, error)) ([]agentrunner.RescueArtifactDigest, error) {
	output, err := gitOutputBytes(ctx, repoPath, "ls-files", "--others", "-i", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	entries := strings.Split(string(output), "\x00")
	skipLog := make([]string, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry == "" {
			continue
		}
		cleaned := filepath.Clean(entry)
		if filepath.IsAbs(cleaned) || cleaned == "." || strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) || cleaned == ".." {
			return nil, fmt.Errorf("%s: ignored file escapes repo dir: %s", stepName, entry)
		}
		skipLog = append(skipLog, "skipped_ignored_content:"+strconv.Quote(cleaned))
	}
	skipLogPath := filepath.Join(rescueDir, "ignored-skipped.txt")
	if err := internalio.WriteAtomic(skipLogPath, []byte(strings.Join(skipLog, "\n"))); err != nil {
		return nil, err
	}
	if err := recordArtifact(budget, skipLogPath, "ignored-skipped.txt"); err != nil {
		return nil, err
	}
	digest, err := fileDigest(skipLogPath)
	if err != nil {
		return nil, err
	}
	return []agentrunner.RescueArtifactDigest{{Path: "ignored-skipped.txt", SHA256: digest}}, nil
}

func copyOtherFilesWithBudget(ctx context.Context, stepName, repoPath, rescueDir, rescueSubdir, skipLogName string, listArgs []string, budget *agentrunner.RescueArtifactBudget, gitOutputBytes GitOutputBytesFunc, ensureDir func(string) error, copyOpenFile CopyOpenFileFunc, fileDigest func(string) (string, error)) ([]agentrunner.RescueArtifactDigest, error) {
	output, err := gitOutputBytes(ctx, repoPath, listArgs...)
	if err != nil {
		return nil, err
	}
	entries := strings.Split(string(output), "\x00")
	rescueBase := filepath.Join(rescueDir, rescueSubdir)
	skipLog := make([]string, 0)
	artifacts := make([]agentrunner.RescueArtifactDigest, 0, len(entries)+1)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry == "" {
			continue
		}
		cleaned := filepath.Clean(entry)
		src := filepath.Join(repoPath, cleaned)
		dst := filepath.Join(rescueBase, cleaned)
		if !strings.HasPrefix(dst, rescueBase+string(os.PathSeparator)) && dst != rescueBase {
			return nil, fmt.Errorf("%s: untracked file escapes rescue dir: %s", stepName, entry)
		}
		info, err := os.Lstat(src)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			skipLog = append(skipLog, "symlink:"+cleaned)
			continue
		}
		if sensitiveUntrackedPath(cleaned) {
			skipLog = append(skipLog, "skipped_sensitive_path:"+strconv.Quote(cleaned))
			continue
		}
		file, _, size, err := agentrunner.OpenValidatedRegularFile(src)
		if err != nil {
			if errors.Is(err, agentrunner.ErrArtifactNotRegular) {
				skipLog = append(skipLog, "skipped_non_regular:"+cleaned)
				continue
			}
			return nil, err
		}
		if size > MaxUntrackedBytes {
			_ = file.Close()
			skipLog = append(skipLog, fmt.Sprintf("skipped_too_large:%s:%d", cleaned, size))
			continue
		}
		artifactPath := filepath.ToSlash(filepath.Join(rescueSubdir, cleaned))
		if err := budget.RecordFile(artifactPath, size); err != nil {
			_ = file.Close()
			return nil, err
		}
		if err := ensureDir(filepath.Dir(dst)); err != nil {
			_ = file.Close()
			return nil, err
		}
		if err := copyOpenFile(ctx, file, dst, 0o600, MaxUntrackedBytes); err != nil {
			return nil, err
		}
		digest, err := fileDigest(dst)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, agentrunner.RescueArtifactDigest{
			Path:   artifactPath,
			SHA256: digest,
		})
	}
	symlinkPath := filepath.Join(rescueDir, skipLogName)
	if err := internalio.WriteAtomic(symlinkPath, []byte(strings.Join(skipLog, "\n"))); err != nil {
		return nil, err
	}
	if err := recordArtifact(budget, symlinkPath, skipLogName); err != nil {
		return nil, err
	}
	digest, err := fileDigest(symlinkPath)
	if err != nil {
		return nil, err
	}
	artifacts = append(artifacts, agentrunner.RescueArtifactDigest{Path: skipLogName, SHA256: digest})
	return artifacts, nil
}

func sensitiveUntrackedPath(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	switch base {
	case ".env",
		".npmrc",
		".pypirc",
		".netrc",
		"id_rsa",
		"id_dsa",
		"id_ecdsa",
		"id_ed25519",
		"credentials",
		"credentials.json":
		return true
	}
	return strings.HasPrefix(base, ".env.") ||
		strings.HasSuffix(base, ".pem") ||
		strings.HasSuffix(base, ".key") ||
		strings.HasSuffix(base, ".p12") ||
		strings.HasSuffix(base, ".pfx")
}

func WriteIgnoredList(ctx context.Context, repoPath, dest string, gitOutputBytes GitOutputBytesFunc) error {
	output, err := gitOutputBytes(ctx, repoPath, "ls-files", "--others", "-i", "--exclude-standard", "-z")
	if err != nil {
		return err
	}
	entries := strings.Split(strings.Trim(string(output), "\x00"), "\x00")
	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry == "" {
			continue
		}
		lines = append(lines, strconv.Quote(entry))
	}
	return internalio.WriteAtomic(dest, []byte(strings.Join(lines, "\n")))
}
