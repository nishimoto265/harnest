package agentrunner

import (
	"bufio"
	"bytes"
	"errors"
	"os/exec"
	"runtime"
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
	seen map[int]struct{}
}

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
		seen:    map[int]struct{}{},
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
	descendants, err := processDescendants(t.rootPID)
	if err != nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, pid := range descendants {
		if pid > 0 {
			t.seen[pid] = struct{}{}
		}
	}
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

func CleanupProcessTree(lease ProcessLease, sessionID int, tracker *DescendantTracker) error {
	errs := make([]error, 0, 3)
	if err := KillProcessGroup(lease.PGID); err != nil {
		errs = append(errs, err)
	}
	if err := KillSessionProcesses(sessionID); err != nil {
		errs = append(errs, err)
	}
	if err := killPIDs(tracker.Snapshot()); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func KillSessionProcesses(sessionID int) error {
	if sessionID <= 0 {
		return nil
	}
	psOutput, err := exec.Command("ps", "-axo", "pid=,sess=").Output()
	if err != nil {
		return err
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
		return err
	}
	return killPIDs(pids)
}

func processDescendants(rootPID int) ([]int, error) {
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

	out := make([]int, 0, 8)
	queue := append([]int(nil), children[rootPID]...)
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		out = append(out, pid)
		queue = append(queue, children[pid]...)
	}
	return out, nil
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
