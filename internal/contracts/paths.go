package contracts

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

var (
	ErrPathNotAbsolute = errors.New("contracts: path must be an absolute path")
	ErrPathNotClean    = errors.New("contracts: path must be a clean absolute path without . or .. elements")
)

// EnsureCleanAbsolutePath rejects paths that are relative or contain lexical
// escapes such as "." / ".." segments. Contracts persist absolute paths and do
// not normalize them on read, so the serialized value must already be clean.
func EnsureCleanAbsolutePath(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%w: path=%q", ErrPathNotAbsolute, path)
	}
	if filepath.Clean(path) != path {
		return fmt.Errorf("%w: path=%q clean=%q", ErrPathNotClean, path, filepath.Clean(path))
	}
	for _, segment := range strings.Split(path, string(filepath.Separator)) {
		if segment == ".." {
			return fmt.Errorf("%w: path=%q", ErrPathNotClean, path)
		}
	}
	return nil
}
