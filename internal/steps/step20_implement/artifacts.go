package step20_implement

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
)

type agentPaths struct {
	dir             string
	manifestPath    string
	sessionPath     string
	diffPath        string
	checklistPath   string
	resumeStatePath string
	heartbeatPath   string
	rescueLockPath  string
	rescuedDir      string
}

func agentPathsFor(runCtx internalio.RunContext, pass int, agent contracts.AgentID) (agentPaths, error) {
	prefix := manifestPrefix(pass, agent)
	dir, err := runCtx.ResolveRunRelative(prefix)
	if err != nil {
		return agentPaths{}, err
	}
	manifestPath, err := runCtx.ManifestPath(pass, agent)
	if err != nil {
		return agentPaths{}, err
	}
	return agentPaths{
		dir:             dir,
		manifestPath:    manifestPath,
		sessionPath:     filepath.Join(dir, "session.jsonl"),
		diffPath:        filepath.Join(dir, "diff.patch"),
		checklistPath:   filepath.Join(dir, "checklist-result.json"),
		resumeStatePath: filepath.Join(dir, ".resume-state.json"),
		heartbeatPath:   filepath.Join(dir, ".heartbeat"),
		rescueLockPath:  filepath.Join(dir, ".rescue.lock"),
		rescuedDir:      filepath.Join(dir, "rescued"),
	}, nil
}

func worktreeFor(pkg *contracts.TaskPackage, pass int, agent contracts.AgentID) (contracts.WorktreeAllocation, error) {
	if pkg == nil {
		return contracts.WorktreeAllocation{}, errors.New("step20: task package is required")
	}
	for _, worktree := range pkg.Worktrees {
		if worktree.Pass == pass && worktree.Agent == agent {
			return worktree, nil
		}
	}
	return contracts.WorktreeAllocation{}, fmt.Errorf("step20: missing worktree allocation: pass=%d agent=%s", pass, agent)
}

func manifestPrefix(pass int, agent contracts.AgentID) string {
	if pass == 2 {
		return filepath.Join("50-pass2", string(agent))
	}
	return filepath.Join("20-pass1", string(agent))
}

func ensureDir(path string) error {
	return os.MkdirAll(path, 0o755)
}

func ensureFile(path string) error {
	if err := ensureDir(filepath.Dir(path)); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDONLY, 0o644)
	if err != nil {
		return err
	}
	return file.Close()
}

func gitOutput(workdir string, args ...string) (string, error) {
	data, err := gitOutputRaw(workdir, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func gitOutputRaw(workdir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = workdir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git %v failed: %w: %s", args, err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

func gitRun(workdir string, args ...string) error {
	_, err := gitOutputRaw(workdir, args...)
	return err
}

func loadOrCreateChecklist(path string, runID contracts.RunID, pass int, agent contracts.AgentID) (contracts.ChecklistResult, error) {
	checklist, err := internalio.ReadJSON[contracts.ChecklistResult](path)
	if err == nil {
		if checklist.RunID != runID || checklist.Pass != pass || checklist.Agent != agent {
			return contracts.ChecklistResult{}, fmt.Errorf("step20: checklist metadata mismatch: run_id=%s pass=%d agent=%s", checklist.RunID, checklist.Pass, checklist.Agent)
		}
		return checklist, nil
	}
	if !os.IsNotExist(err) {
		return contracts.ChecklistResult{}, err
	}
	return contracts.ChecklistResult{
		SchemaVersion: "1",
		RunID:         runID,
		Pass:          pass,
		Agent:         agent,
		Items:         []contracts.ChecklistItem{},
	}, nil
}
