package policyrepo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/processenv"
	"github.com/nishimoto265/auto-improve/internal/registryview"
)

const (
	RepoDirName          = "auto-improve"
	RegistryRepoRelPath  = "auto-improve/rules-registry.jsonl"
	RulesRepoDirRelPath  = "auto-improve/rules"
	registryLocalName    = "rules-registry.jsonl"
	idempotencyLocalName = "rules-idempotency-index.jsonl"
	rulesLocalDirName    = "rules"
)

var runGit = func(ctx context.Context, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = env
	return cmd.CombinedOutput()
}

type snapshot struct {
	registry []byte
	rules    map[string][]byte
}

type Options struct {
	Remote string
}

func HydrateFromBranch(ctx context.Context, repoRoot, branch, runsBase string) error {
	return HydrateFromBranchWithOptions(ctx, repoRoot, branch, runsBase, Options{})
}

func HydrateFromBranchWithOptions(ctx context.Context, repoRoot, branch, runsBase string, opts Options) error {
	lock, err := internalio.AcquireFileLockContext(ctx, filepath.Join(runsBase, "promotion.lock"))
	if err != nil {
		return err
	}
	defer func() { _ = lock.Unlock() }()
	remote := opts.remoteName()
	if err := fetchBranch(ctx, repoRoot, remote, branch); err != nil {
		return err
	}
	snap, err := loadBranchSnapshot(ctx, repoRoot, remote, branch)
	if err != nil {
		return err
	}
	return applySnapshot(runsBase, snap)
}

func PublishSnapshot(ctx context.Context, repoRoot, branch, expectedHead, runsBase, runID string) (string, error) {
	return PublishSnapshotWithOptions(ctx, repoRoot, branch, expectedHead, runsBase, runID, Options{})
}

func PublishSnapshotWithOptions(ctx context.Context, repoRoot, branch, expectedHead, runsBase, runID string, opts Options) (string, error) {
	if expectedHead == "" {
		return "", fmt.Errorf("policyrepo: expected head is required for publish")
	}
	snap, err := loadLocalSnapshot(runsBase)
	if err != nil {
		return "", err
	}
	remote := opts.remoteName()
	if err := fetchBranch(ctx, repoRoot, remote, branch); err != nil {
		return "", err
	}
	tmpDir, err := os.MkdirTemp(runsBase, "policy-publish-"+sanitizeRunID(runID)+"-")
	if err != nil {
		return "", err
	}
	cleanupDone := false
	defer func() {
		if !cleanupDone {
			_ = removeWorktree(repoRoot, tmpDir)
		}
	}()
	if _, err := gitText(ctx, repoRoot, "worktree", "add", "--detach", tmpDir, expectedHead); err != nil {
		return "", err
	}

	if err := syncSnapshotToWorktree(tmpDir, snap); err != nil {
		return "", err
	}
	if _, err := gitText(ctx, tmpDir, "add", "-A", "--", RepoDirName); err != nil {
		return "", err
	}
	hasDiff, err := hasStagedDiff(ctx, tmpDir)
	if err != nil {
		return "", err
	}
	if !hasDiff {
		cleanupDone = true
		if removeErr := removeWorktree(repoRoot, tmpDir); removeErr != nil {
			return "", removeErr
		}
		return expectedHead, nil
	}

	env := processenv.Sanitize()
	env = append(env,
		"GIT_AUTHOR_NAME=auto-improve",
		"GIT_AUTHOR_EMAIL=auto-improve@local",
		"GIT_COMMITTER_NAME=auto-improve",
		"GIT_COMMITTER_EMAIL=auto-improve@local",
	)
	if _, err := runGit(ctx, env, "-C", tmpDir, "commit", "-m", fmt.Sprintf("auto-improve: publish policy snapshot for %s", runID)); err != nil {
		return "", fmt.Errorf("policyrepo: commit policy snapshot: %w", err)
	}
	headBytes, err := gitText(ctx, tmpDir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	newHead := strings.TrimSpace(string(headBytes))
	if _, err := runGit(ctx, processenv.SanitizeForNetworkExec(), "-C", repoRoot, "push", remote, fmt.Sprintf("%s:%s", newHead, branch), fmt.Sprintf("--force-with-lease=%s:%s", branch, expectedHead)); err != nil {
		return "", fmt.Errorf("policyrepo: push policy snapshot: %w", err)
	}
	cleanupDone = true
	if err := removeWorktree(repoRoot, tmpDir); err != nil {
		removeErr := os.RemoveAll(tmpDir)
		if removeErr != nil {
			return newHead, fmt.Errorf("policyrepo: remove policy worktree after publish: %w; remove temp dir: %v", err, removeErr)
		}
		return newHead, fmt.Errorf("policyrepo: remove policy worktree after publish: %w", err)
	}
	return newHead, nil
}

func (o Options) remoteName() string {
	remote := strings.TrimSpace(o.Remote)
	if remote == "" {
		return "origin"
	}
	return remote
}

func syncSnapshotToWorktree(worktreeDir string, snap snapshot) error {
	repoRootDir := filepath.Join(worktreeDir, RepoDirName)
	registryDst := filepath.Join(worktreeDir, RegistryRepoRelPath)
	rulesDst := filepath.Join(worktreeDir, RulesRepoDirRelPath)
	if err := os.RemoveAll(registryDst); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.RemoveAll(rulesDst); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(repoRootDir, 0o755); err != nil {
		return err
	}
	if len(snap.registry) > 0 {
		if err := internalio.WriteAtomic(registryDst, snap.registry); err != nil {
			return err
		}
	}
	if len(snap.rules) == 0 {
		return nil
	}
	for rulePath, data := range snap.rules {
		if err := contracts.ValidateRulePath(rulePath); err != nil {
			return err
		}
		dstPath := filepath.Join(worktreeDir, RepoDirName, filepath.FromSlash(rulePath))
		if err := internalio.WriteAtomic(dstPath, data); err != nil {
			return err
		}
	}
	return nil
}

func loadBranchSnapshot(ctx context.Context, repoRoot, remote, branch string) (snapshot, error) {
	files, err := listPolicyFiles(ctx, repoRoot, remote, branch)
	if err != nil {
		return snapshot{}, err
	}
	if len(files) == 0 {
		return snapshot{rules: map[string][]byte{}}, nil
	}
	if !slices.Contains(files, RegistryRepoRelPath) {
		return snapshot{}, fmt.Errorf("policyrepo: %s is missing %s", branch, RegistryRepoRelPath)
	}
	snap := snapshot{rules: make(map[string][]byte, len(files))}
	for _, rel := range files {
		data, err := readBranchFile(ctx, repoRoot, remote, branch, rel)
		if err != nil {
			return snapshot{}, err
		}
		switch {
		case rel == RegistryRepoRelPath:
			snap.registry = data
		case strings.HasPrefix(rel, RulesRepoDirRelPath+"/"):
			snap.rules[localRulePathForRepoRelative(rel)] = data
		}
	}
	if err := validateSnapshot(snap); err != nil {
		return snapshot{}, err
	}
	return snap, nil
}

func loadLocalSnapshot(runsBase string) (snapshot, error) {
	registryPath := filepath.Join(runsBase, registryLocalName)
	registryBytes, err := os.ReadFile(registryPath)
	if err != nil {
		if os.IsNotExist(err) {
			return snapshot{}, fmt.Errorf("policyrepo: local policy snapshot is missing %s", registryLocalName)
		}
		return snapshot{}, err
	}
	snap := snapshot{
		registry: registryBytes,
		rules:    make(map[string][]byte),
	}
	rulesSrc := filepath.Join(runsBase, rulesLocalDirName)
	if _, err := os.Stat(rulesSrc); err != nil {
		if os.IsNotExist(err) {
			return snapshot{}, fmt.Errorf("policyrepo: local policy snapshot is missing %s", rulesLocalDirName)
		}
		return snapshot{}, err
	}
	err = filepath.WalkDir(rulesSrc, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(rulesSrc, path)
		if err != nil {
			return err
		}
		if rel == "." || d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		snap.rules[filepath.ToSlash(filepath.Join(rulesLocalDirName, rel))] = data
		return nil
	})
	if err != nil {
		return snapshot{}, err
	}
	if err := validateSnapshot(snap); err != nil {
		return snapshot{}, err
	}
	return snap, nil
}

func listPolicyFiles(ctx context.Context, repoRoot, remote, branch string) ([]string, error) {
	out, err := gitText(ctx, repoRoot, "ls-tree", "-r", "--name-only", remoteRef(remote, branch), "--", RepoDirName)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	files := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if line == RegistryRepoRelPath || strings.HasPrefix(line, RulesRepoDirRelPath+"/") {
			files = append(files, line)
		}
	}
	slices.Sort(files)
	return files, nil
}

func readBranchFile(ctx context.Context, repoRoot, remote, branch, rel string) ([]byte, error) {
	out, err := gitRaw(ctx, repoRoot, "show", remoteRef(remote, branch)+":"+rel)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func localPathForRepoRelative(runsBase, rel string) (string, error) {
	switch {
	case rel == RegistryRepoRelPath:
		return filepath.Join(runsBase, registryLocalName), nil
	case strings.HasPrefix(rel, RulesRepoDirRelPath+"/"):
		suffix := strings.TrimPrefix(rel, RulesRepoDirRelPath+"/")
		return filepath.Join(runsBase, rulesLocalDirName, filepath.FromSlash(suffix)), nil
	default:
		return "", fmt.Errorf("policyrepo: unsupported repo policy path %q", rel)
	}
}

func localRulePathForRepoRelative(rel string) string {
	return filepath.ToSlash(strings.TrimPrefix(rel, RepoDirName+"/"))
}

func validateSnapshot(snap snapshot) error {
	if len(snap.registry) == 0 {
		if len(snap.rules) == 0 {
			return nil
		}
		return fmt.Errorf("policyrepo: registry is required when rules are present")
	}
	entries, err := decodeRegistryEntries(snap.registry)
	if err != nil {
		return err
	}
	states, err := registryview.Build(entries)
	if err != nil {
		return err
	}
	for ruleID, state := range registryview.Active(states) {
		body, ok := snap.rules[state.RulePath]
		if !ok {
			return fmt.Errorf("policyrepo: active rule body missing: rule_id=%s rule_path=%s", ruleID, state.RulePath)
		}
		if got := sha256Hex(body); got != state.Sha256 {
			return fmt.Errorf("policyrepo: active rule body sha mismatch: rule_id=%s got=%s want=%s", ruleID, got, state.Sha256)
		}
	}
	return nil
}

func decodeRegistryEntries(data []byte) ([]contracts.RuleRegistryEntry, error) {
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	entries := make([]contracts.RuleRegistryEntry, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry contracts.RuleRegistryEntry
		if err := contracts.DecodeStrictJSON([]byte(line), &entry); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func applySnapshot(runsBase string, snap snapshot) error {
	stageDir, err := os.MkdirTemp(runsBase, ".policy-stage-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stageDir)
	if err := stageSnapshot(stageDir, snap); err != nil {
		return err
	}
	backupDir, err := os.MkdirTemp(runsBase, ".policy-backup-")
	if err != nil {
		return err
	}
	restore, err := moveCurrentPolicyToBackup(runsBase, backupDir)
	if err != nil {
		_ = os.RemoveAll(backupDir)
		return err
	}
	if err := moveSnapshotIntoPlace(runsBase, stageDir, snap); err != nil {
		_ = restore()
		_ = os.RemoveAll(backupDir)
		return err
	}
	return os.RemoveAll(backupDir)
}

func stageSnapshot(stageDir string, snap snapshot) error {
	if len(snap.registry) > 0 {
		if err := internalio.WriteAtomic(filepath.Join(stageDir, registryLocalName), snap.registry); err != nil {
			return err
		}
	}
	for rulePath, data := range snap.rules {
		if err := contracts.ValidateRulePath(rulePath); err != nil {
			return err
		}
		if err := internalio.WriteAtomic(filepath.Join(stageDir, filepath.FromSlash(rulePath)), data); err != nil {
			return err
		}
	}
	return nil
}

func moveCurrentPolicyToBackup(runsBase, backupDir string) (func() error, error) {
	type moved struct {
		src string
		dst string
	}
	movedPaths := make([]moved, 0, 3)
	for _, name := range []string{registryLocalName, idempotencyLocalName, rulesLocalDirName} {
		src := filepath.Join(runsBase, name)
		if _, err := os.Stat(src); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		dst := filepath.Join(backupDir, name)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return nil, err
		}
		if err := os.Rename(src, dst); err != nil {
			return nil, err
		}
		movedPaths = append(movedPaths, moved{src: src, dst: dst})
	}
	return func() error {
		for i := len(movedPaths) - 1; i >= 0; i-- {
			if err := os.RemoveAll(movedPaths[i].src); err != nil && !os.IsNotExist(err) {
				return err
			}
			if err := os.Rename(movedPaths[i].dst, movedPaths[i].src); err != nil {
				return err
			}
		}
		return nil
	}, nil
}

func moveSnapshotIntoPlace(runsBase, stageDir string, snap snapshot) error {
	if len(snap.registry) > 0 {
		if err := os.Rename(filepath.Join(stageDir, registryLocalName), filepath.Join(runsBase, registryLocalName)); err != nil {
			return err
		}
	}
	if len(snap.rules) > 0 {
		if err := os.Rename(filepath.Join(stageDir, rulesLocalDirName), filepath.Join(runsBase, rulesLocalDirName)); err != nil {
			return err
		}
	}
	return nil
}

func fetchBranch(ctx context.Context, repoRoot, remote, branch string) error {
	_, err := runGit(ctx, processenv.SanitizeForNetworkExec(), "-C", repoRoot, "fetch", "--no-tags", remote, branch)
	if err != nil {
		return fmt.Errorf("policyrepo: fetch branch %s: %w", branch, err)
	}
	return nil
}

func remoteRef(remote, branch string) string {
	return strings.TrimSpace(remote) + "/" + branch
}

func gitRaw(ctx context.Context, repoRoot string, args ...string) ([]byte, error) {
	out, err := runGit(ctx, processenv.Sanitize(), append([]string{"-C", repoRoot}, args...)...)
	if err != nil {
		return nil, fmt.Errorf("policyrepo: git %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

func gitText(ctx context.Context, repoRoot string, args ...string) ([]byte, error) {
	out, err := gitRaw(ctx, repoRoot, args...)
	if err != nil {
		return nil, err
	}
	return []byte(strings.TrimSpace(string(out))), nil
}

func hasStagedDiff(ctx context.Context, repoRoot string) (bool, error) {
	_, err := runGit(ctx, processenv.Sanitize(), "-C", repoRoot, "diff", "--cached", "--quiet", "--", RepoDirName)
	if err == nil {
		return false, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return true, nil
	}
	return false, fmt.Errorf("policyrepo: git diff --cached --quiet -- %s: %w", RepoDirName, err)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func removeWorktree(repoRoot, path string) error {
	out, err := runGit(context.Background(), processenv.Sanitize(), "-C", repoRoot, "worktree", "remove", "--force", path)
	if err != nil {
		return fmt.Errorf("policyrepo: remove worktree %s: %w: %s", path, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func sanitizeRunID(runID string) string {
	replacer := strings.NewReplacer("/", "-", "\\", "-", " ", "-")
	return replacer.Replace(runID)
}
