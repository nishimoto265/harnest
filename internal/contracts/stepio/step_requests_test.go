package stepio

import (
	"testing"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildTaskPackage: step20 / step50 の Validate 動作確認用の最小 TaskPackage。
// worktrees[0..2] = pass1 (a1/a2/a3), [3..5] = pass2 (a1/a2/a3).
func buildTaskPackage() contracts.TaskPackage {
	pkg := contracts.TaskPackage{
		SchemaVersion:           "1",
		RunID:                   "2026-04-20-PR42-abcdef0",
		PR:                      42,
		Title:                   "fix: example",
		BaseSHA:                 "1111111111111111111111111111111111111111",
		BestBranch:              "auto-improve/best",
		ReconstructedTaskPrompt: "hello",
		Worktrees:               make([]contracts.WorktreeAllocation, 6),
		CreatedAt:               time.Now(),
	}
	agents := []contracts.AgentID{"a1", "a2", "a3", "a1", "a2", "a3"}
	for i := range pkg.Worktrees {
		pass := 1
		if i >= 3 {
			pass = 2
		}
		pkg.Worktrees[i] = contracts.WorktreeAllocation{
			Agent:   agents[i],
			Pass:    pass,
			Path:    "/tmp/wt",
			Branch:  "b",
			BaseSHA: "1111111111111111111111111111111111111111",
			HeadSHA: "1111111111111111111111111111111111111111",
		}
	}
	return pkg
}

func TestStep20Request_Validate_Valid(t *testing.T) {
	r := Step20Request{
		TaskPackage:    buildTaskPackage(),
		Agents:         []contracts.AgentID{"a1", "a2", "a3"},
		TimeoutSeconds: 600,
	}
	assert.NoError(t, r.Validate())
}

func TestStep20Request_Validate_Reject_DuplicateAgents(t *testing.T) {
	r := Step20Request{
		TaskPackage:    buildTaskPackage(),
		Agents:         []contracts.AgentID{"a1", "a1", "a2"},
		TimeoutSeconds: 600,
	}
	assert.Error(t, r.Validate())
}

func TestStep20Request_Validate_Reject_EmptyAgents(t *testing.T) {
	r := Step20Request{
		TaskPackage:    buildTaskPackage(),
		Agents:         []contracts.AgentID{},
		TimeoutSeconds: 600,
	}
	assert.Error(t, r.Validate())
}

func TestStep20Request_Validate_Reject_ZeroTimeout(t *testing.T) {
	r := Step20Request{
		TaskPackage:    buildTaskPackage(),
		Agents:         []contracts.AgentID{"a1", "a2", "a3"},
		TimeoutSeconds: 0,
	}
	assert.Error(t, r.Validate())
}

func TestStep20Request_Validate_Reject_BadAgentID(t *testing.T) {
	r := Step20Request{
		TaskPackage:    buildTaskPackage(),
		Agents:         []contracts.AgentID{"a1", "bogus", "a3"},
		TimeoutSeconds: 600,
	}
	assert.Error(t, r.Validate())
}

func TestStep20Request_Validate_Reject_PassMismatch(t *testing.T) {
	// a4 は worktrees(pass=1) に存在しない → mismatch
	r := Step20Request{
		TaskPackage:    buildTaskPackage(),
		Agents:         []contracts.AgentID{"a1", "a2", "a4"},
		TimeoutSeconds: 600,
	}
	err := r.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStep20AgentPassMismatch)
}

func TestStep20Request_Validate_Reject_PassSubsetIncomplete(t *testing.T) {
	// pass=1 の agent set は {a1,a2,a3} なので a1 だけでは subset 不一致
	r := Step20Request{
		TaskPackage:    buildTaskPackage(),
		Agents:         []contracts.AgentID{"a1"},
		TimeoutSeconds: 600,
	}
	err := r.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStep20AgentPassMismatch)
}

func TestStep50Request_Validate_Valid(t *testing.T) {
	r := Step50Request{
		TaskPackage:      buildTaskPackage(),
		Agents:           []contracts.AgentID{"a1", "a2", "a3"},
		TimeoutSeconds:   600,
		CandidateRuleIDs: []string{"r-1"},
	}
	assert.NoError(t, r.Validate())
}

func TestStep50Request_Validate_Reject_DuplicateAgents(t *testing.T) {
	r := Step50Request{
		TaskPackage:    buildTaskPackage(),
		Agents:         []contracts.AgentID{"a1", "a1"},
		TimeoutSeconds: 600,
	}
	assert.Error(t, r.Validate())
}

func TestStep50Request_Validate_Reject_ZeroTimeout(t *testing.T) {
	r := Step50Request{
		TaskPackage:    buildTaskPackage(),
		Agents:         []contracts.AgentID{"a1", "a2", "a3"},
		TimeoutSeconds: 0,
	}
	assert.Error(t, r.Validate())
}

func TestStep50Request_Validate_Reject_PassMismatch(t *testing.T) {
	// pass=2 の agent set は {a1,a2,a3}. a4 は居ない → mismatch
	r := Step50Request{
		TaskPackage:    buildTaskPackage(),
		Agents:         []contracts.AgentID{"a1", "a2", "a4"},
		TimeoutSeconds: 600,
	}
	err := r.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStep50AgentPassMismatch)
}
