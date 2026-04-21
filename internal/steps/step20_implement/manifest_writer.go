package step20_implement

import (
	"path/filepath"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
)

func buildSuccessManifest(run RunContext, allocation contracts.WorktreeAllocation, headSHA string, startedAt, finishedAt time.Time) contracts.Manifest {
	return contracts.Manifest{
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
			DiffPath:      filepath.Join(manifestPrefix(run.Pass, run.Agent), "diff.patch"),
			SessionPath:   filepath.Join(manifestPrefix(run.Pass, run.Agent), "session.jsonl"),
			ChecklistPath: filepath.Join(manifestPrefix(run.Pass, run.Agent), "checklist-result.json"),
			PromptVersion: promptVersion,
			StartedAt:     startedAt,
			FinishedAt:    finishedAt,
		},
	}
}

func buildErrorManifest(run RunContext, exitCode int, reason, detail string, startedAt, finishedAt time.Time) contracts.Manifest {
	return contracts.Manifest{
		Kind: contracts.ManifestKindError,
		Value: contracts.ManifestError{
			Kind:          contracts.ManifestKindError,
			SchemaVersion: "1",
			RunID:         run.IO.RunID,
			Pass:          run.Pass,
			Agent:         run.Agent,
			ExitCode:      exitCode,
			Reason:        reason,
			Detail:        detail,
			StartedAt:     startedAt,
			FinishedAt:    finishedAt,
		},
	}
}

func buildTimeoutManifest(run RunContext, timeout time.Duration, startedAt, finishedAt time.Time) contracts.Manifest {
	return contracts.Manifest{
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
}

func writeManifest(path string, manifest contracts.Manifest) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	return internalio.WriteJSONAtomic(path, manifest)
}
