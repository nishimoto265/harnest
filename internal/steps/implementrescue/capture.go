package implementrescue

import (
	"context"
	"path/filepath"

	"github.com/nishimoto265/auto-improve/internal/steps/agentrunner"
)

func CaptureArtifacts(ctx context.Context, opts PerformOptions, rescueDir, headSHA, dirtyFingerprint string, nextRetry int) error {
	budget := agentrunner.NewRescueArtifactBudget()
	artifacts := make([]agentrunner.RescueArtifactDigest, 0, 8)

	commitCount, bundleMode, err := opts.WriteBundle(ctx, opts.Allocation.Path, rescueDir, opts.State.ExpectedBaseSHA)
	if err != nil {
		return err
	}
	if digest, err := opts.FileDigest(filepath.Join(rescueDir, "commits.bundle")); err == nil {
		artifacts = append(artifacts, agentrunner.RescueArtifactDigest{Path: "commits.bundle", SHA256: digest})
	} else {
		return err
	}
	if err := MapCaptureError(opts.StepName, recordArtifact(&budget, filepath.Join(rescueDir, "commits.bundle"), "commits.bundle")); err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		return err
	}
	if err := MapCaptureError(opts.StepName, opts.WriteGitOutput(ctx, opts.Allocation.Path, filepath.Join(rescueDir, "tracked.patch"), "diff", "HEAD", "--binary", "--no-ext-diff", "--no-textconv")); err != nil {
		return err
	}
	if digest, err := opts.FileDigest(filepath.Join(rescueDir, "tracked.patch")); err == nil {
		artifacts = append(artifacts, agentrunner.RescueArtifactDigest{Path: "tracked.patch", SHA256: digest})
	} else {
		return err
	}
	if err := MapCaptureError(opts.StepName, recordArtifact(&budget, filepath.Join(rescueDir, "tracked.patch"), "tracked.patch")); err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		return err
	}
	if err := MapCaptureError(opts.StepName, opts.WriteGitOutput(ctx, opts.Allocation.Path, filepath.Join(rescueDir, "staged.patch"), "diff", "--cached", "--binary", "--no-ext-diff", "--no-textconv")); err != nil {
		return err
	}
	if digest, err := opts.FileDigest(filepath.Join(rescueDir, "staged.patch")); err == nil {
		artifacts = append(artifacts, agentrunner.RescueArtifactDigest{Path: "staged.patch", SHA256: digest})
	} else {
		return err
	}
	if err := MapCaptureError(opts.StepName, recordArtifact(&budget, filepath.Join(rescueDir, "staged.patch"), "staged.patch")); err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		return err
	}
	untrackedArtifacts, err := opts.CopyUntracked(ctx, opts.Allocation.Path, rescueDir, &budget)
	if err != nil {
		return MapCaptureError(opts.StepName, err)
	}
	artifacts = append(artifacts, untrackedArtifacts...)

	ignoredArtifacts, err := opts.CopyIgnored(ctx, opts.Allocation.Path, rescueDir, &budget)
	if err != nil {
		return MapCaptureError(opts.StepName, err)
	}
	artifacts = append(artifacts, ignoredArtifacts...)

	ignoredPath := filepath.Join(rescueDir, "ignored.txt")
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := opts.WriteIgnored(ctx, opts.Allocation.Path, ignoredPath); err != nil {
		return err
	}
	if digest, err := opts.FileDigest(ignoredPath); err == nil {
		artifacts = append(artifacts, agentrunner.RescueArtifactDigest{Path: "ignored.txt", SHA256: digest})
	} else {
		return err
	}
	if err := MapCaptureError(opts.StepName, recordArtifact(&budget, ignoredPath, "ignored.txt")); err != nil {
		return err
	}

	rescueState := agentrunner.RescueStateFile{
		ExpectedBaseSHA:  opts.State.ExpectedBaseSHA,
		RescuedHeadSHA:   headSHA,
		RetryCount:       nextRetry,
		CommitCount:      commitCount,
		BundleMode:       bundleMode,
		CreatedAt:        rescueNow(opts.Now).UTC(),
		Artifacts:        artifacts,
		DirtyFingerprint: dirtyFingerprint,
	}
	if err := agentrunner.WriteRescueState(filepath.Join(rescueDir, "state.json"), rescueState); err != nil {
		return err
	}
	return opts.VerifyState(rescueDir)
}
