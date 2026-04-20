// Package validation exposes a process-wide singleton of go-playground/validator.
//
// 全ての reader / UnmarshalJSON は validation.Instance().Struct(v) 経由でのみ
// struct validation を行う (ad hoc package globals を作らない)。カスタム
// validator / tag name func の登録は全て Instance() の sync.Once 内に集約する。
package validation

import (
	"regexp"
	"sync"

	"github.com/go-playground/validator/v10"
)

var (
	once     sync.Once
	instance *validator.Validate
)

// runIDPattern: "YYYY-MM-DD-PR<num>-<hex7>" (io-contracts.md §run ディレクトリ構造)
var runIDPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}-PR\d+-[0-9a-f]{7}$`)

// agentIDPattern: a1 / a2 / ... / a<positive int>  (no leading zero)
var agentIDPattern = regexp.MustCompile(`^a[1-9]\d*$`)

// sha256HexPattern: 64 char lowercase hex
var sha256HexPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// sha1HexPattern: 40 char lowercase hex (git commit SHA)
var sha1HexPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// Instance returns the lazily-initialised singleton validator.Validate.
// It is safe for concurrent use after the first call.
func Instance() *validator.Validate {
	once.Do(func() {
		instance = validator.New(validator.WithRequiredStructEnabled())

		// Custom validators (all registered here, nowhere else).
		mustRegister(instance, "run_id_fmt", func(fl validator.FieldLevel) bool {
			return runIDPattern.MatchString(fl.Field().String())
		})
		mustRegister(instance, "agent_id_fmt", func(fl validator.FieldLevel) bool {
			return agentIDPattern.MatchString(fl.Field().String())
		})
		mustRegister(instance, "sha256_hex", func(fl validator.FieldLevel) bool {
			return sha256HexPattern.MatchString(fl.Field().String())
		})
		mustRegister(instance, "sha1_hex", func(fl validator.FieldLevel) bool {
			return sha1HexPattern.MatchString(fl.Field().String())
		})
	})
	return instance
}

func mustRegister(v *validator.Validate, tag string, fn validator.Func) {
	if err := v.RegisterValidation(tag, fn); err != nil {
		panic("validation: failed to register tag " + tag + ": " + err.Error())
	}
}
