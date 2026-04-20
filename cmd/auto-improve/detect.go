package main

import (
	"fmt"

	"github.com/nishimoto265/auto-improve/internal/config"
	"github.com/nishimoto265/auto-improve/internal/detect"
	"github.com/nishimoto265/auto-improve/internal/state"
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
			processedPath, err := cfg.ProcessedPath()
			if err != nil {
				return commandExitError{code: 2, msg: err.Error()}
			}
			lastProcessedPR, err := state.LastProcessedPRPath(processedPath)
			if err != nil {
				return err
			}
			prs, err := detect.New(processedPath).DetectMergedPRs(cmd.Context(), lastProcessedPR, cfg.Repo.GitHub)
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
