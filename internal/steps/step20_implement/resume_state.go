package step20_implement

import (
	"os"
	"sync"
	"time"

	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/validation"
)

type resumeState struct {
	ExpectedBaseSHA string    `json:"expected_base_sha" validate:"required,sha1_hex"`
	StartedAt       time.Time `json:"started_at" validate:"required"`
	PID             int       `json:"pid" validate:"required,gt=0"`
	RetryCount      int       `json:"retry_count" validate:"gte=0"`
	LastHeartbeat   time.Time `json:"last_heartbeat" validate:"required"`
}

func (s resumeState) Validate() error {
	return validation.Instance().Struct(s)
}

type resumeStateStore struct {
	path  string
	mu    sync.Mutex
	state resumeState
}

func newResumeStateStore(path string, state resumeState) *resumeStateStore {
	return &resumeStateStore{
		path:  path,
		state: state,
	}
}

func loadResumeState(path string) (resumeState, bool, error) {
	state, err := internalio.ReadJSON[resumeState](path)
	if err == nil {
		return state, true, nil
	}
	if os.IsNotExist(err) {
		return resumeState{}, false, nil
	}
	return resumeState{}, false, err
}

func (s *resumeStateStore) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return internalio.WriteJSONAtomic(s.path, s.state)
}

func (s *resumeStateStore) UpdateLastHeartbeat(at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.LastHeartbeat = at
	return internalio.WriteJSONAtomic(s.path, s.state)
}
