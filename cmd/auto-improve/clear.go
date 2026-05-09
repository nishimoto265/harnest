package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nishimoto265/auto-improve/internal/gitremote"
	"github.com/nishimoto265/auto-improve/internal/processenv"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

type clearOptions struct {
	All    bool
	DryRun bool
}

type clearPlan struct {
	Event      string             `json:"event"`
	Mode       string             `json:"mode"`
	Home       string             `json:"home"`
	Repo       string             `json:"repo,omitempty"`
	ArchiveDir string             `json:"archive_dir,omitempty"`
	Moved      []clearMovePlan    `json:"moved"`
	Removed    []repoRegistration `json:"removed_registrations,omitempty"`
	DryRun     bool               `json:"dry_run"`
}

type clearMovePlan struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Target string `json:"target"`
	Exists bool   `json:"exists"`
}

func newClearCmd(outputOpts *cliOutputOptions) *cobra.Command {
	opts := clearOptions{}
	cmd := &cobra.Command{
		Use:   "clear [repo-url]",
		Short: "Archive generated HarNest state so it can be rebuilt cleanly",
		Args: func(cmd *cobra.Command, args []string) error {
			if opts.All {
				if len(args) != 0 {
					return commandExitError{code: 2, msg: cliErrorPrefix() + " clear --all does not accept a repo-url"}
				}
				return nil
			}
			if len(args) != 1 {
				return commandExitError{code: 2, msg: cliErrorPrefix() + " clear requires a repo-url, or use --all"}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if outputOpts == nil {
				outputOpts = &cliOutputOptions{}
			}
			if err := validateOutputOptions(*outputOpts); err != nil {
				return err
			}
			plan, err := buildClearPlan(args, opts)
			if err != nil {
				return err
			}
			if !opts.DryRun {
				if err := executeClearPlan(plan); err != nil {
					return err
				}
			}
			return outputClearPlan(cmd, plan, *outputOpts)
		},
	}
	cmd.Flags().BoolVar(&opts.All, "all", false, "Archive all generated state under AUTO_IMPROVE_HOME")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "Show what would be archived without moving anything")
	return cmd
}

func buildClearPlan(args []string, opts clearOptions) (clearPlan, error) {
	home, err := autoImproveHome()
	if err != nil {
		return clearPlan{}, err
	}
	archiveDir := uniqueClearArchiveDir(home, time.Now().UTC())
	if opts.All {
		return clearPlan{
			Event:      "clear_plan",
			Mode:       "all",
			Home:       home,
			ArchiveDir: archiveDir,
			Moved: []clearMovePlan{
				clearMove("repos", filepath.Join(home, "repos"), filepath.Join(archiveDir, "repos")),
				clearMove("runs", filepath.Join(home, "runs"), filepath.Join(archiveDir, "runs")),
				clearMove("worktrees", filepath.Join(home, "worktrees"), filepath.Join(archiveDir, "worktrees")),
				clearMove("repositories", filepath.Join(home, "repositories.yaml"), filepath.Join(archiveDir, "repositories.yaml")),
			},
			DryRun: opts.DryRun,
		}, nil
	}

	info, err := gitremote.ParseGitHubRemote(args[0], gitremote.AllowedGitHubHostsFromEnv(processenv.SanitizeForNetworkExec()))
	if err != nil {
		return clearPlan{}, commandExitError{code: 2, msg: err.Error()}
	}
	namespace := repoNamespace(info.Slug)
	registrations, err := readRepositoryRegistrations(home)
	if err != nil {
		return clearPlan{}, err
	}
	var removed []repoRegistration
	for _, registration := range registrations {
		if registration.Slug == info.Slug {
			removed = append(removed, registration)
		}
	}
	return clearPlan{
		Event:      "clear_plan",
		Mode:       "repo",
		Home:       home,
		Repo:       info.Slug,
		ArchiveDir: filepath.Join(archiveDir, namespace),
		Moved: []clearMovePlan{
			clearMove("repo", filepath.Join(home, "repos", filepath.FromSlash(info.Slug)), filepath.Join(archiveDir, namespace, "repo")),
			clearMove("runs", filepath.Join(home, "runs", namespace), filepath.Join(archiveDir, namespace, "runs")),
			clearMove("worktrees", filepath.Join(home, "worktrees", namespace), filepath.Join(archiveDir, namespace, "worktrees")),
		},
		Removed: removed,
		DryRun:  opts.DryRun,
	}, nil
}

func uniqueClearArchiveDir(home string, now time.Time) string {
	stamp := now.Format("20060102-150405")
	base := filepath.Join(home, "archives", "cleared", stamp)
	if _, err := os.Stat(base); os.IsNotExist(err) {
		return base
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%02d", base, i)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
}

func clearMove(name, source, target string) clearMovePlan {
	_, err := os.Stat(source)
	return clearMovePlan{
		Name:   name,
		Source: source,
		Target: target,
		Exists: err == nil,
	}
}

func executeClearPlan(plan clearPlan) error {
	if err := os.MkdirAll(plan.ArchiveDir, 0o755); err != nil {
		return err
	}
	if err := writeClearArchiveMetadata(plan); err != nil {
		return err
	}
	for _, move := range plan.Moved {
		if !move.Exists {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(move.Target), 0o755); err != nil {
			return err
		}
		if err := os.Rename(move.Source, move.Target); err != nil {
			return fmt.Errorf("%s clear: archive %s: %w", cliErrorPrefix(), move.Name, err)
		}
	}
	if plan.Mode == "repo" {
		if err := removeRepositoryRegistration(plan.Home, plan.Repo); err != nil {
			return err
		}
	}
	return nil
}

func writeClearArchiveMetadata(plan clearPlan) error {
	if len(plan.Removed) > 0 {
		data, err := yaml.Marshal(plan.Removed)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(plan.ArchiveDir, "repositories.removed.yaml"), data, 0o644); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(plan.ArchiveDir, "clear-metadata.json"), append(data, '\n'), 0o644)
}

func removeRepositoryRegistration(home, slug string) error {
	registrations, err := readRepositoryRegistrations(home)
	if err != nil {
		return err
	}
	next := registrations[:0]
	for _, registration := range registrations {
		if registration.Slug == slug {
			continue
		}
		next = append(next, registration)
	}
	if len(next) == len(registrations) {
		return nil
	}
	return writeRepositoryRegistrations(home, next)
}

func outputClearPlan(cmd *cobra.Command, plan clearPlan, opts cliOutputOptions) error {
	if opts.Quiet {
		return nil
	}
	if opts.JSON {
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetIndent("", "  ")
		return encoder.Encode(plan)
	}
	lines := []string{
		fmt.Sprintf("%s clear: archived generated state", cliCommandName),
		fmt.Sprintf("mode: %s", plan.Mode),
		fmt.Sprintf("home: %s", plan.Home),
	}
	if plan.Repo != "" {
		lines = append(lines, fmt.Sprintf("repo: %s", plan.Repo))
	}
	lines = append(lines, fmt.Sprintf("archive: %s", plan.ArchiveDir))
	if plan.DryRun {
		lines[0] = fmt.Sprintf("%s clear: dry run", cliCommandName)
	}
	lines = append(lines, "items:")
	for _, move := range plan.Moved {
		status := "missing"
		if move.Exists {
			status = "archived"
			if plan.DryRun {
				status = "would archive"
			}
		}
		lines = append(lines, fmt.Sprintf("  - %s: %s", move.Name, status))
		if move.Exists {
			lines = append(lines, fmt.Sprintf("    %s -> %s", move.Source, move.Target))
		}
	}
	if len(plan.Removed) > 0 {
		lines = append(lines, fmt.Sprintf("registrations: %d archived and removed from active list", len(plan.Removed)))
	}
	_, err := fmt.Fprintln(cmd.OutOrStdout(), strings.Join(lines, "\n"))
	return err
}
