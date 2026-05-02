package step10restorebase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/policyrepo"
	"github.com/nishimoto265/auto-improve/internal/steps/policyoverlay"
	"github.com/stretchr/testify/require"
)

const testBaseSHA = "0123456789abcdef0123456789abcdef01234567"

const testMergeCommitOID = "89abcdef0123456789abcdef0123456789abcdef"

const testBaseRefOID = "76543210fedcba9876543210fedcba9876543210"

const testBestBranchSHA = "fedcba9876543210fedcba9876543210fedcba98"

type stubGH struct {
	info PRInfo
	err  error
}

func (s stubGH) PRView(ctx context.Context, pr int, repo string) (PRInfo, error) {
	if s.err != nil {
		return PRInfo{}, s.err
	}
	out := s.info
	if out.Number == 0 {
		out.Number = pr
	}
	return out, nil
}

type stubGit struct {
	mu                sync.Mutex
	known             map[string]string // path → sha (marks "already exists")
	createdBy         []string
	resolvedBy        map[string]string
	mergeBase         map[string]string
	fetched           []string
	fetchedBranches   []string
	repoSlug          string
	repoSlugErr       error
	repoSlugByRoot    map[string]string
	changedFiles      []string
	diffText          string
	changedFilesErr   error
	diffErr           error
	changedFilesCalls int
	diffCalls         int
}

type fakeTaskBriefGenerator struct {
	task  string
	err   error
	calls int
	input TaskBriefInput
}

func (g *fakeTaskBriefGenerator) GenerateTaskBrief(ctx context.Context, input TaskBriefInput) (string, error) {
	_ = ctx
	g.calls++
	g.input = input
	if g.err != nil {
		return "", g.err
	}
	return g.task, nil
}

func newStubGit() *stubGit {
	return &stubGit{
		known:          map[string]string{},
		resolvedBy:     map[string]string{},
		mergeBase:      map[string]string{},
		repoSlug:       "owner/repo",
		repoSlugByRoot: map[string]string{},
	}
}

func (s *stubGit) WorktreeAdd(ctx context.Context, repoRoot, path, branch, sha string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.known[path]; ok {
		if existing != sha {
			return false, fmt.Errorf("%w: path=%s expected=%s actual=%s", ErrWorktreeDrift, path, sha, existing)
		}
		return false, nil
	}
	s.known[path] = sha
	s.createdBy = append(s.createdBy, path)
	return true, nil
}

func (s *stubGit) PreparePassBase(ctx context.Context, allocation contracts.PassBaseAllocation, runID contracts.RunID, policySnapshotDir string, activeRules []policyrepo.ActiveRule, experimentLessons []policyoverlay.ExperimentLesson) (contracts.PassBaseAllocation, error) {
	return allocation, nil
}

func (s *stubGit) ResolveRef(ctx context.Context, repoRoot, ref string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sha, ok := s.resolvedBy[repoRoot+"::"+ref]; ok {
		return sha, nil
	}
	if sha, ok := s.resolvedBy[ref]; ok {
		return sha, nil
	}
	return testBaseSHA, nil
}

func (s *stubGit) MergeBase(ctx context.Context, repoRoot, left, right string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sha, ok := s.mergeBase[repoRoot+"::"+left+"::"+right]; ok {
		return sha, nil
	}
	if sha, ok := s.mergeBase[left+"::"+right]; ok {
		return sha, nil
	}
	return "", errors.New("merge-base unavailable")
}

func (s *stubGit) FetchCommit(ctx context.Context, repoRoot, sha string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fetched = append(s.fetched, repoRoot+"::"+sha)
	return nil
}

func (s *stubGit) FetchBranch(ctx context.Context, repoRoot, branch string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fetchedBranches = append(s.fetchedBranches, repoRoot+"::"+branch)
	return nil
}

func (s *stubGit) RepoSlug(ctx context.Context, repoRoot string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.repoSlugErr != nil {
		return "", s.repoSlugErr
	}
	if slug, ok := s.repoSlugByRoot[repoRoot]; ok {
		return slug, nil
	}
	return s.repoSlug, nil
}

func (s *stubGit) ChangedFiles(ctx context.Context, repoRoot, from, to string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.changedFilesCalls++
	if s.changedFilesErr != nil {
		return nil, s.changedFilesErr
	}
	return append([]string(nil), s.changedFiles...), nil
}

func (s *stubGit) Diff(ctx context.Context, repoRoot, from, to string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.diffCalls++
	if s.diffErr != nil {
		return "", s.diffErr
	}
	return s.diffText, nil
}

type recordingGH struct {
	repo string
	info PRInfo
	err  error
}

func (g *recordingGH) PRView(ctx context.Context, pr int, repo string) (PRInfo, error) {
	g.repo = repo
	if g.err != nil {
		return PRInfo{}, g.err
	}
	out := g.info
	if out.Number == 0 {
		out.Number = pr
	}
	return out, nil
}

func newRunCtx(t *testing.T) internalio.RunContext {
	t.Helper()
	base := t.TempDir()
	runsBase := filepath.Join(base, "runs")
	worktreeBase := filepath.Join(base, "worktrees")
	require.NoError(t, os.MkdirAll(runsBase, 0o755))
	require.NoError(t, os.MkdirAll(worktreeBase, 0o755))
	runID := contracts.RunID("2026-04-21-PR42-abcdef0")
	rc, err := internalio.NewRunContext(runID, runsBase, worktreeBase)
	require.NoError(t, err)
	return rc
}

func sha256HexForStep10Test(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func writeTaskBriefGeneratorOutput(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "task-generator-output.json")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	return path
}
