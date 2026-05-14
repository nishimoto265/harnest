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

	"github.com/nishimoto265/harnest/internal/processenv"
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
	CommandContext func(context.Context, string, ...string) (*exec.Cmd, error)
}

type RescueLeaseQuiesceOptions struct {
	// KillLeasedProcessGroup kills the saved lease's process group using full
	// lease identity (pid + start time) on every signal attempt so a recycled
	// PGID owned by an unrelated process group is never targeted. When nil a
	// default implementation is supplied that validates identity via
	// PIDAlive + LookupProcessStartTime and verifies the saved PGID is still
	// owned by the saved PID before each SIGKILL.
	KillLeasedProcessGroup func(context.Context, RescueLeaseState, RescueLeaseQuiesceOptions) error
	WorktreeProcessIDs     func(context.Context, string) ([]int, error)
	KillPID                func(int, syscall.Signal) error
	Sleep                  func(time.Duration)
	Now                    func() time.Time
	PIDAlive               func(int) bool
	LookupProcessStartTime func(int) (string, error)
	LookupProcessGroupID   func(int) (int, error)
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
	if opts.LookupProcessGroupID == nil {
		opts.LookupProcessGroupID = syscall.Getpgid
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

	shouldKill, err := shouldKillSavedProcessGroup(state, opts.PIDAlive, opts.LookupProcessStartTime)
	if err != nil {
		return err
	}
	if shouldKill {
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
		remaining := deadline.Sub(opts.Now())
		if remaining <= 0 {
			return ErrRescueLeaseQuiesceTimedOut
		}
		callCtx, cancel := context.WithTimeout(ctx, remaining)
		pids, err := opts.WorktreeProcessIDs(callCtx, worktreePath)
		cancel()
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
				ownerAlive, err := savedLeaseOwnerAlive(state, opts.PIDAlive, opts.LookupProcessStartTime)
				if err != nil {
					return err
				}
				if ownerAlive {
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
		lookPath = processenv.TrustedLookPath
	}
	commandContext := opts.CommandContext
	if commandContext == nil {
		commandContext = processenv.TrustedCommandContext
	}
	lsofPath, err := lookPath("lsof")
	if err != nil {
		return nil, fmt.Errorf("lsof is required for rescue quiesce: %w", err)
	}
	cmd, err := commandContext(ctx, lsofPath, "-t", "+D", worktreePath)
	if err != nil {
		return nil, fmt.Errorf("lsof is required for rescue quiesce: %w", err)
	}
	cmd.Env = processenv.SanitizeForLocalExec()
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
	lookupProcessGroupID := opts.LookupProcessGroupID
	if lookupProcessGroupID == nil {
		lookupProcessGroupID = syscall.Getpgid
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
		ownerAlive, err := savedLeaseOwnerAlive(state, opts.PIDAlive, opts.LookupProcessStartTime)
		if err != nil {
			return errors.Join(lastErr, err)
		}
		if !ownerAlive {
			return lastErr
		}
		if err := verifySavedProcessGroupOwner(state, lookupProcessGroupID); err != nil {
			return errors.Join(lastErr, err)
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
	kill, err := shouldKillSavedProcessGroup(state, pidAlive, lookupProcessStartTime)
	return err == nil && kill
}

func shouldKillSavedProcessGroup(state RescueLeaseState, pidAlive func(int) bool, lookupProcessStartTime func(int) (string, error)) (bool, error) {
	if state.PGID <= 0 || state.PID <= 0 || state.LeaderStartTime == "" {
		return false, nil
	}
	if pidAlive != nil && !pidAlive(state.PID) {
		return false, nil
	}
	if isProcessInspectionUnavailableStartTime(state.LeaderStartTime) {
		return false, fmt.Errorf("%w: saved lease pid=%d", ErrCleanupInspectionUnavailable, state.PID)
	}
	if lookupProcessStartTime == nil {
		lookupProcessStartTime = LookupProcessStartTime
	}
	startTime, err := lookupProcessStartTime(state.PID)
	if err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return false, nil
		}
		if isProcessInspectionUnavailable(err) {
			return false, fmt.Errorf("%w: saved lease pid=%d", ErrCleanupInspectionUnavailable, state.PID)
		}
		return false, err
	}
	if isProcessInspectionUnavailableStartTime(startTime) {
		return false, fmt.Errorf("%w: saved lease pid=%d", ErrCleanupInspectionUnavailable, state.PID)
	}
	return startTime == state.LeaderStartTime, nil
}

func savedLeaseOwnerAlive(state RescueLeaseState, pidAlive func(int) bool, lookupProcessStartTime func(int) (string, error)) (bool, error) {
	if state.PID <= 0 || state.LeaderStartTime == "" {
		return false, nil
	}
	if pidAlive != nil && !pidAlive(state.PID) {
		return false, nil
	}
	if isProcessInspectionUnavailableStartTime(state.LeaderStartTime) {
		return false, fmt.Errorf("%w: saved lease pid=%d", ErrCleanupInspectionUnavailable, state.PID)
	}
	if lookupProcessStartTime == nil {
		lookupProcessStartTime = LookupProcessStartTime
	}
	startTime, err := lookupProcessStartTime(state.PID)
	if err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return false, nil
		}
		if isProcessInspectionUnavailable(err) {
			return false, fmt.Errorf("%w: saved lease pid=%d", ErrCleanupInspectionUnavailable, state.PID)
		}
		return false, err
	}
	if isProcessInspectionUnavailableStartTime(startTime) {
		return false, fmt.Errorf("%w: saved lease pid=%d", ErrCleanupInspectionUnavailable, state.PID)
	}
	return startTime == state.LeaderStartTime, nil
}

func verifySavedProcessGroupOwner(state RescueLeaseState, lookupProcessGroupID func(int) (int, error)) error {
	if state.PID <= 0 || state.PGID <= 0 {
		return nil
	}
	if lookupProcessGroupID == nil {
		lookupProcessGroupID = syscall.Getpgid
	}
	pgid, err := lookupProcessGroupID(state.PID)
	if err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if isProcessInspectionUnavailable(err) {
			return fmt.Errorf("%w: saved lease pid=%d pgid=%d", ErrCleanupInspectionUnavailable, state.PID, state.PGID)
		}
		return err
	}
	if pgid != state.PGID {
		return fmt.Errorf("%w: saved lease pid=%d pgid=%d actual_pgid=%d", ErrCleanupOwnershipUnverified, state.PID, state.PGID, pgid)
	}
	return nil
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
