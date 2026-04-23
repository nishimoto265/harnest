package main

import (
	"context"
	"encoding/json"
	"time"

	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/nishimoto265/auto-improve/internal/preflight"
	"github.com/spf13/cobra"
)

func newPreflightCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "preflight",
		Short: "Run local toolchain and filesystem sanity checks",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadDefault()
			if err != nil {
				return commandExitError{code: 2, msg: err.Error()}
			}

			checkCtx, cancel := withPreflightTimeout(cmd.Context(), cfg)
			defer cancel()
			result := preflight.New().Check(checkCtx, cfg)
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			if err := enc.Encode(result); err != nil {
				return err
			}
			if !result.OK {
				return commandExitError{code: 10, msg: "preflight failed"}
			}
			return nil
		},
	}
}

func withPreflightTimeout(ctx context.Context, cfg config.Config) (context.Context, context.CancelFunc) {
	if cfg.PreflightTimeoutSec <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, time.Duration(cfg.PreflightTimeoutSec)*time.Second)
}
