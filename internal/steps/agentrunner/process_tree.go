package agentrunner

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type DescendantTracker struct {
	rootPID       int
	rootStartTime string
	stop          chan struct{}
	done          chan struct{}

	mu   sync.Mutex
	seen map[int]string
}

type processIdentity struct {
	pid       int
	startTime string
}

var lookupProcessStartTime = processStartTime
var killPIDSignal = func(pid int, sig syscall.Signal) error {
	return syscall.Kill(pid, sig)
}
var cleanupNow = time.Now
var cleanupSleep = time.Sleep
var processParentList = processParents
var sessionProcessesUntilGoneList = sessionProcesses
var processGroupMembersUntilGoneList = processGroupMembers
var killProcessGroupUntilGoneSignal = KillProcessGroup
var killSessionProcessesUntilGoneKill = killPIDs

var (
	ErrCleanupTimeout               = errors.New("agentrunner: process cleanup timed out")
	ErrCleanupInspectionUnavailable = errors.New("agentrunner: process inspection unavailable during active lease cleanup")
	ErrCleanupOwnershipUnverified   = errors.New("agentrunner: process cleanup ownership could not be verified")
)

const processInspectionUnavailableStartTimePrefix = "process-inspection-unavailable:"

type processIdentityStatus int

const (
	processIdentityMismatch processIdentityStatus = iota
	processIdentityMatch
	processIdentityGone
)

func StartDescendantTracker(rootPID int, interval time.Duration) *DescendantTracker {
	if rootPID <= 0 {
		return nil
	}
	if interval <= 0 {
		interval = 25 * time.Millisecond
	}
	rootStartTime, err := lookupProcessStartTime(rootPID)
	if err != nil {
		if isProcessInspectionUnavailable(err) {
			rootStartTime = processInspectionUnavailableStartTime(rootPID)
		} else {
			rootStartTime = ""
		}
	}
	tracker := &DescendantTracker{
		rootPID:       rootPID,
		rootStartTime: rootStartTime,
		stop:          make(chan struct{}),
		done:          make(chan struct{}),
		seen:          map[int]string{},
	}
	go tracker.run(interval)
	return tracker
}

func (t *DescendantTracker) run(interval time.Duration) {
	defer close(t.done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		t.capture()
		select {
		case <-ticker.C:
		case <-t.stop:
			t.capture()
			return
		}
	}
}

func (t *DescendantTracker) capture() {
	if t == nil || t.rootPID <= 0 {
		return
	}
	descendants, err := processDescendants(t.rootPID, t.snapshotSeedIdentities())
	if err != nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, pid := range descendants {
		if pid <= 0 {
			continue
		}
		if _, ok := t.seen[pid]; ok {
			continue
		}
		startTime, err := lookupProcessStartTime(pid)
		if err != nil {
			if isProcessInspectionUnavailable(err) {
				startTime = processInspectionUnavailableStartTime(pid)
			} else {
				continue
			}
		}
		if startTime == "" {
			continue
		}
		t.seen[pid] = startTime
	}
}

func (t *DescendantTracker) snapshotSeedIdentities() []processIdentity {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	seeds := make([]processIdentity, 0, len(t.seen)+1)
	if t.rootPID > 0 && t.rootStartTime != "" {
		seeds = append(seeds, processIdentity{pid: t.rootPID, startTime: t.rootStartTime})
	}
	for pid, startTime := range t.seen {
		if startTime == "" {
			continue
		}
		seeds = append(seeds, processIdentity{pid: pid, startTime: startTime})
	}
	return seeds
}

func (t *DescendantTracker) Stop() {
	if t == nil {
		return
	}
	close(t.stop)
	<-t.done
}

func (t *DescendantTracker) Snapshot() []int {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]int, 0, len(t.seen))
	for pid := range t.seen {
		out = append(out, pid)
	}
	return out
}

func (t *DescendantTracker) snapshotIdentities() []processIdentity {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]processIdentity, 0, len(t.seen))
	for pid, startTime := range t.seen {
		out = append(out, processIdentity{pid: pid, startTime: startTime})
	}
	return out
}

func (t *DescendantTracker) CaptureBurst(window time.Duration) {
	if t == nil {
		return
	}
	if window <= 0 {
		t.capture()
		return
	}
	deadline := time.Now().Add(window)
	for {
		t.capture()
		now := time.Now()
		if !now.Before(deadline) {
			return
		}
		sleepFor := deadline.Sub(now)
		if sleepFor > time.Millisecond {
			sleepFor = time.Millisecond
		}
		time.Sleep(sleepFor)
	}
}

func (t *DescendantTracker) CaptureUntilStable(maxWait, interval time.Duration) {
	if t == nil {
		return
	}
	if interval <= 0 {
		interval = 25 * time.Millisecond
	}
	if maxWait <= 0 {
		maxWait = 500 * time.Millisecond
	}
	deadline := time.Now().Add(maxWait)
	var last []int
	stableSamples := 0
	for {
		t.capture()
		current := t.Snapshot()
		sort.Ints(current)
		if samePIDSet(last, current) {
			stableSamples++
			if stableSamples >= 2 {
				return
			}
		} else {
			stableSamples = 0
			last = append(last[:0], current...)
		}
		if !time.Now().Before(deadline) {
			return
		}
		time.Sleep(interval)
	}
}

func CleanupProcessTree(lease ProcessLease, sessionID int, tracker *DescendantTracker) error {
	errs := make([]error, 0, 3)
	if tracker != nil {
		tracker.CaptureUntilStable(500*time.Millisecond, 25*time.Millisecond)
	}
	if err := killProcessGroupUntilGoneOwned(lease, 500*time.Millisecond, 25*time.Millisecond); err != nil {
		errs = append(errs, err)
	}
	if err := killSessionProcessesUntilGoneOwned(lease, sessionID, 500*time.Millisecond, 25*time.Millisecond); err != nil {
		errs = append(errs, err)
	}
	if cleanupInspectionUnavailable(errs) {
		return errors.Join(errs...)
	}
	if tracker != nil {
		tracker.CaptureBurst(250 * time.Millisecond)
		tracker.CaptureUntilStable(500*time.Millisecond, 25*time.Millisecond)
	}
	if err := killTrackedPIDs(tracker.snapshotIdentities()); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func cleanupInspectionUnavailable(errs []error) bool {
	for _, err := range errs {
		if errors.Is(err, ErrCleanupInspectionUnavailable) {
			return true
		}
	}
	return false
}

func killProcessGroupUntilGoneOwned(lease ProcessLease, maxWait, interval time.Duration) error {
	if lease.PGID <= 0 {
		return nil
	}
	if lease.PID <= 0 || lease.StartTime == "" {
		return nil
	}
	if interval <= 0 {
		interval = 25 * time.Millisecond
	}
	if maxWait <= 0 {
		maxWait = 500 * time.Millisecond
	}
	deadline := cleanupNow().Add(maxWait)
	var lastErr error
	for {
		status, err := inspectProcessIdentity(lease.PID, lease.StartTime)
		if err != nil {
			if isProcessInspectionUnavailable(err) {
				return errors.Join(lastErr, fmt.Errorf("%w: pid=%d pgid=%d", ErrCleanupInspectionUnavailable, lease.PID, lease.PGID))
			}
			return errors.Join(lastErr, err)
		}
		if status == processIdentityMismatch {
			return lastErr
		}
		if status == processIdentityGone && lease.PGID != lease.PID {
			return errors.Join(lastErr, fmt.Errorf("%w: pid=%d pgid=%d", ErrCleanupOwnershipUnverified, lease.PID, lease.PGID))
		}
		if err := killProcessGroupUntilGoneSignal(lease.PGID); err != nil {
			lastErr = err
		}
		members, err := processGroupMembersUntilGoneList(lease.PGID)
		if err != nil {
			if isProcessInspectionUnavailable(err) {
				if status == processIdentityGone {
					return lastErr
				}
				return errors.Join(lastErr, fmt.Errorf("%w: pgid=%d", ErrCleanupInspectionUnavailable, lease.PGID))
			}
			return errors.Join(lastErr, err)
		}
		if len(members) == 0 {
			return lastErr
		}
		if !cleanupNow().Before(deadline) {
			timeoutErr := fmt.Errorf("%w: pgid=%d survivors=%v", ErrCleanupTimeout, lease.PGID, members)
			return errors.Join(timeoutErr, lastErr)
		}
		cleanupSleep(interval)
	}
}

func killSessionProcessesUntilGoneOwned(lease ProcessLease, sessionID int, maxWait, interval time.Duration) error {
	if sessionID <= 0 {
		return nil
	}
	if lease.PID <= 0 || lease.StartTime == "" {
		return nil
	}
	if interval <= 0 {
		interval = 25 * time.Millisecond
	}
	if maxWait <= 0 {
		maxWait = 500 * time.Millisecond
	}
	deadline := cleanupNow().Add(maxWait)
	var lastErr error
	for {
		status, err := inspectProcessIdentity(lease.PID, lease.StartTime)
		if err != nil {
			if isProcessInspectionUnavailable(err) {
				return errors.Join(lastErr, fmt.Errorf("%w: pid=%d session_id=%d", ErrCleanupInspectionUnavailable, lease.PID, sessionID))
			}
			return errors.Join(lastErr, err)
		}
		if status == processIdentityMismatch {
			return lastErr
		}
		if status == processIdentityGone && sessionID != lease.PID {
			return errors.Join(lastErr, fmt.Errorf("%w: pid=%d session_id=%d", ErrCleanupOwnershipUnverified, lease.PID, sessionID))
		}
		pids, err := sessionProcessesUntilGoneList(sessionID)
		if err != nil {
			if isProcessInspectionUnavailable(err) {
				if status == processIdentityGone {
					return lastErr
				}
				return errors.Join(lastErr, fmt.Errorf("%w: session_id=%d", ErrCleanupInspectionUnavailable, sessionID))
			}
			return errors.Join(lastErr, err)
		}
		if len(pids) == 0 {
			return lastErr
		}
		if err := killSessionProcessesUntilGoneKill(pids); err != nil {
			lastErr = err
		}
		if !cleanupNow().Before(deadline) {
			timeoutErr := fmt.Errorf("%w: session_id=%d survivors=%v", ErrCleanupTimeout, sessionID, pids)
			return errors.Join(timeoutErr, lastErr)
		}
		cleanupSleep(interval)
	}
}

func inspectProcessIdentity(pid int, expectedStartTime string) (processIdentityStatus, error) {
	if pid <= 0 || expectedStartTime == "" {
		return processIdentityMismatch, nil
	}
	if isProcessInspectionUnavailableStartTime(expectedStartTime) {
		if err := killPIDSignal(pid, 0); errors.Is(err, syscall.ESRCH) {
			return processIdentityGone, nil
		} else if err != nil {
			return processIdentityMismatch, err
		}
		return processIdentityMismatch, ErrCleanupInspectionUnavailable
	}
	currentStartTime, err := lookupProcessStartTime(pid)
	switch {
	case errors.Is(err, syscall.ESRCH):
		return processIdentityGone, nil
	case err != nil:
		return processIdentityMismatch, err
	default:
		if currentStartTime == expectedStartTime {
			return processIdentityMatch, nil
		}
		return processIdentityMismatch, nil
	}
}

func sessionProcesses(sessionID int) ([]int, error) {
	if sessionID <= 0 {
		return nil, nil
	}
	psOutput, err := exec.Command("ps", "-axo", "pid=,sess=").Output()
	if err != nil {
		return nil, err
	}
	pids := make([]int, 0, 8)
	scanner := bufio.NewScanner(bytes.NewReader(psOutput))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			continue
		}
		pid, errPID := strconv.Atoi(fields[0])
		sess, errSess := strconv.Atoi(fields[1])
		if errPID != nil || errSess != nil {
			continue
		}
		if sess == sessionID && pid > 0 {
			pids = append(pids, pid)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return pids, nil
}

func processDescendants(rootPID int, seeds []processIdentity) ([]int, error) {
	if rootPID <= 0 {
		return nil, nil
	}
	processes, err := processParentList()
	if err != nil {
		return nil, err
	}
	children := map[int][]int{}
	for _, process := range processes {
		if process.pid <= 0 || process.ppid <= 0 {
			continue
		}
		children[process.ppid] = append(children[process.ppid], process.pid)
	}

	visited := map[int]struct{}{}
	queue := make([]int, 0, len(seeds)+1)
	for _, seed := range seeds {
		if seed.pid <= 0 {
			continue
		}
		if _, ok := visited[seed.pid]; ok {
			continue
		}
		status, err := inspectProcessIdentity(seed.pid, seed.startTime)
		if err != nil || status != processIdentityMatch {
			continue
		}
		visited[seed.pid] = struct{}{}
		queue = append(queue, seed.pid)
	}
	out := make([]int, 0, 8)
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		for _, child := range children[pid] {
			if _, ok := visited[child]; ok {
				continue
			}
			visited[child] = struct{}{}
			out = append(out, child)
			queue = append(queue, child)
		}
	}
	return out, nil
}

type processParent struct {
	pid  int
	ppid int
}

func processParents() ([]processParent, error) {
	psOutput, err := exec.Command("ps", "-axo", "pid=,ppid=").Output()
	if err != nil {
		return nil, err
	}
	processes := make([]processParent, 0, 64)
	scanner := bufio.NewScanner(bytes.NewReader(psOutput))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			continue
		}
		pid, errPID := strconv.Atoi(fields[0])
		ppid, errPPID := strconv.Atoi(fields[1])
		if errPID != nil || errPPID != nil {
			continue
		}
		processes = append(processes, processParent{pid: pid, ppid: ppid})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return processes, nil
}

func processGroupMembers(pgid int) ([]int, error) {
	if pgid <= 0 {
		return nil, nil
	}
	psOutput, err := exec.Command("ps", "-axo", "pid=,pgid=").Output()
	if err != nil {
		return nil, err
	}
	pids := make([]int, 0, 8)
	scanner := bufio.NewScanner(bytes.NewReader(psOutput))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			continue
		}
		pid, errPID := strconv.Atoi(fields[0])
		group, errGroup := strconv.Atoi(fields[1])
		if errPID != nil || errGroup != nil {
			continue
		}
		if group == pgid && pid > 0 {
			pids = append(pids, pid)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return pids, nil
}

func samePIDSet(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func killPIDs(pids []int) error {
	seen := map[int]struct{}{}
	errs := make([]error, 0, len(pids))
	for _, pid := range pids {
		if pid <= 0 {
			continue
		}
		if _, ok := seen[pid]; ok {
			continue
		}
		seen[pid] = struct{}{}
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func killTrackedPIDs(ids []processIdentity) error {
	seen := map[int]struct{}{}
	errs := make([]error, 0, len(ids))
	for _, id := range ids {
		if id.pid <= 0 {
			continue
		}
		if _, ok := seen[id.pid]; ok {
			continue
		}
		seen[id.pid] = struct{}{}
		if id.startTime == "" || isProcessInspectionUnavailableStartTime(id.startTime) {
			if err := killPIDSignal(id.pid, 0); errors.Is(err, syscall.ESRCH) {
				continue
			} else if err != nil {
				errs = append(errs, err)
				continue
			}
			errs = append(errs, fmt.Errorf("%w: tracked pid=%d", ErrCleanupInspectionUnavailable, id.pid))
			continue
		}
		currentStartTime, err := lookupProcessStartTime(id.pid)
		switch {
		case errors.Is(err, syscall.ESRCH):
			continue
		case isProcessInspectionUnavailable(err):
			errs = append(errs, fmt.Errorf("%w: tracked pid=%d", ErrCleanupInspectionUnavailable, id.pid))
			continue
		case err != nil:
			errs = append(errs, err)
			continue
		case currentStartTime != id.startTime:
			continue
		}
		if err := killPIDSignal(id.pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func isProcessInspectionUnavailable(err error) bool {
	return errors.Is(err, exec.ErrNotFound) ||
		errors.Is(err, os.ErrNotExist) ||
		errors.Is(err, os.ErrPermission)
}

func processInspectionUnavailableStartTime(pid int) string {
	return processInspectionUnavailableStartTimePrefix + strconv.Itoa(pid)
}

func isProcessInspectionUnavailableStartTime(startTime string) bool {
	return strings.HasPrefix(startTime, processInspectionUnavailableStartTimePrefix)
}

func LookupProcessStartTime(pid int) (string, error) {
	return lookupProcessStartTime(pid)
}
