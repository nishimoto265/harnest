package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nishimoto265/auto-improve/internal/archive"
	"github.com/nishimoto265/auto-improve/internal/config"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
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
	var inspect bool
	var runID string
	var clearDivergedSunset bool
	cmd := &cobra.Command{
		Use:           "recover",
		Short:         "Inspect or recover a stuck promotion run",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if inspect && runID != "" {
				return commandExitError{code: 2, msg: "recover: --inspect and --run are mutually exclusive"}
			}
			if clearDivergedSunset {
				if inspect || runID != "" {
					return commandExitError{code: 2, msg: "recover: --clear-diverged-sunset does not accept --inspect or --run"}
				}
				return runRecoverClearDivergedSunset(cmd)
			}
			if inspect {
				return runRecoverInspect(cmd)
			}
			return commandExitError{code: 2, msg: "recover: not implemented"}
		},
	}
	cmd.Flags().BoolVar(&inspect, "inspect", false, "Inspect recovery state without making changes")
	cmd.Flags().StringVar(&runID, "run", "", "Run ID to inspect or recover")
	cmd.Flags().BoolVar(&clearDivergedSunset, "clear-diverged-sunset", false, "Clear the durable sunset divergence block after verifying sunset is not mid-transaction")
	return cmd
}

func runRecoverInspect(cmd *cobra.Command) error {
	cfg, err := config.LoadDefault()
	if err != nil {
		return commandExitError{code: 2, msg: err.Error()}
	}
	runsBase, err := cfg.RunsBase()
	if err != nil {
		return commandExitError{code: 2, msg: err.Error()}
	}
	lockPath, err := cfg.PromotionLockPath()
	if err != nil {
		return commandExitError{code: 2, msg: err.Error()}
	}
	lock, _, err := internalio.InspectFileLock(lockPath)
	if err != nil {
		if errors.Is(err, internalio.ErrFileLockHeld) {
			return commandExitError{code: 2, msg: "recover: promotion.lock is held by another process"}
		}
		return err
	}
	if lock != nil {
		defer func() {
			_ = lock.Unlock()
		}()
	}
	if err := validateRegistryIntegrity(runsBase); err != nil {
		return err
	}
	return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{
		"event":     "recover_inspect",
		"runs_base": runsBase,
		"at":        time.Now().UTC().Format(time.RFC3339Nano),
	})
}

func runRecoverClearDivergedSunset(cmd *cobra.Command) error {
	runsBase, lock, err := recoverRunsBaseAndLock()
	if err != nil {
		return err
	}
	defer func() {
		_ = lock.Unlock()
	}()

	markerPath := filepath.Join(runsBase, "sunset-running.marker")
	if _, err := os.Stat(markerPath); err == nil {
		return commandExitError{code: 2, msg: "recover: sunset-running.marker still exists; refusing to clear sunset divergence during an in-progress transaction"}
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := validateRegistryIntegrity(runsBase); err != nil {
		return err
	}
	if err := archive.ClearDivergedMarker(runsBase); err != nil {
		return err
	}

	return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{
		"event":     "diverged_sunset_cleared",
		"runs_base": runsBase,
		"at":        time.Now().UTC().Format(time.RFC3339Nano),
	})
}

func recoverRunsBaseAndLock() (string, *internalio.FileLock, error) {
	cfg, err := config.LoadDefault()
	if err != nil {
		return "", nil, commandExitError{code: 2, msg: err.Error()}
	}
	runsBase, err := cfg.RunsBase()
	if err != nil {
		return "", nil, commandExitError{code: 2, msg: err.Error()}
	}
	lockPath, err := cfg.PromotionLockPath()
	if err != nil {
		return "", nil, commandExitError{code: 2, msg: err.Error()}
	}
	lock, acquired, err := internalio.TryAcquireFileLock(lockPath)
	if err != nil {
		return "", nil, err
	}
	if !acquired {
		return "", nil, commandExitError{code: 2, msg: "recover: promotion.lock is held by another process"}
	}
	return runsBase, lock, nil
}

func validateRegistryIntegrity(runsBase string) error {
	if _, err := internalio.RegistryLines(filepath.Join(runsBase, "rules-registry.jsonl")); err != nil {
		return commandExitError{code: 2, msg: fmt.Sprintf("recover: rules-registry.jsonl integrity check failed: %v", err)}
	}
	return nil
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
