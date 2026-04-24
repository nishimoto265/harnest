package main

import (
	"fmt"

	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/spf13/cobra"
)

func newDetectMergedCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "detect-merged",
		Short: "Detect newly merged pull requests",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadDefault()
			if err != nil {
				return commandExitError{code: 2, msg: err.Error()}
			}
			if err := checkCLIRecoveryGate(cfg); err != nil {
				return err
			}
			processedPath, err := cfg.ProcessedPath()
			if err != nil {
				return commandExitError{code: 2, msg: err.Error()}
			}
			prs, err := detectMergedPRs(cmd.Context(), cfg, processedPath)
			if err != nil {
				return err
			}
			for _, pr := range prs {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%d\t%s\t%s\n", pr.Number, pr.BaseRefName, pr.Title); err != nil {
					return err
				}
			}
			return nil
		},
	}
}
