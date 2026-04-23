package agentrunner

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type DescendantTracker struct {
	rootPID int
	stop    chan struct{}
	done    chan struct{}

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
var sessionProcessesUntilGoneList = sessionProcesses
var processGroupMembersUntilGoneList = processGroupMembers
var killProcessGroupUntilGoneSignal = KillProcessGroup
var killSessionProcessesUntilGoneKill = killPIDs

var ErrCleanupTimeout = errors.New("agentrunner: process cleanup timed out")

func StartDescendantTracker(rootPID int, interval time.Duration) *DescendantTracker {
	if rootPID <= 0 {
		return nil
	}
	if interval <= 0 {
		interval = 25 * time.Millisecond
	}
	tracker := &DescendantTracker{
		rootPID: rootPID,
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
		seen:    map[int]string{},
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
	descendants, err := processDescendants(t.rootPID, t.snapshotSeeds())
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
			startTime = ""
		}
		t.seen[pid] = startTime
	}
}

func (t *DescendantTracker) snapshotSeeds() []int {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	seeds := make([]int, 0, len(t.seen)+1)
	seeds = append(seeds, t.rootPID)
	for pid := range t.seen {
		seeds = append(seeds, pid)
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
		if !time.Now().Before(deadline) {
			return
		}
		runtime.Gosched()
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
	if tracker != nil {
		tracker.CaptureBurst(250 * time.Millisecond)
		tracker.CaptureUntilStable(500*time.Millisecond, 25*time.Millisecond)
	}
	if err := killTrackedPIDs(tracker.snapshotIdentities()); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
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
		matches, err := processIdentityMatches(lease.PID, lease.StartTime)
		if err != nil {
			return errors.Join(lastErr, err)
		}
		if !matches {
			return lastErr
		}
		if err := killProcessGroupUntilGoneSignal(lease.PGID); err != nil {
			lastErr = err
		}
		members, err := processGroupMembersUntilGoneList(lease.PGID)
		if err != nil {
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
		matches, err := processIdentityMatches(lease.PID, lease.StartTime)
		if err != nil {
			return errors.Join(lastErr, err)
		}
		if !matches {
			return lastErr
		}
		pids, err := sessionProcessesUntilGoneList(sessionID)
		if err != nil {
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

func processIdentityMatches(pid int, expectedStartTime string) (bool, error) {
	if pid <= 0 || expectedStartTime == "" {
		return false, nil
	}
	currentStartTime, err := lookupProcessStartTime(pid)
	switch {
	case errors.Is(err, syscall.ESRCH):
		return false, nil
	case err != nil:
		return false, err
	default:
		return currentStartTime == expectedStartTime, nil
	}
}

func KillSessionProcesses(sessionID int) error {
	pids, err := sessionProcesses(sessionID)
	if err != nil {
		return err
	}
	return killPIDs(pids)
}

func KillSessionProcessesUntilGone(sessionID int, maxWait, interval time.Duration) error {
	if sessionID <= 0 {
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
		pids, err := sessionProcessesUntilGoneList(sessionID)
		if err != nil {
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

func processDescendants(rootPID int, seeds []int) ([]int, error) {
	if rootPID <= 0 {
		return nil, nil
	}
	psOutput, err := exec.Command("ps", "-axo", "pid=,ppid=").Output()
	if err != nil {
		return nil, err
	}
	children := map[int][]int{}
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
		children[ppid] = append(children[ppid], pid)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	visited := map[int]struct{}{}
	queue := make([]int, 0, len(seeds)+1)
	for _, seed := range seeds {
		if seed <= 0 {
			continue
		}
		if _, ok := visited[seed]; ok {
			continue
		}
		visited[seed] = struct{}{}
		queue = append(queue, seed)
	}
	if _, ok := visited[rootPID]; !ok {
		visited[rootPID] = struct{}{}
		queue = append(queue, rootPID)
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

func KillProcessGroupUntilGone(pgid int, maxWait, interval time.Duration) error {
	if pgid <= 0 {
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
		if err := killProcessGroupUntilGoneSignal(pgid); err != nil {
			lastErr = err
		}
		members, err := processGroupMembersUntilGoneList(pgid)
		if err != nil {
			return errors.Join(lastErr, err)
		}
		if len(members) == 0 {
			return lastErr
		}
		if !cleanupNow().Before(deadline) {
			timeoutErr := fmt.Errorf("%w: pgid=%d survivors=%v", ErrCleanupTimeout, pgid, members)
			return errors.Join(timeoutErr, lastErr)
		}
		cleanupSleep(interval)
	}
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
		if id.startTime != "" {
			currentStartTime, err := lookupProcessStartTime(id.pid)
			switch {
			case errors.Is(err, syscall.ESRCH):
				continue
			case err != nil:
				errs = append(errs, err)
				continue
			case currentStartTime != id.startTime:
				continue
			}
		}
		if err := killPIDSignal(id.pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func processStartTime(pid int) (string, error) {
	if pid <= 0 {
		return "", syscall.ESRCH
	}
	output, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "lstart=").Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", syscall.ESRCH
		}
		return "", err
	}
	startTime := strings.TrimSpace(string(output))
	if startTime == "" {
		return "", syscall.ESRCH
	}
	return startTime, nil
}

func LookupProcessStartTime(pid int) (string, error) {
	return lookupProcessStartTime(pid)
}
