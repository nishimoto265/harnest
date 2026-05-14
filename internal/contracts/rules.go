package contracts

import (
	"errors"
	"fmt"

	"github.com/nishimoto265/harnest/internal/validation"
)

var (
	ErrRuleIDInvalid   = errors.New("contracts: invalid rule_id")
	ErrRulePathInvalid = errors.New("contracts: invalid rule_path")
)

func ValidateRuleID(ruleID string) error {
	if err := validation.Instance().Var(ruleID, "required,rule_id_fmt"); err != nil {
		return fmt.Errorf("%w: %q", ErrRuleIDInvalid, ruleID)
	}
	return nil
}

func ValidateOptionalRuleID(ruleID string) error {
	if ruleID == "" {
		return nil
	}
	return ValidateRuleID(ruleID)
}

func ValidateRulePath(rulePath string) error {
	if err := EnsureCleanRelativePath(rulePath); err != nil {
		return fmt.Errorf("%w: %w", ErrRulePathInvalid, err)
	}
	if err := EnsureRelativePathUnderPrefix(rulePath, "rules"); err != nil {
		return fmt.Errorf("%w: %w", ErrRulePathInvalid, err)
	}
	if err := validation.Instance().Var(rulePath, "required,rule_path_fmt"); err != nil {
		return fmt.Errorf("%w: %q", ErrRulePathInvalid, rulePath)
	}
	return nil
}
