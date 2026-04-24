package policyrepo

import (
	"bytes"
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
	"sort"
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
	metadataLocalName    = "snapshot.json"
	idempotencyLocalName = "rules-idempotency-index.jsonl"
	rulesLocalDirName    = "rules"
)

var runGit = func(ctx context.Context, env []string, args ...string) ([]byte, error) {
	cmd, err := processenv.TrustedCommandContext(ctx, "git", args...)
	if err != nil {
		return nil, err
	}
	cmd.Env = env
	return cmd.CombinedOutput()
}

var removePreparedPublishWorktree = removeWorktree

type snapshot struct {
	registry        []byte
	registryPresent bool
	rules           map[string][]byte
}

type SnapshotMetadata struct {
	SchemaVersion string `json:"schema_version" validate:"required,oneof=1"`
	PolicyBranch  string `json:"policy_branch,omitempty"`
	PolicyHead    string `json:"policy_head,omitempty" validate:"omitempty,sha1_hex"`
	RegistryHead  string `json:"registry_head" validate:"omitempty,sha256_hex"`
}

type ActiveRule struct {
	RuleID   string
	RulePath string
	Body     string
}

type PreparedPublish struct {
	RepoRoot     string
	Branch       string
	ExpectedHead string
	Head         string
	worktreeDir  string
	needsPush    bool
	cleaned      bool
}

func HydrateFromBranch(ctx context.Context, repoRoot, branch, runsBase string) error {
	_, err := hydrateSnapshotFromBranch(ctx, repoRoot, branch, runsBase, "")
	return err
}

func HydrateAndSnapshotFromBranch(ctx context.Context, repoRoot, branch, runsBase, runDir string) error {
	_, err := hydrateSnapshotFromBranch(ctx, repoRoot, branch, runsBase, runDir)
	return err
}

func SnapshotLocalForRun(ctx context.Context, runsBase, runDir string) error {
	lock, err := internalio.AcquireFileLockContext(ctx, filepath.Join(runsBase, "promotion.lock"))
	if err != nil {
		return err
	}
	defer func() { _ = lock.Unlock() }()

	snap, err := loadOptionalLocalSnapshot(runsBase)
	if err != nil {
		return err
	}
	if err := applySnapshotToRunDir(runDir, snap); err != nil {
		return err
	}
	registryPath := filepath.Join(runDir, "policy", registryLocalName)
	if len(snap.registry) == 0 {
		if err := internalio.WriteAtomic(registryPath, nil); err != nil {
			return err
		}
	}
	registryHead, err := registryHead(registryPath)
	if err != nil {
		return err
	}
	return writeSnapshotMetadata(runDir, SnapshotMetadata{
		SchemaVersion: "1",
		RegistryHead:  registryHead,
	})
}

func hydrateSnapshotFromBranch(ctx context.Context, repoRoot, branch, runsBase, runDir string) (snapshot, error) {
	lock, err := internalio.AcquireFileLockContext(ctx, filepath.Join(runsBase, "promotion.lock"))
	if err != nil {
		return snapshot{}, err
	}
	defer func() { _ = lock.Unlock() }()

	if err := fetchBranch(ctx, repoRoot, branch); err != nil {
		return snapshot{}, err
	}
	policyHead, err := branchHead(ctx, repoRoot, branch)
	if err != nil {
		return snapshot{}, err
	}
	snap, err := loadBranchSnapshot(ctx, repoRoot, branch)
	if err != nil {
		return snapshot{}, err
	}
	if strings.TrimSpace(runDir) != "" {
		if err := applySnapshotToRunDir(runDir, snap); err != nil {
			return snapshot{}, err
		}
		registryHead, err := registryHead(filepath.Join(runDir, "policy", registryLocalName))
		if err != nil {
			return snapshot{}, err
		}
		if err := writeSnapshotMetadata(runDir, SnapshotMetadata{
			SchemaVersion: "1",
			PolicyBranch:  branch,
			PolicyHead:    policyHead,
			RegistryHead:  registryHead,
		}); err != nil {
			return snapshot{}, err
		}
	}
	if err := applySnapshot(runsBase, snap); err != nil {
		return snapshot{}, err
	}
	return snap, nil
}

func PublishSnapshot(ctx context.Context, repoRoot, branch, expectedHead, runsBase, runID string) (string, error) {
	plan, err := PrepareSnapshotPublish(ctx, repoRoot, branch, expectedHead, runsBase, runID)
	if err != nil {
		return "", err
	}
	defer func() {
		_ = plan.Cleanup()
	}()
	if err := plan.Push(ctx); err != nil {
		return "", err
	}
	return plan.Head, nil
}

func PrepareSnapshotPublish(ctx context.Context, repoRoot, branch, expectedHead, runsBase, runID string) (*PreparedPublish, error) {
	if expectedHead == "" {
		return nil, fmt.Errorf("policyrepo: expected head is required for publish")
	}
	snap, err := loadLocalSnapshot(runsBase)
	if err != nil {
		return nil, err
	}
	if err := fetchBranch(ctx, repoRoot, branch); err != nil {
		return nil, err
	}
	tmpDir, err := os.MkdirTemp(runsBase, "policy-publish-"+sanitizeRunID(runID)+"-")
	if err != nil {
		return nil, err
	}
	plan := &PreparedPublish{
		RepoRoot:     repoRoot,
		Branch:       branch,
		ExpectedHead: expectedHead,
		Head:         expectedHead,
		worktreeDir:  tmpDir,
	}
	if _, err := gitText(ctx, repoRoot, "worktree", "add", "--detach", tmpDir, expectedHead); err != nil {
		_ = plan.Cleanup()
		return nil, err
	}

	if err := syncSnapshotToWorktree(tmpDir, snap); err != nil {
		_ = plan.Cleanup()
		return nil, err
	}
	if _, err := gitText(ctx, tmpDir, "add", "-A", "--", RepoDirName); err != nil {
		_ = plan.Cleanup()
		return nil, err
	}
	hasDiff, err := hasStagedDiff(ctx, tmpDir)
	if err != nil {
		_ = plan.Cleanup()
		return nil, err
	}
	if !hasDiff {
		if err := plan.Cleanup(); err != nil {
			return nil, err
		}
		return plan, nil
	}

	env := processenv.GitLocalEnv()
	env = append(env,
		"GIT_AUTHOR_NAME=auto-improve",
		"GIT_AUTHOR_EMAIL=auto-improve@local",
		"GIT_COMMITTER_NAME=auto-improve",
		"GIT_COMMITTER_EMAIL=auto-improve@local",
		"GIT_AUTHOR_DATE=2000-01-01T00:00:00Z",
		"GIT_COMMITTER_DATE=2000-01-01T00:00:00Z",
	)
	if _, err := runGit(ctx, env, "-C", tmpDir, "commit", "-m", fmt.Sprintf("auto-improve: publish policy snapshot for %s", runID)); err != nil {
		_ = plan.Cleanup()
		return nil, fmt.Errorf("policyrepo: commit policy snapshot: %w", err)
	}
	headBytes, err := gitText(ctx, tmpDir, "rev-parse", "HEAD")
	if err != nil {
		_ = plan.Cleanup()
		return nil, err
	}
	plan.Head = strings.TrimSpace(string(headBytes))
	plan.needsPush = true
	return plan, nil
}

func (p *PreparedPublish) Push(ctx context.Context) error {
	if p == nil {
		return errors.New("policyrepo: prepared publish is required")
	}
	if !p.needsPush {
		return nil
	}
	remoteURL, err := originPushURL(ctx, p.RepoRoot)
	if err != nil {
		return err
	}
	if _, err := runGit(ctx, processenv.GitNetworkEnvForRemoteURL(remoteURL), "-C", p.RepoRoot, "push", "origin", fmt.Sprintf("%s:%s", p.Head, p.Branch), fmt.Sprintf("--force-with-lease=%s:%s", p.Branch, p.ExpectedHead)); err != nil {
		return fmt.Errorf("policyrepo: push policy snapshot: %w", err)
	}
	return nil
}

func (p *PreparedPublish) Cleanup() error {
	if p == nil || p.cleaned || p.worktreeDir == "" {
		return nil
	}
	if err := removePreparedPublishWorktree(p.RepoRoot, p.worktreeDir); err != nil {
		removeErr := os.RemoveAll(p.worktreeDir)
		if removeErr != nil {
			return fmt.Errorf("policyrepo: remove policy worktree after publish: %w; remove temp dir: %v", err, removeErr)
		}
		return fmt.Errorf("policyrepo: remove policy worktree after publish: %w", err)
	}
	p.cleaned = true
	return nil
}

func BranchSnapshotMatchesLocal(ctx context.Context, repoRoot, branch, runsBase string) (bool, error) {
	if err := fetchBranch(ctx, repoRoot, branch); err != nil {
		return false, err
	}
	remote, err := loadBranchSnapshot(ctx, repoRoot, branch)
	if err != nil {
		return false, err
	}
	local, err := loadLocalSnapshot(runsBase)
	if err != nil {
		return false, err
	}
	return snapshotsEqual(remote, local), nil
}

func snapshotsEqual(a, b snapshot) bool {
	if a.registryPresent != b.registryPresent || !bytes.Equal(a.registry, b.registry) || len(a.rules) != len(b.rules) {
		return false
	}
	for path, aBody := range a.rules {
		if !bytes.Equal(aBody, b.rules[path]) {
			return false
		}
	}
	return true
}

func applySnapshotToRunDir(runDir string, snap snapshot) error {
	dst := filepath.Join(runDir, "policy")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	return applySnapshot(dst, snap)
}

func LoadSnapshotMetadata(runCtx internalio.RunContext) (SnapshotMetadata, bool, error) {
	path := filepath.Join(runCtx.RunDir(), "policy", metadataLocalName)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return SnapshotMetadata{}, false, nil
		}
		return SnapshotMetadata{}, false, err
	}
	meta, err := internalio.ReadJSON[SnapshotMetadata](path)
	if err != nil {
		return SnapshotMetadata{}, false, err
	}
	return meta, true, nil
}

func RegistryPathForRun(runCtx internalio.RunContext) (string, error) {
	policyDir := filepath.Join(runCtx.RunDir(), "policy")
	snapshotPath := runCtx.PolicySnapshotRegistryPath()
	if _, err := os.Stat(snapshotPath); err == nil {
		return snapshotPath, nil
	} else if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if _, err := os.Stat(policyDir); err == nil {
		return "", fmt.Errorf("policyrepo: run policy snapshot is missing %s", registryLocalName)
	} else if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	return runCtx.RulesRegistryPath(), nil
}

func LoadActiveRulesForRun(runCtx internalio.RunContext) ([]ActiveRule, error) {
	registryPath, err := RegistryPathForRun(runCtx)
	if err != nil {
		return nil, err
	}
	return LoadActiveRules(registryPath)
}

func LoadActiveRules(registryPath string) ([]ActiveRule, error) {
	if _, err := os.Stat(registryPath); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	lines, err := internalio.RegistryLines(registryPath)
	if err != nil {
		return nil, err
	}
	entries := make([]contracts.RuleRegistryEntry, 0, len(lines))
	for _, line := range lines {
		entries = append(entries, line.Entry)
	}
	states, err := registryview.Build(entries)
	if err != nil {
		return nil, err
	}
	active := registryview.Active(states)
	if len(active) == 0 {
		return nil, nil
	}
	ruleIDs := make([]string, 0, len(active))
	for ruleID := range active {
		ruleIDs = append(ruleIDs, ruleID)
	}
	sort.Strings(ruleIDs)
	base := filepath.Dir(registryPath)
	rules := make([]ActiveRule, 0, len(ruleIDs))
	for _, ruleID := range ruleIDs {
		state := active[ruleID]
		if err := contracts.ValidateRulePath(state.RulePath); err != nil {
			return nil, err
		}
		bodyPath := filepath.Join(base, filepath.FromSlash(state.RulePath))
		body, err := internalio.OpenValidatedRegularFile(bodyPath, base)
		if err != nil {
			return nil, err
		}
		if got := sha256Hex(body); got != state.Sha256 {
			return nil, fmt.Errorf("policyrepo: active rule body sha mismatch: rule_id=%s got=%s want=%s", ruleID, got, state.Sha256)
		}
		rules = append(rules, ActiveRule{
			RuleID:   ruleID,
			RulePath: state.RulePath,
			Body:     string(body),
		})
	}
	return rules, nil
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
	if snap.registryPresent {
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

func loadBranchSnapshot(ctx context.Context, repoRoot, branch string) (snapshot, error) {
	files, err := listPolicyFiles(ctx, repoRoot, branch)
	if err != nil {
		return snapshot{}, err
	}
	if len(files) == 0 {
		return snapshot{}, fmt.Errorf("policyrepo: %s has no managed policy files", branch)
	}
	if !slices.Contains(files, RegistryRepoRelPath) {
		return snapshot{}, fmt.Errorf("policyrepo: %s is missing %s", branch, RegistryRepoRelPath)
	}
	snap := snapshot{rules: make(map[string][]byte, len(files))}
	for _, rel := range files {
		data, err := readBranchFile(ctx, repoRoot, branch, rel)
		if err != nil {
			return snapshot{}, err
		}
		switch {
		case rel == RegistryRepoRelPath:
			snap.registry = data
			snap.registryPresent = true
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
		registry:        registryBytes,
		registryPresent: true,
		rules:           make(map[string][]byte),
	}
	rulesSrc := filepath.Join(runsBase, rulesLocalDirName)
	if _, err := os.Stat(rulesSrc); err != nil {
		if os.IsNotExist(err) {
			if err := validateSnapshot(snap); err != nil {
				return snapshot{}, err
			}
			return snap, nil
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

func loadOptionalLocalSnapshot(runsBase string) (snapshot, error) {
	registryPath := filepath.Join(runsBase, registryLocalName)
	if _, err := os.Stat(registryPath); err == nil {
		return loadLocalSnapshot(runsBase)
	} else if err != nil && !os.IsNotExist(err) {
		return snapshot{}, err
	}
	rulesSrc := filepath.Join(runsBase, rulesLocalDirName)
	if _, statErr := os.Stat(rulesSrc); statErr == nil {
		return loadLocalSnapshot(runsBase)
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return snapshot{}, statErr
	}
	return snapshot{rules: map[string][]byte{}}, nil
}

func writeSnapshotMetadata(runDir string, meta SnapshotMetadata) error {
	path := filepath.Join(runDir, "policy", metadataLocalName)
	return internalio.WriteJSONAtomic(path, meta)
}

func registryHead(path string) (string, error) {
	lines, err := internalio.RegistryLines(path)
	if err != nil {
		return "", err
	}
	if len(lines) == 0 {
		return "", nil
	}
	return lines[len(lines)-1].Sha256, nil
}

func listPolicyFiles(ctx context.Context, repoRoot, branch string) ([]string, error) {
	out, err := gitText(ctx, repoRoot, "ls-tree", "-r", "--name-only", "origin/"+branch, "--", RepoDirName)
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

func readBranchFile(ctx context.Context, repoRoot, branch, rel string) ([]byte, error) {
	out, err := gitRaw(ctx, repoRoot, "show", "origin/"+branch+":"+rel)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func localRulePathForRepoRelative(rel string) string {
	return filepath.ToSlash(strings.TrimPrefix(rel, RepoDirName+"/"))
}

func validateSnapshot(snap snapshot) error {
	if !snap.registryPresent {
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
	if snap.registryPresent {
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
	if snap.registryPresent {
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

func fetchBranch(ctx context.Context, repoRoot, branch string) error {
	remoteURL, err := originRemoteURL(ctx, repoRoot)
	if err != nil {
		return err
	}
	_, err = runGit(ctx, processenv.GitNetworkEnvForRemoteURL(remoteURL), "-C", repoRoot, "fetch", "--no-tags", "origin", branch)
	if err != nil {
		return fmt.Errorf("policyrepo: fetch branch %s: %w", branch, err)
	}
	return nil
}

func originRemoteURL(ctx context.Context, repoRoot string) (string, error) {
	out, err := gitText(ctx, repoRoot, "remote", "get-url", "origin")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func originPushURL(ctx context.Context, repoRoot string) (string, error) {
	out, err := gitText(ctx, repoRoot, "remote", "get-url", "--push", "--all", "origin")
	if err != nil {
		return "", err
	}
	return preferredRemoteURLForAuth(string(out)), nil
}

func preferredRemoteURLForAuth(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	first := ""
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if first == "" {
			first = line
		}
		if strings.HasPrefix(strings.ToLower(line), "https://") {
			return line
		}
	}
	return first
}

func branchHead(ctx context.Context, repoRoot, branch string) (string, error) {
	head, err := gitText(ctx, repoRoot, "rev-parse", "origin/"+branch)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(head)), nil
}

func gitRaw(ctx context.Context, repoRoot string, args ...string) ([]byte, error) {
	out, err := runGit(ctx, processenv.GitLocalEnv(), append([]string{"-C", repoRoot}, args...)...)
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
	_, err := runGit(ctx, processenv.GitLocalEnv(), "-C", repoRoot, "diff", "--no-ext-diff", "--cached", "--quiet", "--", RepoDirName)
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
	out, err := runGit(context.Background(), processenv.GitLocalEnv(), "-C", repoRoot, "worktree", "remove", "--force", path)
	if err != nil {
		return fmt.Errorf("policyrepo: remove worktree %s: %w: %s", path, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func sanitizeRunID(runID string) string {
	replacer := strings.NewReplacer("/", "-", "\\", "-", " ", "-")
	return replacer.Replace(runID)
}
