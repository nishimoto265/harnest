package main

import (
	"context"

	"github.com/nishimoto265/auto-improve/internal/orchestrator"
	"github.com/spf13/cobra"
)

var runSunsetTick = func(ctx context.Context, force bool) error {
	return orchestrator.RunSunsetTickWithOptions(ctx, orchestrator.SunsetTickOptions{Force: force})
}

func newSunsetCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "sunset",
		Short: "Run the rule sunset/archive flow",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSunsetTick(cmd.Context(), force)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Bypass the sunset gate and wait for promotion.lock")
	return cmd
}
