package contracts

import (
	"errors"
	"fmt"
)

var ErrOverflowRefPathPrefixMismatch = errors.New("contracts: overflow_ref path must stay under the required prefix")

func (r OverflowRef) Validate() error {
	if err := validateStruct(r); err != nil {
		return err
	}
	if err := EnsureCleanRelativePath(r.Path); err != nil {
		return fmt.Errorf("path: %w", err)
	}
	return nil
}

func validateOverflowRefUnderPrefix(field string, ref *OverflowRef, prefix string) error {
	if ref == nil {
		return nil
	}
	if err := ref.Validate(); err != nil {
		return fmt.Errorf("%s: %w", field, err)
	}
	if err := EnsureRelativePathUnderPrefix(ref.Path, prefix); err != nil {
		if errors.Is(err, ErrPathRelativeBadPrefix) {
			return fmt.Errorf("%w: field=%s path=%q required_prefix=%q", ErrOverflowRefPathPrefixMismatch, field, ref.Path, prefix)
		}
		return fmt.Errorf("%s: %w", field, err)
	}
	return nil
}
