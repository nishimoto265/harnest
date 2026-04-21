package state

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
)

const processedDetailsDir = "processed-details"

type NextAction string

const (
	NextActionFreshStart          NextAction = "fresh_start"
	NextActionResume              NextAction = "resume"
	NextActionNeedsManualRecovery NextAction = "needs_manual_recovery"
)

type Reader struct {
	path string
}

type Writer struct {
	path   string
	runDir string
}

type ResumeRequest struct {
	PR    int
	RunID contracts.RunID
	Step  contracts.FailedStep
}

type LatestRun struct {
	PR        int
	RunID     contracts.RunID
	Step      contracts.FailedStep
	LastEvent *contracts.StateEntry
	Action    NextAction
}

func NewReader(ctx internalio.RunContext) Reader {
	return Reader{path: ctx.ProcessedPath()}
}

func NewReaderPath(path string) (Reader, error) {
	if err := contracts.EnsureCleanAbsolutePath(path); err != nil {
		return Reader{}, err
	}
	return Reader{path: path}, nil
}

func NewWriter(ctx internalio.RunContext) Writer {
	return Writer{
		path:   ctx.ProcessedPath(),
		runDir: ctx.RunDir(),
	}
}

func NewWriterPath(path string) (Writer, error) {
	if err := contracts.EnsureCleanAbsolutePath(path); err != nil {
		return Writer{}, err
	}
	return Writer{path: path}, nil
}

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

func Append(ctx internalio.RunContext, entry contracts.StateEntry) error {
	return NewWriter(ctx).Append(entry)
}

func AppendStateEntry(ctx internalio.RunContext, entry contracts.StateEntry) error {
	return Append(ctx, entry)
}

func ScanEventsForRun(ctx internalio.RunContext, runID contracts.RunID) ([]contracts.StateEntry, error) {
	entries, err := internalio.ReadJSONL[contracts.StateEntry](ctx.ProcessedPath())
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

func ResumeTarget(entries []contracts.StateEntry) []ResumeRequest {
	grouped := make(map[int][]contracts.StateEntry)
	for _, entry := range entries {
		pr, ok := stateEntryPR(entry)
		if !ok {
			continue
		}
		grouped[pr] = append(grouped[pr], entry)
	}
	if len(grouped) == 0 {
		return nil
	}
	prs := make([]int, 0, len(grouped))
	for pr := range grouped {
		prs = append(prs, pr)
	}
	sort.Ints(prs)
	requests := make([]ResumeRequest, 0, len(prs))
	for _, pr := range prs {
		entry := latestActionEntry(grouped[pr])
		if entry == nil || NextActionForEntry(entry) != NextActionResume {
			continue
		}
		runID, ok := stateEntryRunID(*entry)
		if !ok {
			continue
		}
		step, ok := stateEntryStep(*entry)
		if !ok {
			continue
		}
		requests = append(requests, ResumeRequest{
			PR:    pr,
			RunID: runID,
			Step:  step,
		})
	}
	if len(requests) == 0 {
		return nil
	}
	return requests
}

func ClassifyNextAction(entries []contracts.StateEntry) NextAction {
	return NextActionForEntry(latestActionEntry(entries))
}

func classifyNextActionKind(kind contracts.StateKind) NextAction {
	switch kind {
	case contracts.StateKindStarted,
		contracts.StateKindStepDone,
		contracts.StateKindInterrupted,
		contracts.StateKindPromoting,
		contracts.StateKindWarningRegistrySizeHigh,
		contracts.StateKindWarningRegistrySizeCritical,
		contracts.StateKindWarningRescueRetry:
		return NextActionResume
	case contracts.StateKindNeedsManualRecovery:
		return NextActionNeedsManualRecovery
	default:
		return NextActionFreshStart
	}
}

func NextActionForEntry(entry *contracts.StateEntry) NextAction {
	if entry == nil {
		return NextActionFreshStart
	}
	return classifyNextActionKind(entry.Kind)
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

func (w Writer) Append(entry contracts.StateEntry) error {
	runDir := w.runDir
	if _, ok := stateEntryRunID(entry); !ok {
		runDir = ""
	}
	normalized, err := normalizeDetailOverflow(runDir, entry)
	if err != nil {
		return err
	}
	lock, err := internalio.AcquireFileLock(filepath.Join(filepath.Dir(w.path), "state.lock"))
	if err != nil {
		return err
	}
	defer func() {
		_ = lock.Unlock()
	}()
	return internalio.AppendJSONL(w.path, normalized)
}

func (w Writer) AppendStateEntry(entry contracts.StateEntry) error {
	return w.Append(entry)
}

type processedLine struct {
	Number int
	Offset int64
	Data   []byte
}

func latestEntriesByPRPath(path string) (map[int]contracts.StateEntry, error) {
	lines, err := readProcessedLines(path)
	if err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return nil, nil
	}
	grouped := make(map[int][]contracts.StateEntry)
	for _, line := range lines {
		entry, err := decodeStateLine(line)
		if err != nil {
			return nil, err
		}
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
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	if data[len(data)-1] != '\n' {
		time.Sleep(10 * time.Millisecond)
		retried, retryErr := os.ReadFile(path)
		if retryErr == nil && len(retried) > 0 {
			data = retried
		}
		if len(data) > 0 && data[len(data)-1] != '\n' {
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

func normalizeDetailOverflow(runDir string, entry contracts.StateEntry) (contracts.StateEntry, error) {
	switch value := entry.Value.(type) {
	case contracts.StateEntryInterrupted:
		normalized, err := normalizeDetailVariant(runDir, value.Detail, value.DetailOverflowRef)
		if err != nil {
			return contracts.StateEntry{}, err
		}
		value.Detail = normalized.detail
		value.DetailOverflowRef = normalized.ref
		entry.Value = value
	case *contracts.StateEntryInterrupted:
		if value == nil {
			return contracts.StateEntry{}, contracts.ErrNilValidationValue
		}
		cloned := *value
		normalized, err := normalizeDetailVariant(runDir, cloned.Detail, cloned.DetailOverflowRef)
		if err != nil {
			return contracts.StateEntry{}, err
		}
		cloned.Detail = normalized.detail
		cloned.DetailOverflowRef = normalized.ref
		entry.Value = cloned
	case contracts.StateEntryWarning:
		normalized, err := normalizeDetailVariant(runDir, value.Detail, value.DetailOverflowRef)
		if err != nil {
			return contracts.StateEntry{}, err
		}
		value.Detail = normalized.detail
		value.DetailOverflowRef = normalized.ref
		entry.Value = value
	case *contracts.StateEntryWarning:
		if value == nil {
			return contracts.StateEntry{}, contracts.ErrNilValidationValue
		}
		cloned := *value
		normalized, err := normalizeDetailVariant(runDir, cloned.Detail, cloned.DetailOverflowRef)
		if err != nil {
			return contracts.StateEntry{}, err
		}
		cloned.Detail = normalized.detail
		cloned.DetailOverflowRef = normalized.ref
		entry.Value = cloned
	case contracts.StateEntryCompleted:
		normalized, err := normalizeDetailVariant(runDir, value.Detail, value.DetailOverflowRef)
		if err != nil {
			return contracts.StateEntry{}, err
		}
		value.Detail = normalized.detail
		value.DetailOverflowRef = normalized.ref
		entry.Value = value
	case *contracts.StateEntryCompleted:
		if value == nil {
			return contracts.StateEntry{}, contracts.ErrNilValidationValue
		}
		cloned := *value
		normalized, err := normalizeDetailVariant(runDir, cloned.Detail, cloned.DetailOverflowRef)
		if err != nil {
			return contracts.StateEntry{}, err
		}
		cloned.Detail = normalized.detail
		cloned.DetailOverflowRef = normalized.ref
		entry.Value = cloned
	case contracts.StateEntryFailed:
		normalized, err := normalizeDetailVariant(runDir, value.Detail, value.DetailOverflowRef)
		if err != nil {
			return contracts.StateEntry{}, err
		}
		value.Detail = normalized.detail
		value.DetailOverflowRef = normalized.ref
		entry.Value = value
	case *contracts.StateEntryFailed:
		if value == nil {
			return contracts.StateEntry{}, contracts.ErrNilValidationValue
		}
		cloned := *value
		normalized, err := normalizeDetailVariant(runDir, cloned.Detail, cloned.DetailOverflowRef)
		if err != nil {
			return contracts.StateEntry{}, err
		}
		cloned.Detail = normalized.detail
		cloned.DetailOverflowRef = normalized.ref
		entry.Value = cloned
	case contracts.StateEntrySkipped:
		normalized, err := normalizeDetailVariant(runDir, value.Detail, value.DetailOverflowRef)
		if err != nil {
			return contracts.StateEntry{}, err
		}
		value.Detail = normalized.detail
		value.DetailOverflowRef = normalized.ref
		entry.Value = value
	case *contracts.StateEntrySkipped:
		if value == nil {
			return contracts.StateEntry{}, contracts.ErrNilValidationValue
		}
		cloned := *value
		normalized, err := normalizeDetailVariant(runDir, cloned.Detail, cloned.DetailOverflowRef)
		if err != nil {
			return contracts.StateEntry{}, err
		}
		cloned.Detail = normalized.detail
		cloned.DetailOverflowRef = normalized.ref
		entry.Value = cloned
	case contracts.StateEntryNeedsManualRecovery:
		normalized, err := normalizeDetailVariant(runDir, value.Detail, value.DetailOverflowRef)
		if err != nil {
			return contracts.StateEntry{}, err
		}
		value.Detail = normalized.detail
		value.DetailOverflowRef = normalized.ref
		entry.Value = value
	case *contracts.StateEntryNeedsManualRecovery:
		if value == nil {
			return contracts.StateEntry{}, contracts.ErrNilValidationValue
		}
		cloned := *value
		normalized, err := normalizeDetailVariant(runDir, cloned.Detail, cloned.DetailOverflowRef)
		if err != nil {
			return contracts.StateEntry{}, err
		}
		cloned.Detail = normalized.detail
		cloned.DetailOverflowRef = normalized.ref
		entry.Value = cloned
	}
	return entry, nil
}

type normalizedDetail struct {
	detail string
	ref    *contracts.OverflowRef
}

func normalizeDetailVariant(runDir, detail string, ref *contracts.OverflowRef) (normalizedDetail, error) {
	if utf8.RuneCountInString(detail) <= 300 {
		return normalizedDetail{detail: detail, ref: ref}, nil
	}
	if runDir == "" {
		return normalizedDetail{}, errors.New("state: detail overflow requires run directory")
	}
	sum := sha256.Sum256([]byte(detail))
	sha256Hex := hex.EncodeToString(sum[:])
	sidecarPath, err := internalio.WriteSidecar(filepath.Join(runDir, processedDetailsDir), sha256Hex, detail)
	if err != nil {
		return normalizedDetail{}, err
	}
	relPath, err := internalio.SidecarRefPath(runDir, sidecarPath)
	if err != nil {
		return normalizedDetail{}, err
	}
	return normalizedDetail{
		detail: truncateRunes(detail, 300),
		ref: &contracts.OverflowRef{
			Path:   relPath,
			Sha256: sha256Hex,
		},
	}, nil
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := make([]rune, 0, limit)
	for _, r := range value {
		if len(runes) == limit {
			break
		}
		runes = append(runes, r)
	}
	return string(runes)
}

func stateEntryPR(entry contracts.StateEntry) (int, bool) {
	switch value := entry.Value.(type) {
	case contracts.StateEntryStarted:
		return value.PR, true
	case *contracts.StateEntryStarted:
		return derefPR(value, func(v *contracts.StateEntryStarted) int { return v.PR })
	case contracts.StateEntryStepDone:
		return value.PR, true
	case *contracts.StateEntryStepDone:
		return derefPR(value, func(v *contracts.StateEntryStepDone) int { return v.PR })
	case contracts.StateEntryInterrupted:
		return value.PR, true
	case *contracts.StateEntryInterrupted:
		return derefPR(value, func(v *contracts.StateEntryInterrupted) int { return v.PR })
	case contracts.StateEntryPromoting:
		return value.PR, true
	case *contracts.StateEntryPromoting:
		return derefPR(value, func(v *contracts.StateEntryPromoting) int { return v.PR })
	case contracts.StateEntryWarning:
		if value.PR == nil {
			return 0, false
		}
		return *value.PR, true
	case *contracts.StateEntryWarning:
		if value == nil || value.PR == nil {
			return 0, false
		}
		return *value.PR, true
	case contracts.StateEntryCompleted:
		return value.PR, true
	case *contracts.StateEntryCompleted:
		return derefPR(value, func(v *contracts.StateEntryCompleted) int { return v.PR })
	case contracts.StateEntryFailed:
		return value.PR, true
	case *contracts.StateEntryFailed:
		return derefPR(value, func(v *contracts.StateEntryFailed) int { return v.PR })
	case contracts.StateEntryPromoted:
		return value.PR, true
	case *contracts.StateEntryPromoted:
		return derefPR(value, func(v *contracts.StateEntryPromoted) int { return v.PR })
	case contracts.StateEntryRollback:
		return value.PR, true
	case *contracts.StateEntryRollback:
		return derefPR(value, func(v *contracts.StateEntryRollback) int { return v.PR })
	case contracts.StateEntrySkipped:
		return value.PR, true
	case *contracts.StateEntrySkipped:
		return derefPR(value, func(v *contracts.StateEntrySkipped) int { return v.PR })
	case contracts.StateEntryTimeout:
		return value.PR, true
	case *contracts.StateEntryTimeout:
		return derefPR(value, func(v *contracts.StateEntryTimeout) int { return v.PR })
	case contracts.StateEntryNeedsManualRecovery:
		return value.PR, true
	case *contracts.StateEntryNeedsManualRecovery:
		return derefPR(value, func(v *contracts.StateEntryNeedsManualRecovery) int { return v.PR })
	default:
		return 0, false
	}
}

func stateEntryRunID(entry contracts.StateEntry) (contracts.RunID, bool) {
	switch value := entry.Value.(type) {
	case contracts.StateEntryStarted:
		return value.RunID, true
	case *contracts.StateEntryStarted:
		return derefRunID(value, func(v *contracts.StateEntryStarted) contracts.RunID { return v.RunID })
	case contracts.StateEntryStepDone:
		return value.RunID, true
	case *contracts.StateEntryStepDone:
		return derefRunID(value, func(v *contracts.StateEntryStepDone) contracts.RunID { return v.RunID })
	case contracts.StateEntryInterrupted:
		return value.RunID, true
	case *contracts.StateEntryInterrupted:
		return derefRunID(value, func(v *contracts.StateEntryInterrupted) contracts.RunID { return v.RunID })
	case contracts.StateEntryPromoting:
		return value.RunID, true
	case *contracts.StateEntryPromoting:
		return derefRunID(value, func(v *contracts.StateEntryPromoting) contracts.RunID { return v.RunID })
	case contracts.StateEntryWarning:
		if value.RunID == nil {
			return "", false
		}
		return *value.RunID, true
	case *contracts.StateEntryWarning:
		if value == nil || value.RunID == nil {
			return "", false
		}
		return *value.RunID, true
	case contracts.StateEntryCompleted:
		return value.RunID, true
	case *contracts.StateEntryCompleted:
		return derefRunID(value, func(v *contracts.StateEntryCompleted) contracts.RunID { return v.RunID })
	case contracts.StateEntryFailed:
		return value.RunID, true
	case *contracts.StateEntryFailed:
		return derefRunID(value, func(v *contracts.StateEntryFailed) contracts.RunID { return v.RunID })
	case contracts.StateEntryPromoted:
		return value.RunID, true
	case *contracts.StateEntryPromoted:
		return derefRunID(value, func(v *contracts.StateEntryPromoted) contracts.RunID { return v.RunID })
	case contracts.StateEntryRollback:
		return value.RunID, true
	case *contracts.StateEntryRollback:
		return derefRunID(value, func(v *contracts.StateEntryRollback) contracts.RunID { return v.RunID })
	case contracts.StateEntrySkipped:
		return value.RunID, true
	case *contracts.StateEntrySkipped:
		return derefRunID(value, func(v *contracts.StateEntrySkipped) contracts.RunID { return v.RunID })
	case contracts.StateEntryTimeout:
		return value.RunID, true
	case *contracts.StateEntryTimeout:
		return derefRunID(value, func(v *contracts.StateEntryTimeout) contracts.RunID { return v.RunID })
	case contracts.StateEntryNeedsManualRecovery:
		return value.RunID, true
	case *contracts.StateEntryNeedsManualRecovery:
		return derefRunID(value, func(v *contracts.StateEntryNeedsManualRecovery) contracts.RunID { return v.RunID })
	default:
		return "", false
	}
}

func stateEntryStep(entry contracts.StateEntry) (contracts.FailedStep, bool) {
	switch value := entry.Value.(type) {
	case contracts.StateEntryStarted:
		return value.Step, true
	case *contracts.StateEntryStarted:
		return derefStep(value, func(v *contracts.StateEntryStarted) contracts.FailedStep { return v.Step })
	case contracts.StateEntryStepDone:
		return value.Step, true
	case *contracts.StateEntryStepDone:
		return derefStep(value, func(v *contracts.StateEntryStepDone) contracts.FailedStep { return v.Step })
	case contracts.StateEntryInterrupted:
		return value.Step, true
	case *contracts.StateEntryInterrupted:
		return derefStep(value, func(v *contracts.StateEntryInterrupted) contracts.FailedStep { return v.Step })
	case contracts.StateEntryPromoting:
		return value.Step, true
	case *contracts.StateEntryPromoting:
		return derefStep(value, func(v *contracts.StateEntryPromoting) contracts.FailedStep { return v.Step })
	case contracts.StateEntryWarning:
		if value.Step == nil {
			return "", false
		}
		return *value.Step, true
	case *contracts.StateEntryWarning:
		if value == nil || value.Step == nil {
			return "", false
		}
		return *value.Step, true
	case contracts.StateEntryCompleted:
		return value.Step, true
	case *contracts.StateEntryCompleted:
		return derefStep(value, func(v *contracts.StateEntryCompleted) contracts.FailedStep { return v.Step })
	case contracts.StateEntryFailed:
		return value.Step, true
	case *contracts.StateEntryFailed:
		return derefStep(value, func(v *contracts.StateEntryFailed) contracts.FailedStep { return v.Step })
	case contracts.StateEntryPromoted:
		return value.Step, true
	case *contracts.StateEntryPromoted:
		return derefStep(value, func(v *contracts.StateEntryPromoted) contracts.FailedStep { return v.Step })
	case contracts.StateEntryRollback:
		return value.Step, true
	case *contracts.StateEntryRollback:
		return derefStep(value, func(v *contracts.StateEntryRollback) contracts.FailedStep { return v.Step })
	case contracts.StateEntrySkipped:
		return value.Step, true
	case *contracts.StateEntrySkipped:
		return derefStep(value, func(v *contracts.StateEntrySkipped) contracts.FailedStep { return v.Step })
	case contracts.StateEntryTimeout:
		return value.Step, true
	case *contracts.StateEntryTimeout:
		return derefStep(value, func(v *contracts.StateEntryTimeout) contracts.FailedStep { return v.Step })
	case contracts.StateEntryNeedsManualRecovery:
		return value.Step, true
	case *contracts.StateEntryNeedsManualRecovery:
		return derefStep(value, func(v *contracts.StateEntryNeedsManualRecovery) contracts.FailedStep { return v.Step })
	default:
		return "", false
	}
}

func derefPR[T any](value *T, fn func(*T) int) (int, bool) {
	if value == nil {
		return 0, false
	}
	return fn(value), true
}

func derefRunID[T any](value *T, fn func(*T) contracts.RunID) (contracts.RunID, bool) {
	if value == nil {
		return "", false
	}
	return fn(value), true
}

func derefStep[T any](value *T, fn func(*T) contracts.FailedStep) (contracts.FailedStep, bool) {
	if value == nil {
		return "", false
	}
	return fn(value), true
}
