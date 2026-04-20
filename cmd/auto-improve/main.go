package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func main() {
	root := newRootCmd()
	if err := root.Execute(); err != nil {
		var exitErr interface{ ExitCode() int }
		if errors.As(err, &exitErr) {
			_, _ = fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(exitErr.ExitCode())
		}
		_, _ = fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "auto-improve",
		Short:         "Self-improving harness pipeline for AI coding agents",
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	cmd.AddCommand(
		newPreflightCmd(),
		newDetectMergedCmd(),
		newRunCmd(),
		newSunsetCmd(),
		newRecoverCmd(),
	)
	return cmd
}

func newRunCmd() *cobra.Command {
	cmd := notImplementedCommand("run", "Run the pipeline for one PR or the detect loop")
	cmd.Use = "run"
	cmd.Flags().Int("pr", 0, "PR number to process")
	cmd.Flags().Bool("detect-loop", false, "Run the detect loop instead of a single PR")
	cmd.Flags().Bool("with-preflight", false, "Run preflight checks before starting")
	return cmd
}

func newSunsetCmd() *cobra.Command {
	return notImplementedCommand("sunset", "Run the rule sunset/archive flow")
}

func newRecoverCmd() *cobra.Command {
	cmd := notImplementedCommand("recover", "Inspect or recover a stuck promotion run")
	cmd.Use = "recover"
	cmd.Flags().Bool("inspect", false, "Inspect recovery state without making changes")
	cmd.Flags().String("run", "", "Run ID to inspect or recover")
	return cmd
}

func notImplementedCommand(use, short string) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			return commandExitError{
				code: 2,
				msg:  fmt.Sprintf("%s: not implemented", cmd.Name()),
			}
		},
	}
}

type commandExitError struct {
	code int
	msg  string
}

func (e commandExitError) Error() string {
	return e.msg
}

func (e commandExitError) ExitCode() int {
	return e.code
}
