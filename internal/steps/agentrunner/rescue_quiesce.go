package agentrunner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var (
	ErrRescueLeaseQuiesceEnumerate = errors.New("agentrunner: rescue lease quiesce enumerate worktree processes")
	ErrRescueLeaseQuiesceTimedOut  = errors.New("agentrunner: rescue lease quiesce timed out while worktree remained busy")
)

type RescueLeaseState struct {
	PID             int
	PGID            int
	LeaderStartTime string
}

type WorktreeProcessIDsOptions struct {
	LookPath       func(string) (string, error)
	CommandContext func(context.Context, string, ...string) *exec.Cmd
}

type RescueLeaseQuiesceOptions struct {
	KillProcessGroupUntilGone func(int, time.Duration, time.Duration) error
	WorktreeProcessIDs        func(context.Context, string) ([]int, error)
	KillPID                   func(int, syscall.Signal) error
	Sleep                     func(time.Duration)
	Now                       func() time.Time
	PIDAlive                  func(int) bool
	LookupProcessStartTime    func(int) (string, error)
	MaxWait                   time.Duration
	Interval                  time.Duration
	SelfPID                   int
}

type RescueLeaseQuiesceEnumerateError struct {
	Err error
}

func (e *RescueLeaseQuiesceEnumerateError) Error() string {
	if e == nil || e.Err == nil {
		return ErrRescueLeaseQuiesceEnumerate.Error()
	}
	return e.Err.Error()
}

func (e *RescueLeaseQuiesceEnumerateError) Unwrap() error {
	return ErrRescueLeaseQuiesceEnumerate
}

func EnsureRescueLeaseQuiesced(ctx context.Context, worktreePath string, state RescueLeaseState, opts RescueLeaseQuiesceOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if opts.KillProcessGroupUntilGone == nil {
		opts.KillProcessGroupUntilGone = KillProcessGroupUntilGone
	}
	if opts.WorktreeProcessIDs == nil {
		opts.WorktreeProcessIDs = func(ctx context.Context, worktreePath string) ([]int, error) {
			return WorktreeProcessIDs(ctx, worktreePath, WorktreeProcessIDsOptions{})
		}
	}
	if opts.KillPID == nil {
		opts.KillPID = syscall.Kill
	}
	if opts.Sleep == nil {
		opts.Sleep = time.Sleep
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.PIDAlive == nil {
		opts.PIDAlive = func(pid int) bool {
			err := syscall.Kill(pid, 0)
			return err == nil || !errors.Is(err, syscall.ESRCH)
		}
	}
	if opts.LookupProcessStartTime == nil {
		opts.LookupProcessStartTime = LookupProcessStartTime
	}
	if opts.MaxWait <= 0 {
		opts.MaxWait = 750 * time.Millisecond
	}
	if opts.Interval < 0 {
		opts.Interval = 0
	}
	if opts.SelfPID == 0 {
		opts.SelfPID = os.Getpid()
	}

	if ShouldKillSavedProcessGroup(state, opts.PIDAlive, opts.LookupProcessStartTime) {
		_ = opts.KillProcessGroupUntilGone(state.PGID, 500*time.Millisecond, 25*time.Millisecond)
	}

	deadline := opts.Now().Add(opts.MaxWait)
	emptyChecks := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		pids, err := opts.WorktreeProcessIDs(ctx, worktreePath)
		if err != nil {
			return &RescueLeaseQuiesceEnumerateError{Err: err}
		}
		activePIDs := make([]int, 0, len(pids))
		for _, pid := range pids {
			if pid <= 0 || pid == opts.SelfPID {
				continue
			}
			activePIDs = append(activePIDs, pid)
		}
		if len(activePIDs) == 0 {
			emptyChecks++
			if emptyChecks >= 2 {
				return nil
			}
		} else {
			emptyChecks = 0
			for _, pid := range activePIDs {
				_ = opts.KillPID(pid, syscall.SIGKILL)
			}
		}
		if !opts.Now().Before(deadline) {
			return ErrRescueLeaseQuiesceTimedOut
		}
		opts.Sleep(opts.Interval)
	}
}

func WorktreeProcessIDs(ctx context.Context, worktreePath string, opts WorktreeProcessIDsOptions) ([]int, error) {
	lookPath := opts.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	commandContext := opts.CommandContext
	if commandContext == nil {
		commandContext = exec.CommandContext
	}
	lsofPath, err := lookPath("lsof")
	if err != nil {
		return nil, fmt.Errorf("lsof is required for rescue quiesce: %w", err)
	}
	cmd := commandContext(ctx, lsofPath, "-t", "+D", worktreePath)
	output, err := cmd.Output()
	if err == nil {
		return ParsePIDList(string(output)), nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return nil, nil
	}
	return nil, fmt.Errorf("enumerate worktree processes with lsof: %w", err)
}

func ShouldKillSavedProcessGroup(state RescueLeaseState, pidAlive func(int) bool, lookupProcessStartTime func(int) (string, error)) bool {
	if state.PGID <= 0 || state.PID <= 0 || state.LeaderStartTime == "" {
		return false
	}
	if pidAlive != nil && !pidAlive(state.PID) {
		return false
	}
	if lookupProcessStartTime == nil {
		lookupProcessStartTime = LookupProcessStartTime
	}
	startTime, err := lookupProcessStartTime(state.PID)
	if err != nil {
		return false
	}
	return startTime == state.LeaderStartTime
}

func ParsePIDList(output string) []int {
	seen := make(map[int]struct{})
	pids := make([]int, 0)
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pid, err := strconv.Atoi(line)
		if err != nil || pid <= 0 {
			continue
		}
		if _, ok := seen[pid]; ok {
			continue
		}
		seen[pid] = struct{}{}
		pids = append(pids, pid)
	}
	return pids
}
