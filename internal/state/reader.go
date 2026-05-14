package state

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/nishimoto265/harnest/internal/contracts"
	internalio "github.com/nishimoto265/harnest/internal/io"
)

func LastEventForPR(ctx internalio.RunContext, pr int) (*contracts.StateEntry, error) {
	return NewReader(ctx).LastEventForPR(pr)
}

func ReadLatestForPR(ctx internalio.RunContext, pr int) (*contracts.StateEntry, error) {
	return LastEventForPR(ctx, pr)
}

func TerminalPRSet(ctx internalio.RunContext) (map[int]struct{}, error) {
	return TerminalPRSetPath(ctx.ProcessedPath())
}

func TerminalPRSetPath(path string) (map[int]struct{}, error) {
	latest, err := latestEntriesByPRPath(path)
	if err != nil {
		return nil, err
	}
	if len(latest) == 0 {
		return nil, nil
	}
	processed := make(map[int]struct{}, len(latest))
	for pr, entry := range latest {
		if entry.Kind.IsTerminal() {
			processed[pr] = struct{}{}
		}
	}
	if len(processed) == 0 {
		return nil, nil
	}
	return processed, nil
}

func LastProcessedPR(ctx internalio.RunContext) (int, error) {
	return LastProcessedPRPath(ctx.ProcessedPath())
}

func LastProcessedPRPath(path string) (int, error) {
	processed, err := TerminalPRSetPath(path)
	if err != nil {
		return 0, err
	}
	last := 0
	for pr := range processed {
		if pr > last {
			last = pr
		}
	}
	return last, nil
}

func ScanEventsForRun(ctx internalio.RunContext, runID contracts.RunID) ([]contracts.StateEntry, error) {
	entries, err := readProcessedEntriesPath(ctx.ProcessedPath())
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, nil
	}
	events := make([]contracts.StateEntry, 0, len(entries))
	for _, entry := range entries {
		entryRunID, ok := stateEntryRunID(entry)
		if !ok || entryRunID != runID {
			continue
		}
		events = append(events, entry)
	}
	if len(events) == 0 {
		return nil, nil
	}
	return events, nil
}

func LatestRunForPR(ctx internalio.RunContext, pr int) (LatestRun, error) {
	entries, err := eventsForPRPath(ctx.ProcessedPath(), pr)
	if err != nil {
		return LatestRun{}, err
	}
	last := latestActionEntry(entries)
	result := LatestRun{
		PR:        pr,
		LastEvent: last,
		Action:    NextActionFreshStart,
	}
	if last == nil {
		return result, nil
	}
	result.Action = NextActionForEntry(last)
	if runID, ok := stateEntryRunID(*last); ok {
		result.RunID = runID
	}
	if step, ok := stateEntryStep(*last); ok {
		result.Step = step
	}
	return result, nil
}

func ResumeTargetPath(path string) ([]ResumeRequest, error) {
	entries, err := readProcessedEntriesPath(path)
	if err != nil {
		return nil, err
	}
	return ResumeTarget(entries), nil
}

func NeedsManualRecoveryRunsPath(path string) ([]LatestRun, error) {
	latest, err := latestEntriesByPRPath(path)
	if err != nil {
		return nil, err
	}
	if len(latest) == 0 {
		return nil, nil
	}
	prs := make([]int, 0, len(latest))
	for pr := range latest {
		prs = append(prs, pr)
	}
	sort.Ints(prs)
	runs := make([]LatestRun, 0, len(prs))
	for _, pr := range prs {
		entry := latest[pr]
		if NextActionForEntry(&entry) != NextActionNeedsManualRecovery {
			continue
		}
		run := LatestRun{
			PR:        pr,
			LastEvent: &entry,
			Action:    NextActionNeedsManualRecovery,
		}
		if runID, ok := stateEntryRunID(entry); ok {
			run.RunID = runID
		}
		if step, ok := stateEntryStep(entry); ok {
			run.Step = step
		}
		runs = append(runs, run)
	}
	if len(runs) == 0 {
		return nil, nil
	}
	return runs, nil
}

func (r Reader) LatestForPR(pr int) (*contracts.StateEntry, error) {
	return r.LastEventForPR(pr)
}

func (r Reader) LatestEventForPR(pr int) (*contracts.StateEntry, error) {
	return r.LastEventForPR(pr)
}

func (r Reader) LastEventForPR(pr int) (*contracts.StateEntry, error) {
	if pr <= 0 {
		return nil, fmt.Errorf("state: pr must be > 0: pr=%d", pr)
	}
	lines, err := readProcessedLines(r.path)
	if err != nil {
		return nil, err
	}
	for i := len(lines) - 1; i >= 0; i-- {
		entry, err := decodeStateLine(lines[i])
		if err != nil {
			return nil, err
		}
		entryPR, ok := stateEntryPR(entry)
		if ok && entryPR == pr {
			found := entry
			return &found, nil
		}
	}
	return nil, nil
}

type processedLine struct {
	Number int
	Offset int64
	Data   []byte
}

type processedFileSnapshot struct {
	Data    []byte
	Size    int64
	ModTime time.Time
}

func latestEntriesByPRPath(path string) (map[int]contracts.StateEntry, error) {
	entries, err := readProcessedEntriesPath(path)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, nil
	}
	grouped := make(map[int][]contracts.StateEntry)
	for _, entry := range entries {
		pr, ok := stateEntryPR(entry)
		if !ok {
			continue
		}
		grouped[pr] = append(grouped[pr], entry)
	}
	latest := make(map[int]contracts.StateEntry, len(grouped))
	for pr, entries := range grouped {
		entry := latestActionEntry(entries)
		if entry == nil {
			continue
		}
		latest[pr] = *entry
	}
	if len(latest) == 0 {
		return nil, nil
	}
	return latest, nil
}

func eventsForPRPath(path string, pr int) ([]contracts.StateEntry, error) {
	if pr <= 0 {
		return nil, fmt.Errorf("state: pr must be > 0: pr=%d", pr)
	}
	lines, err := readProcessedLines(path)
	if err != nil {
		return nil, err
	}
	events := make([]contracts.StateEntry, 0, len(lines))
	for _, line := range lines {
		entry, err := decodeStateLine(line)
		if err != nil {
			return nil, err
		}
		entryPR, ok := stateEntryPR(entry)
		if ok && entryPR == pr {
			events = append(events, entry)
		}
	}
	return events, nil
}

func readProcessedEntriesPath(path string) ([]contracts.StateEntry, error) {
	lines, err := readProcessedLines(path)
	if err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return nil, nil
	}
	entries := make([]contracts.StateEntry, 0, len(lines))
	for _, line := range lines {
		entry, err := decodeStateLine(line)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func latestActionEntry(entries []contracts.StateEntry) *contracts.StateEntry {
	var latestWarning *contracts.StateEntry
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		if entry.Kind.IsWarning() {
			if latestWarning == nil {
				candidate := entry
				latestWarning = &candidate
			}
			continue
		}
		candidate := entry
		return &candidate
	}
	return latestWarning
}

func readProcessedLines(path string) ([]processedLine, error) {
	if err := contracts.EnsureCleanAbsolutePath(path); err != nil {
		return nil, err
	}
	snapshot, err := readProcessedSnapshot(path)
	if err != nil {
		return nil, err
	}
	if len(snapshot.Data) == 0 {
		return nil, nil
	}
	data := snapshot.Data
	if data[len(data)-1] != '\n' {
		initialSnapshot := snapshot
		time.Sleep(10 * time.Millisecond)
		retried, retryErr := readProcessedSnapshot(path)
		if retryErr != nil {
			return nil, retryErr
		}
		if len(retried.Data) > 0 {
			data = retried.Data
			snapshot = retried
		}
		if len(data) > 0 && data[len(data)-1] != '\n' {
			if !processedWriterInFlight(path, initialSnapshot, retried) {
				return nil, fmt.Errorf("%w: path=%s", ErrPartialStateLine, path)
			}
			lastNewline := bytes.LastIndexByte(data, '\n')
			if lastNewline < 0 {
				return nil, nil
			}
			data = data[:lastNewline+1]
		}
		if len(data) == 0 {
			return nil, nil
		}
	}
	lines := make([]processedLine, 0, 8)
	start := 0
	lineNo := 1
	for start < len(data) {
		end := start
		for end < len(data) && data[end] != '\n' {
			end++
		}
		lines = append(lines, processedLine{
			Number: lineNo,
			Offset: int64(start),
			Data:   data[start:end],
		})
		lineNo++
		if end == len(data) {
			break
		}
		start = end + 1
	}
	return lines, nil
}

func stateLockPath(path string) string {
	return filepath.Join(filepath.Dir(path), "state.lock")
}

func readProcessedSnapshot(path string) (processedFileSnapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return processedFileSnapshot{}, nil
		}
		return processedFileSnapshot{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return processedFileSnapshot{}, nil
		}
		return processedFileSnapshot{}, err
	}
	return processedFileSnapshot{
		Data:    data,
		Size:    info.Size(),
		ModTime: info.ModTime(),
	}, nil
}

func processedWriterInFlight(path string, before, after processedFileSnapshot) bool {
	if after.Size > before.Size || after.ModTime.After(before.ModTime) {
		return true
	}
	lockPath := stateLockPath(path)
	if _, err := os.Stat(lockPath); err != nil {
		return false
	}
	lock, acquired, err := internalio.TryAcquireFileLock(lockPath)
	if err != nil {
		return false
	}
	if acquired {
		_ = lock.Unlock()
		return false
	}
	return true
}

func decodeStateLine(line processedLine) (contracts.StateEntry, error) {
	if len(line.Data) == 0 {
		return contracts.StateEntry{}, fmt.Errorf("jsonl line %d at offset %d: %w", line.Number, line.Offset, contracts.ErrEmptyJSON)
	}
	if len(line.Data)+1 > internalio.JSONLMaxLineBytes {
		return contracts.StateEntry{}, fmt.Errorf("jsonl line %d at offset %d: %w", line.Number, line.Offset, internalio.ErrEntryTooLarge)
	}
	var entry contracts.StateEntry
	if err := contracts.DecodeStrictJSON(line.Data, &entry); err != nil {
		return contracts.StateEntry{}, fmt.Errorf("jsonl line %d at offset %d: %w", line.Number, line.Offset, err)
	}
	return entry, nil
}
