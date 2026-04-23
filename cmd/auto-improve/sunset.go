package main

import (
	"github.com/nishimoto265/auto-improve/internal/orchestrator"
	"github.com/spf13/cobra"
)

var runSunsetTick = orchestrator.RunSunsetTick

func newSunsetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sunset",
		Short: "Run the rule sunset/archive flow",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSunsetTick(cmd.Context())
		},
	}
}
