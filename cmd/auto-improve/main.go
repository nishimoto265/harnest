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
	entrypointOptions := repoEntrypointOptions{}
	outputOptions := cliOutputOptions{}
	cmd := &cobra.Command{
		Use:           cliCommandName + " [repo-url]",
		Short:         productName + " improves an AI coding harness from merged PRs",
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			return runRepoEntrypoint(cmd, args[0], entrypointOptions, outputOptions)
		},
	}
	bindOutputFlags(cmd, &outputOptions)
	cmd.Flags().IntVar(&entrypointOptions.Limit, "limit", 0, "Process at most N selected PRs and exit")
	cmd.Flags().StringVar(&entrypointOptions.PRList, "pr", "", "Comma-separated PR numbers to process and exit")
	cmd.Flags().BoolVar(&entrypointOptions.DryRun, "dry-run", false, "Resolve repo and PR candidates without running the pipeline")

	cmd.AddCommand(
		newPreflightCmd(),
		newDetectMergedCmd(),
		newRunCmd(&outputOptions),
		newClearCmd(&outputOptions),
		newLessonsCmd(),
		newSunsetCmd(),
		newRecoverCmd(),
	)
	return cmd
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
