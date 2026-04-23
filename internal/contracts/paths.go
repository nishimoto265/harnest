package contracts

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

var (
	ErrPathNotAbsolute       = errors.New("contracts: path must be an absolute path")
	ErrPathNotClean          = errors.New("contracts: path must be a clean absolute path without . or .. elements")
	ErrPathContainsNUL       = errors.New("contracts: path must not contain NUL bytes")
	ErrPathBasenameMismatch  = errors.New("contracts: path basename does not match the required filename")
	ErrPathRelativeEmpty     = errors.New("contracts: relative path must not be empty")
	ErrPathRelativeAbsolute  = errors.New("contracts: relative path must not be absolute")
	ErrPathRelativeNotClean  = errors.New("contracts: relative path must be clean and must not contain . or .. elements")
	ErrPathRelativeBadPrefix = errors.New("contracts: relative path must stay under the required prefix")
)

// EnsureCleanAbsolutePath rejects paths that are relative or contain lexical
// escapes such as "." / ".." segments. Contracts persist absolute paths and do
// not normalize them on read, so the serialized value must already be clean.
func EnsureCleanAbsolutePath(path string) error {
	if strings.ContainsRune(path, '\x00') {
		return fmt.Errorf("%w: path=%q", ErrPathContainsNUL, path)
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%w: path=%q", ErrPathNotAbsolute, path)
	}
	if filepath.Clean(path) != path {
		return fmt.Errorf("%w: path=%q clean=%q", ErrPathNotClean, path, filepath.Clean(path))
	}
	for _, segment := range strings.Split(path, string(filepath.Separator)) {
		if segment == "." || segment == ".." {
			return fmt.Errorf("%w: path=%q", ErrPathNotClean, path)
		}
	}
	return nil
}

// EnsureCleanRelativePath rejects absolute paths plus lexical escapes such as
// "." / ".." / empty segments. Contracts persist run-relative paths exactly as
// serialized, so the stored value must already be normalized.
func EnsureCleanRelativePath(path string) error {
	if strings.ContainsRune(path, '\x00') {
		return fmt.Errorf("%w: path=%q", ErrPathContainsNUL, path)
	}
	if path == "" {
		return ErrPathRelativeEmpty
	}
	if filepath.IsAbs(path) {
		return fmt.Errorf("%w: path=%q", ErrPathRelativeAbsolute, path)
	}
	if clean := filepath.Clean(path); clean != path || clean == "." {
		return fmt.Errorf("%w: path=%q clean=%q", ErrPathRelativeNotClean, path, clean)
	}
	for _, segment := range strings.Split(path, string(filepath.Separator)) {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("%w: path=%q", ErrPathRelativeNotClean, path)
		}
	}
	return nil
}

// EnsureRelativePathUnderPrefix enforces that path is a clean relative path and
// remains rooted under the given clean relative prefix.
func EnsureRelativePathUnderPrefix(path, prefix string) error {
	if err := EnsureCleanRelativePath(path); err != nil {
		return err
	}
	if err := EnsureCleanRelativePath(prefix); err != nil {
		return fmt.Errorf("contracts: invalid required prefix %q: %w", prefix, err)
	}
	if path == prefix {
		return fmt.Errorf("%w: path=%q required_prefix=%q", ErrPathRelativeBadPrefix, path, prefix)
	}
	required := prefix + string(filepath.Separator)
	if !strings.HasPrefix(path, required) {
		return fmt.Errorf("%w: path=%q required_prefix=%q", ErrPathRelativeBadPrefix, path, prefix)
	}
	return nil
}

// EnsureCleanAbsolutePathWithBasename enforces an absolute clean path whose
// basename is the expected filename.
func EnsureCleanAbsolutePathWithBasename(path, basename string) error {
	if err := EnsureCleanAbsolutePath(path); err != nil {
		return err
	}
	if filepath.Base(path) != basename {
		return fmt.Errorf("%w: path=%q basename=%q want=%q", ErrPathBasenameMismatch, path, filepath.Base(path), basename)
	}
	return nil
}

// CanonicalizePathForUniqueness resolves absolute paths to a comparison key for
// uniqueness checks. Existing paths are canonicalized through Abs+EvalSymlinks;
// not-yet-created planning paths resolve the longest existing ancestor through
// EvalSymlinks and then append the missing tail. On darwin the comparison key is
// lower-cased so default case-insensitive worktree volumes do not admit
// duplicates that differ only by spelling.
func CanonicalizePathForUniqueness(path string) (string, error) {
	if err := EnsureCleanAbsolutePath(path); err != nil {
		return "", err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	canonical, err := canonicalizePathWithMissingTail(abs)
	if err != nil {
		return "", err
	}
	return normalizeUniquenessPathKey(canonical), nil
}

func canonicalizePathWithMissingTail(abs string) (string, error) {
	canonical, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return filepath.Clean(canonical), nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}

	current := abs
	var tail []string
	for {
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		tail = append(tail, filepath.Base(current))
		current = parent

		canonical, err = filepath.EvalSymlinks(current)
		if err == nil {
			base := filepath.Clean(canonical)
			for i := len(tail) - 1; i >= 0; i-- {
				base = filepath.Join(base, tail[i])
			}
			return filepath.Clean(base), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
	}
}

func normalizeUniquenessPathKey(path string) string {
	clean := filepath.Clean(path)
	if runtime.GOOS == "darwin" {
		return strings.ToLower(clean)
	}
	return clean
}
