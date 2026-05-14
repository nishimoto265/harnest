package step20_implement

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
	"github.com/nishimoto265/harnest/internal/steps/agentrunner"
)

func (s *Step) writeSuccessArtifacts(ctx context.Context, run RunContext, allocation contracts.WorktreeAllocation, runResult runnerResult) error {
	if err := run.IO.ValidateWorktreeAllocation(allocation); err != nil {
		return err
	}
	collectCtx, cancel := context.WithTimeout(ctx, successCollectTTL)
	defer cancel()

	headSHA, err := gitOutputContext(collectCtx, strings.TrimSpace, allocation.Path, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if err := agentrunner.ValidateSuccessHead(collectCtx, allocation, headSHA, "step20"); err != nil {
		return err
	}
	if headSHA != allocation.BaseSHA {
		if err := rejectCommittedPolicyArtifactChanges(collectCtx, allocation); err != nil {
			return err
		}
	}
	checklistPath, err := artifactPath(run.IO, run.Pass, run.Agent, checklistFileName)
	if err != nil {
		return err
	}
	checklist, err := loadChecklistArtifact(collectCtx, allocation.Path, run.IO.RunID, run.Pass, run.Agent)
	if err != nil {
		return err
	}
	if err := internalio.WriteJSONAtomic(checklistPath, checklist); err != nil {
		return err
	}
	diffPath, err := artifactPath(run.IO, run.Pass, run.Agent, diffFileName)
	if err != nil {
		return err
	}
	diffBytes, err := successDiffBytes(collectCtx, allocation.Path, allocation.BaseSHA)
	if err != nil {
		return err
	}
	if len(diffBytes) == 0 {
		return s.writeNoChangeManifest(ctx, run, runResult)
	}
	syntheticCommit := false
	syntheticParent := ""
	if headSHA == allocation.BaseSHA {
		headSHA, syntheticParent, err = synthesizeSuccessCommit(collectCtx, allocation, run)
		if err != nil {
			return err
		}
		syntheticCommit = true
	}
	if err := internalio.WriteAtomic(diffPath, diffBytes); err != nil {
		return err
	}
	if syntheticCommit {
		if err := finalizeSyntheticSuccessCommit(collectCtx, allocation, headSHA, syntheticParent, "step20"); err != nil {
			return err
		}
	}

	prefix := manifestPrefix(run.Pass, run.Agent)
	manifest := contracts.Manifest{
		Kind: contracts.ManifestKindSuccess,
		Value: contracts.ManifestSuccess{
			Kind:          contracts.ManifestKindSuccess,
			SchemaVersion: "1",
			RunID:         run.IO.RunID,
			Pass:          run.Pass,
			Agent:         run.Agent,
			BranchName:    allocation.Branch,
			HeadSHA:       headSHA,
			BaseSHA:       allocation.BaseSHA,
			DiffPath:      filepath.Join(prefix, diffFileName),
			SessionPath:   filepath.Join(prefix, sessionFileName),
			ChecklistPath: filepath.Join(prefix, checklistFileName),
			PromptVersion: promptVersion,
			StartedAt:     runResult.StartedAt.UTC(),
			FinishedAt:    runResult.FinishedAt.UTC(),
		},
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return writeManifest(run.IO, run.Pass, run.Agent, manifest)
}

func (s *Step) writeNoChangeManifest(ctx context.Context, run RunContext, runResult runnerResult) error {
	manifest := contracts.Manifest{
		Kind: contracts.ManifestKindError,
		Value: contracts.ManifestError{
			Kind:          contracts.ManifestKindError,
			SchemaVersion: "1",
			RunID:         run.IO.RunID,
			Pass:          run.Pass,
			Agent:         run.Agent,
			ExitCode:      0,
			Reason:        "unknown",
			Detail:        "agent produced no diff",
			StartedAt:     runResult.StartedAt.UTC(),
			FinishedAt:    runResult.FinishedAt.UTC(),
		},
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return writeManifest(run.IO, run.Pass, run.Agent, manifest)
}

func (s *Step) writeErrorManifest(ctx context.Context, run RunContext, runResult runnerResult) error {
	reason := agentrunner.InterruptionReason(runResult.ExitCode, runResult.StdoutSnippet, runResult.StderrSnippet)
	manifest := contracts.Manifest{
		Kind: contracts.ManifestKindError,
		Value: contracts.ManifestError{
			Kind:          contracts.ManifestKindError,
			SchemaVersion: "1",
			RunID:         run.IO.RunID,
			Pass:          run.Pass,
			Agent:         run.Agent,
			ExitCode:      runResult.ExitCode,
			Reason:        string(reason),
			Detail:        agentrunner.TruncateDetail(runResult.StderrSnippet, runResult.StdoutSnippet),
			StartedAt:     runResult.StartedAt.UTC(),
			FinishedAt:    runResult.FinishedAt.UTC(),
		},
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return writeManifest(run.IO, run.Pass, run.Agent, manifest)
}

func (s *Step) writeTimeoutManifest(ctx context.Context, run RunContext, timeout time.Duration, startedAt, finishedAt time.Time) error {
	manifest := contracts.Manifest{
		Kind: contracts.ManifestKindTimeout,
		Value: contracts.ManifestTimeout{
			Kind:           contracts.ManifestKindTimeout,
			SchemaVersion:  "1",
			RunID:          run.IO.RunID,
			Pass:           run.Pass,
			Agent:          run.Agent,
			TimeoutSeconds: int(timeout / time.Second),
			StartedAt:      startedAt,
			FinishedAt:     finishedAt,
		},
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return writeManifest(run.IO, run.Pass, run.Agent, manifest)
}

func writeManifest(runIO internalio.RunContext, pass int, agent contracts.AgentID, manifest contracts.Manifest) error {
	path, err := runIO.ManifestPath(pass, agent)
	if err != nil {
		return err
	}
	return internalio.WriteJSONAtomic(path, manifest)
}

func artifactPath(runIO internalio.RunContext, pass int, agent contracts.AgentID, name string) (string, error) {
	rel := filepath.Join(manifestPrefix(pass, agent), name)
	return runIO.ResolveRunRelative(rel)
}

func loadChecklistArtifact(ctx context.Context, worktreePath string, runID contracts.RunID, pass int, agent contracts.AgentID) (contracts.ChecklistResult, error) {
	return agentrunner.LoadChecklistArtifactContext(ctx, worktreePath, checklistFileName, "step20", runID, pass, agent)
}
