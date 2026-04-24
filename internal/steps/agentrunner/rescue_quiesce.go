package agentrunner

import (
	"bytes"
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
	// KillLeasedProcessGroup kills the saved lease's process group using full
	// lease identity (pid + start time) on every signal attempt so a recycled
	// PGID owned by an unrelated process group is never targeted. When nil a
	// default implementation is supplied that validates identity via
	// PIDAlive + LookupProcessStartTime before each SIGKILL.
	KillLeasedProcessGroup func(context.Context, RescueLeaseState, RescueLeaseQuiesceOptions) error
	WorktreeProcessIDs     func(context.Context, string) ([]int, error)
	KillPID                func(int, syscall.Signal) error
	Sleep                  func(time.Duration)
	Now                    func() time.Time
	PIDAlive               func(int) bool
	LookupProcessStartTime func(int) (string, error)
	MaxWait                time.Duration
	Interval               time.Duration
	SelfPID                int
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
	if opts.KillLeasedProcessGroup == nil {
		opts.KillLeasedProcessGroup = defaultKillLeasedProcessGroup
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
		if err := opts.KillLeasedProcessGroup(ctx, state, opts); err != nil {
			if errors.Is(err, ErrCleanupTimeout) {
				return fmt.Errorf("%w: %v", ErrRescueLeaseQuiesceTimedOut, err)
			}
			return err
		}
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
				if savedLeaseOwnerAlive(state, opts.PIDAlive, opts.LookupProcessStartTime) {
					return ErrRescueLeaseQuiesceTimedOut
				}
				return nil
			}
		} else {
			emptyChecks = 0
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
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err == nil {
		return ParsePIDList(string(output)), nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		stderrText := strings.TrimSpace(stderr.String())
		if stderrText == "" || isLsofNoMatches(stderrText) {
			return ParsePIDList(string(output)), nil
		}
		return nil, fmt.Errorf("%w: lsof +D %s exited %d: %s", ErrRescueLeaseQuiesceEnumerate, worktreePath, exitErr.ExitCode(), stderrText)
	}
	return nil, fmt.Errorf("%w: enumerate worktree processes with lsof: %v", ErrRescueLeaseQuiesceEnumerate, err)
}

// defaultKillLeasedProcessGroup SIGKILLs the saved lease's process group while
// validating lease identity (pid + start time) before every signal. If the
// original lease owner has exited (identity mismatch / ESRCH) the helper
// returns early and does not signal the PGID — preventing a recycled PGID
// race where an unrelated process group would be killed.
//
// The helper polls pgid members between signals and exits cleanly once no
// members remain. If members persist past MaxWait (bounded to 500ms for the
// saved-path regardless of the broader quiesce deadline) it returns a wrapped
// ErrCleanupTimeout which the caller maps to ErrRescueLeaseQuiesceTimedOut.
func defaultKillLeasedProcessGroup(ctx context.Context, state RescueLeaseState, opts RescueLeaseQuiesceOptions) error {
	if state.PGID <= 0 || state.PID <= 0 || state.LeaderStartTime == "" {
		return nil
	}
	killPID := opts.KillPID
	if killPID == nil {
		killPID = syscall.Kill
	}
	sleep := opts.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	interval := opts.Interval
	if interval <= 0 {
		interval = 25 * time.Millisecond
	}
	// Cap the saved-group kill loop at 500ms regardless of caller's overall
	// MaxWait so the outer worktree drain loop still has budget.
	maxWait := 500 * time.Millisecond
	deadline := now().Add(maxWait)
	var lastErr error
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !savedLeaseOwnerAlive(state, opts.PIDAlive, opts.LookupProcessStartTime) {
			return lastErr
		}
		if err := killPID(-state.PGID, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			lastErr = err
		}
		members, err := processGroupMembersUntilGoneList(state.PGID)
		if err != nil {
			return errors.Join(lastErr, err)
		}
		if len(members) == 0 {
			return lastErr
		}
		if !now().Before(deadline) {
			timeoutErr := fmt.Errorf("%w: pgid=%d survivors=%v", ErrCleanupTimeout, state.PGID, members)
			return errors.Join(timeoutErr, lastErr)
		}
		sleep(interval)
	}
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

func savedLeaseOwnerAlive(state RescueLeaseState, pidAlive func(int) bool, lookupProcessStartTime func(int) (string, error)) bool {
	if state.PID <= 0 || state.LeaderStartTime == "" {
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

func isLsofNoMatches(stderr string) bool {
	stderr = strings.ToLower(strings.TrimSpace(stderr))
	return strings.Contains(stderr, "no matches found")
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
