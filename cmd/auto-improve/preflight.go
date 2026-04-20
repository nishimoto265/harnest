package main

import (
	"encoding/json"

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

			result := preflight.New().Check(cmd.Context(), cfg)
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
