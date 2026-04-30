package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nishimoto265/auto-improve/internal/lessons"
	"github.com/spf13/cobra"
)

func newLessonsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "lessons",
		Short:         "Create lessons and generate lesson checklists",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.AddCommand(
		newLessonsNewCmd(),
		newLessonsGenerateChecklistCmd(),
		newLessonsPrepareChecklistResultCmd(),
		newLessonsVerifyChecklistResultCmd(),
		newLessonsInstallGuidanceCmd(),
	)
	return cmd
}

func newLessonsNewCmd() *cobra.Command {
	var root string
	var checklistItem string
	var severity string
	var confidence string
	var category string
	cmd := &cobra.Command{
		Use:   "new <id>",
		Short: "Create a lesson skeleton under .auto-improve/lessons",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 1 {
				return commandExitError{code: 2, msg: "lessons new: requires exactly one <id>"}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := lessons.CreateLesson(lessons.NewLessonRequest{
				Root:          root,
				ID:            args[0],
				ChecklistItem: checklistItem,
				Severity:      lessons.Severity(severity),
				Confidence:    lessons.Confidence(confidence),
				Category:      category,
			})
			if err != nil {
				return commandExitError{code: 2, msg: fmt.Sprintf("lessons new: %v", err)}
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{
				"event": "lesson_created",
				"path":  path,
			})
		},
	}
	cmd.Flags().StringVar(&root, "root", ".", "Repository root containing .auto-improve")
	cmd.Flags().StringVar(&checklistItem, "checklist-item", "", "Short checklist text generated from this lesson")
	cmd.Flags().StringVar(&severity, "severity", string(lessons.SeverityMedium), "critical, high, medium, or low")
	cmd.Flags().StringVar(&confidence, "confidence", string(lessons.ConfidenceMedium), "high, medium, or low")
	cmd.Flags().StringVar(&category, "category", "general", "Lesson category")
	return cmd
}

func newLessonsGenerateChecklistCmd() *cobra.Command {
	var root string
	var check bool
	cmd := &cobra.Command{
		Use:   "generate-checklist",
		Short: "Generate .auto-improve/checklist.md from active lessons",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			content, err := lessons.GenerateChecklist(root)
			if err != nil {
				return commandExitError{code: 2, msg: fmt.Sprintf("lessons generate-checklist: %v", err)}
			}
			path := lessons.ChecklistPath(mustAbsRoot(root))
			if check {
				existing, err := os.ReadFile(path)
				if err != nil {
					return commandExitError{code: 1, msg: fmt.Sprintf("lessons generate-checklist: read existing checklist: %v", err)}
				}
				if string(existing) != content {
					return commandExitError{code: 1, msg: "lessons generate-checklist: checklist is stale"}
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{
					"event": "checklist_up_to_date",
					"path":  path,
				})
			}
			path, err = lessons.WriteChecklist(root)
			if err != nil {
				return commandExitError{code: 2, msg: fmt.Sprintf("lessons generate-checklist: %v", err)}
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{
				"event": "checklist_generated",
				"path":  path,
			})
		},
	}
	cmd.Flags().StringVar(&root, "root", ".", "Repository root containing .auto-improve")
	cmd.Flags().BoolVar(&check, "check", false, "Fail if .auto-improve/checklist.md is not up to date")
	return cmd
}

func newLessonsPrepareChecklistResultCmd() *cobra.Command {
	var root string
	var force bool
	cmd := &cobra.Command{
		Use:   "prepare-checklist-result",
		Short: "Copy .auto-improve/checklist.md to .auto-improve/work/checklist-result.md",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := lessons.PrepareChecklistResult(root, force)
			if err != nil {
				return commandExitError{code: 2, msg: fmt.Sprintf("lessons prepare-checklist-result: %v", err)}
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{
				"event": "checklist_result_prepared",
				"path":  path,
			})
		},
	}
	cmd.Flags().StringVar(&root, "root", ".", "Repository root containing .auto-improve")
	cmd.Flags().BoolVar(&force, "force", false, "Overwrite an existing checklist result")
	return cmd
}

func newLessonsVerifyChecklistResultCmd() *cobra.Command {
	var root string
	cmd := &cobra.Command{
		Use:   "verify-checklist-result",
		Short: "Verify .auto-improve/work/checklist-result.md is fully resolved",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			summary, err := lessons.VerifyChecklistResult(root)
			if err != nil {
				return commandExitError{code: 1, msg: fmt.Sprintf("lessons verify-checklist-result: %v", err)}
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
				Event   string                         `json:"event"`
				Path    string                         `json:"path"`
				Summary lessons.ChecklistResultSummary `json:"summary"`
			}{
				Event:   "checklist_result_verified",
				Path:    lessons.ChecklistResultPath(mustAbsRoot(root)),
				Summary: summary,
			})
		},
	}
	cmd.Flags().StringVar(&root, "root", ".", "Repository root containing .auto-improve")
	return cmd
}

func newLessonsInstallGuidanceCmd() *cobra.Command {
	var root string
	var providers []string
	cmd := &cobra.Command{
		Use:   "install-guidance",
		Short: "Install optional provider guidance and checklist verification hooks",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := lessons.InstallGuidance(lessons.InstallGuidanceOptions{
				Root:      root,
				Providers: providers,
			})
			if err != nil {
				return commandExitError{code: 2, msg: fmt.Sprintf("lessons install-guidance: %v", err)}
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
				Event string   `json:"event"`
				Files []string `json:"files"`
			}{
				Event: "guidance_installed",
				Files: result.Files,
			})
		},
	}
	cmd.Flags().StringVar(&root, "root", ".", "Repository root containing .auto-improve")
	cmd.Flags().StringSliceVar(&providers, "provider", nil, "Provider(s) to install: claude,codex")
	return cmd
}

func mustAbsRoot(root string) string {
	if out, err := filepath.Abs(root); err == nil {
		return out
	}
	return root
}
