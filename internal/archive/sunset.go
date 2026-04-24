// Package archive contains the rule sunset business logic described in
// docs/design/io-contracts.md §archive.
package archive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/registryview"
	"github.com/nishimoto265/auto-improve/internal/state"
)

const (
	markerFilename     = "sunset-running.marker"
	divergedMarkerFile = markerFilename + ".diverged"
	lastSunsetFilename = "last-sunset-at"
	defaultGate        = 24 * time.Hour
	defaultLockTimeout = 30 * time.Second
)

var errBlockedBySentinel = errors.New("archive: blocked by sentinel")
var ErrSunsetActive = errors.New("archive: sunset is active")
var ErrStaleMarkerDiverged = errors.New("archive: stale sunset marker diverged from current registry snapshot")

var appendRegistryEntry = internalio.AppendRegistryEntry

type Opts struct {
	RunsBase    string
	SunsetRunID string
	Transitions []Transition
	Force       bool
	Now         func() time.Time
	Gate        time.Duration
	LockTimeout time.Duration

	RegistryHighAt int
	RegistryCritAt int
}

type Transition struct {
	RuleID     string
	PrevStatus contracts.RuleStatus
	NewStatus  contracts.RuleStatus
	Kind       contracts.RegistryKind
	Transition contracts.SunsetTransition
}

type Result struct {
	AppendedOpIDs []string
	SkippedOpIDs  []string
}

type sunsetMarker struct {
	RecordedStartTime time.Time      `json:"recorded_start_time"`
	SunsetRunID       string         `json:"sunset_run_id"`
	Transitions       []Transition   `json:"transitions"`
	RegistryHeadSHA   string         `json:"registry_head_sha,omitempty"`
	RuleSeqSnapshot   map[string]int `json:"rule_seq_snapshot,omitempty"`
}

type registryLine = internalio.RegistryLine

func RunSunset(ctx context.Context, opts Opts) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	opts = applyDefaults(opts)
	if opts.RunsBase == "" {
		return Result{}, errors.New("archive: runs_base is required")
	}
	if opts.SunsetRunID == "" {
		return Result{}, errors.New("archive: sunset_run_id is required")
	}

	registryPath := filepath.Join(opts.RunsBase, "rules-registry.jsonl")
	result := Result{}
	for _, t := range opts.Transitions {
		if blocked, err := sentinelExists(opts.RunsBase); err != nil {
			return result, err
		} else if blocked {
			return result, errBlockedBySentinel
		}
		opID := ComputeOpID(opts.SunsetRunID, t.RuleID, transitionKey(t))
		// F19: accept legacy plain-concat op-id so entries appended before the
		// length-prefixed encoding are still recognised as already-applied.
		foundExisting := false
		for _, candidate := range opIDCandidates(opts.SunsetRunID, t.RuleID, transitionKey(t)) {
			existing, ok, err := findByOpID(registryPath, candidate)
			if err != nil {
				return result, err
			}
			if ok {
				_ = existing
				result.SkippedOpIDs = append(result.SkippedOpIDs, candidate)
				foundExisting = true
				break
			}
		}
		if foundExisting {
			continue
		}
		_ = opID

		entry, err := buildRegistryEntry(registryPath, t, opts.SunsetRunID, opID, opts.Now())
		if err != nil {
			return result, err
		}
		if blocked, err := sentinelExists(opts.RunsBase); err != nil {
			return result, err
		} else if blocked {
			return result, errBlockedBySentinel
		}
		appended, err := appendRegistryEntry(registryPath, entry)
		if err != nil {
			return result, fmt.Errorf("archive: append registry entry: %w", err)
		}
		syncRegistryIndex(opts.RunsBase, registryPath, entry, appended)
		result.AppendedOpIDs = append(result.AppendedOpIDs, opID)
	}

	if blocked, err := sentinelExists(opts.RunsBase); err != nil {
		return result, err
	} else if blocked {
		return result, errBlockedBySentinel
	}
	if err := emitSizeWarnings(opts); err != nil {
		return result, err
	}
	return result, nil
}

func BuildTransitionPlan(runsBase string) ([]Transition, error) {
	if runsBase == "" {
		return nil, errors.New("archive: runs_base is required")
	}
	registryPath := filepath.Join(runsBase, "rules-registry.jsonl")
	lines, err := readRegistryLines(registryPath)
	if err != nil {
		return nil, err
	}
	entries := make([]contracts.RuleRegistryEntry, 0, len(lines))
	for _, line := range lines {
		entries = append(entries, line.Entry)
	}
	states, err := registryview.Build(entries)
	if err != nil {
		return nil, err
	}
	ruleIDs := make([]string, 0, len(states))
	for ruleID, state := range states {
		if !state.Exists || state.Status != contracts.RuleStatusDeprecated {
			continue
		}
		ruleIDs = append(ruleIDs, ruleID)
	}
	sort.Strings(ruleIDs)

	transitions := make([]Transition, 0, len(ruleIDs))
	for _, ruleID := range ruleIDs {
		state := states[ruleID]
		transitions = append(transitions, Transition{
			RuleID:     ruleID,
			PrevStatus: state.Status,
			NewStatus:  contracts.RuleStatusArchived,
			Kind:       contracts.RegistryKindArchived,
			Transition: contracts.SunsetTransitionArchive,
		})
	}
	return transitions, nil
}

func RunSunsetWithLock(ctx context.Context, opts Opts) (Result, error) {
	opts = applyDefaults(opts)
	if opts.RunsBase == "" {
		return Result{}, errors.New("archive: runs_base is required")
	}
	if opts.SunsetRunID == "" {
		return Result{}, errors.New("archive: sunset_run_id is required")
	}
	if blocked, err := sentinelExists(opts.RunsBase); err != nil {
		return Result{}, err
	} else if blocked {
		return Result{}, nil
	}
	if diverged, err := divergedMarkerExists(opts.RunsBase); err != nil {
		return Result{}, err
	} else if diverged {
		return Result{}, ErrStaleMarkerDiverged
	}

	lockPath := filepath.Join(opts.RunsBase, "promotion.lock")
	var lock *internalio.FileLock
	var err error
	if opts.Force {
		lock, err = internalio.AcquireFileLock(lockPath)
	} else {
		lockCtx := ctx
		var cancel context.CancelFunc
		if opts.LockTimeout > 0 {
			lockCtx, cancel = context.WithTimeout(ctx, opts.LockTimeout)
			defer cancel()
		}
		lock, err = internalio.AcquireFileLockContext(lockCtx, lockPath)
		if errors.Is(err, context.DeadlineExceeded) {
			slog.Warn("archive: promotion.lock acquisition timed out", slog.Duration("timeout", opts.LockTimeout))
			return Result{}, nil
		}
	}
	if err != nil {
		return Result{}, fmt.Errorf("archive: acquire promotion.lock: %w", err)
	}
	defer func() {
		_ = lock.Unlock()
	}()

	if blocked, err := sentinelExists(opts.RunsBase); err != nil {
		return Result{}, err
	} else if blocked {
		return Result{}, nil
	}
	if diverged, err := divergedMarkerExists(opts.RunsBase); err != nil {
		return Result{}, err
	} else if diverged {
		return Result{}, ErrStaleMarkerDiverged
	}

	if err := reconcileStaleMarker(ctx, opts); err != nil {
		return Result{}, err
	}
	if blocked, err := sentinelExists(opts.RunsBase); err != nil {
		return Result{}, err
	} else if blocked {
		return Result{}, nil
	}
	if !opts.Force {
		ok, err := gateAllows(opts)
		if err != nil {
			return Result{}, err
		}
		if !ok {
			return Result{}, nil
		}
	}
	if len(opts.Transitions) == 0 {
		return Result{}, nil
	}

	if err := writeMarker(opts); err != nil {
		return Result{}, err
	}
	result, runErr := RunSunset(ctx, opts)
	if errors.Is(runErr, errBlockedBySentinel) {
		return result, nil
	}
	if runErr != nil {
		return result, runErr
	}
	if err := writeLastSunsetAt(opts.RunsBase, opts.Now()); err != nil {
		return result, err
	}
	if err := os.Remove(filepath.Join(opts.RunsBase, markerFilename)); err != nil && !os.IsNotExist(err) {
		return result, err
	}
	return result, nil
}

// ReconcileStaleSunsetMarkerWithLock completes or clears a stale
// sunset-running.marker under the shared promotion lock. If the lock is held,
// the marker may belong to a live sunset and must continue to block callers.
func ReconcileStaleSunsetMarkerWithLock(ctx context.Context, runsBase string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if runsBase == "" {
		return errors.New("archive: runs_base is required")
	}
	if diverged, err := divergedMarkerExists(runsBase); err != nil {
		return err
	} else if diverged {
		return ErrStaleMarkerDiverged
	}
	if blocked, err := sentinelExists(runsBase); err != nil {
		return err
	} else if blocked {
		return nil
	}
	markerPath := filepath.Join(runsBase, markerFilename)
	if _, err := os.Stat(markerPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	lock, acquired, err := internalio.TryAcquireFileLock(filepath.Join(runsBase, "promotion.lock"))
	if err != nil {
		return fmt.Errorf("archive: acquire promotion.lock: %w", err)
	}
	if !acquired {
		return ErrSunsetActive
	}
	defer func() {
		_ = lock.Unlock()
	}()
	if diverged, err := divergedMarkerExists(runsBase); err != nil {
		return err
	} else if diverged {
		return ErrStaleMarkerDiverged
	}
	if blocked, err := sentinelExists(runsBase); err != nil {
		return err
	} else if blocked {
		return nil
	}
	if _, err := os.Stat(markerPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return reconcileStaleMarker(ctx, Opts{RunsBase: runsBase})
}

func ComputeOpID(sunsetRunID, ruleID, transition string) string {
	var payload bytes.Buffer
	for _, field := range []string{sunsetRunID, ruleID, transition} {
		fmt.Fprintf(&payload, "%08x:", len(field))
		payload.WriteString(field)
	}
	sum := sha256.Sum256(payload.Bytes())
	return hex.EncodeToString(sum[:])
}

// computeLegacyOpID reproduces the pre-length-prefixed plain-concat op-id
// encoding that predates the unambiguous tuple scheme. Kept for backward
// compatibility with in-flight marker / registry state written before the
// encoding change so findByOpID / markerTailProgressPrefixLen can recognise
// partially-applied sunset transitions after an upgrade (F19).
func computeLegacyOpID(sunsetRunID, ruleID, transition string) string {
	sum := sha256.Sum256([]byte(sunsetRunID + ruleID + transition))
	return hex.EncodeToString(sum[:])
}

// opIDCandidates returns the current and legacy op-id encodings. Callers that
// reconcile persisted state (registry lookup, stale marker replay) must accept
// both so an upgrade mid-sunset does not re-apply or strand entries.
func opIDCandidates(sunsetRunID, ruleID, transition string) []string {
	current := ComputeOpID(sunsetRunID, ruleID, transition)
	legacy := computeLegacyOpID(sunsetRunID, ruleID, transition)
	if current == legacy {
		return []string{current}
	}
	return []string{current, legacy}
}

func applyDefaults(o Opts) Opts {
	if o.Now == nil {
		o.Now = func() time.Time { return time.Now().UTC() }
	}
	if o.Gate == 0 {
		o.Gate = defaultGate
	}
	if o.LockTimeout == 0 {
		o.LockTimeout = defaultLockTimeout
	}
	if o.RegistryHighAt == 0 {
		o.RegistryHighAt = 1500
	}
	if o.RegistryCritAt == 0 {
		o.RegistryCritAt = 2000
	}
	return o
}

func transitionKey(t Transition) string {
	if t.Transition != "" {
		return string(t.Transition)
	}
	switch t.Kind {
	case contracts.RegistryKindArchived:
		return string(contracts.SunsetTransitionArchive)
	case contracts.RegistryKindRestored:
		if t.NewStatus == contracts.RuleStatusActive {
			return string(contracts.SunsetTransitionActivate)
		}
		return string(contracts.SunsetTransitionDeprecate)
	default:
		return string(t.Transition)
	}
}

func registryPrevHashForVersion(versionSeq int64, prevHash string) string {
	if versionSeq == 1 {
		return ""
	}
	return prevHash
}

func buildRegistryEntry(path string, t Transition, sunsetRunID, opID string, at time.Time) (contracts.RuleRegistryEntry, error) {
	lines, err := readRegistryLines(path)
	if err != nil {
		return contracts.RuleRegistryEntry{}, err
	}
	if err := validateTransitionAgainstRegistry(lines, t); err != nil {
		return contracts.RuleRegistryEntry{}, err
	}
	prevHash := ""
	if len(lines) > 0 {
		prevHash = lines[len(lines)-1].Sha256
	}
	versionSeq := nextRegistryVersion(lines, t.RuleID)

	switch t.Kind {
	case contracts.RegistryKindStatusChanged:
		v := contracts.RuleRegistryStatusChanged{
			Kind:          contracts.RegistryKindStatusChanged,
			SchemaVersion: "1",
			RuleID:        t.RuleID,
			PrevStatus:    t.PrevStatus,
			NewStatus:     t.NewStatus,
			Transition:    t.Transition,
			OpID:          opID,
			VersionSeq:    versionSeq,
			PrevHash:      registryPrevHashForVersion(versionSeq, prevHash),
			BySunsetRunID: sunsetRunID,
			At:            at,
		}
		return contracts.RuleRegistryEntry{Kind: v.Kind, Value: v}, nil
	case contracts.RegistryKindArchived:
		v := contracts.RuleRegistryArchived{
			Kind:          contracts.RegistryKindArchived,
			SchemaVersion: "1",
			RuleID:        t.RuleID,
			PrevStatus:    t.PrevStatus,
			NewStatus:     contracts.RuleStatusArchived,
			OpID:          opID,
			VersionSeq:    versionSeq,
			PrevHash:      registryPrevHashForVersion(versionSeq, prevHash),
			BySunsetRunID: sunsetRunID,
			At:            at,
		}
		return contracts.RuleRegistryEntry{Kind: v.Kind, Value: v}, nil
	case contracts.RegistryKindRestored:
		v := contracts.RuleRegistryRestored{
			Kind:          contracts.RegistryKindRestored,
			SchemaVersion: "1",
			RuleID:        t.RuleID,
			PrevStatus:    contracts.RuleStatusArchived,
			NewStatus:     t.NewStatus,
			OpID:          opID,
			VersionSeq:    versionSeq,
			PrevHash:      registryPrevHashForVersion(versionSeq, prevHash),
			BySunsetRunID: sunsetRunID,
			At:            at,
		}
		return contracts.RuleRegistryEntry{Kind: v.Kind, Value: v}, nil
	default:
		return contracts.RuleRegistryEntry{}, fmt.Errorf("archive: unsupported transition kind=%q", t.Kind)
	}
}

func validateTransitionAgainstRegistry(lines []registryLine, t Transition) error {
	entries := make([]contracts.RuleRegistryEntry, 0, len(lines))
	for _, line := range lines {
		entries = append(entries, line.Entry)
	}
	states, err := registryview.Build(entries)
	if err != nil {
		return err
	}
	state, ok := states[t.RuleID]
	if !ok || (state.RuleID == "" && state.Status == "" && !state.Exists) {
		return fmt.Errorf("archive: rule not found in registry: rule_id=%s", t.RuleID)
	}
	if state.Status != t.PrevStatus {
		return fmt.Errorf("archive: registry status mismatch: rule_id=%s have=%s want=%s", t.RuleID, state.Status, t.PrevStatus)
	}
	return nil
}

func nextRegistryVersion(lines []registryLine, _ string) int64 {
	if len(lines) == 0 {
		return 1
	}
	return registryVersionSeq(lines[len(lines)-1].Entry) + 1
}

func registryVersionSeq(entry contracts.RuleRegistryEntry) int64 {
	switch v := entry.Value.(type) {
	case contracts.RuleRegistryAdded:
		return v.VersionSeq
	case contracts.RuleRegistryUpdated:
		return v.VersionSeq
	case contracts.RuleRegistryRolledBack:
		return v.VersionSeq
	case contracts.RuleRegistryStatusChanged:
		return v.VersionSeq
	case contracts.RuleRegistryArchived:
		return v.VersionSeq
	case contracts.RuleRegistryRestored:
		return v.VersionSeq
	default:
		return 0
	}
}

func findByOpID(path, opID string) (contracts.RegistryAppendResult, bool, error) {
	lines, err := registryLookupLines(path)
	if err != nil {
		return contracts.RegistryAppendResult{}, false, err
	}
	for i := len(lines) - 1; i >= 0; i-- {
		switch v := lines[i].Entry.Value.(type) {
		case contracts.RuleRegistryStatusChanged:
			if v.OpID == opID {
				return contracts.RegistryAppendResult{Offset: lines[i].Offset, Sha256: lines[i].Sha256}, true, nil
			}
		case contracts.RuleRegistryArchived:
			if v.OpID == opID {
				return contracts.RegistryAppendResult{Offset: lines[i].Offset, Sha256: lines[i].Sha256}, true, nil
			}
		case contracts.RuleRegistryRestored:
			if v.OpID == opID {
				return contracts.RegistryAppendResult{Offset: lines[i].Offset, Sha256: lines[i].Sha256}, true, nil
			}
		}
	}
	return contracts.RegistryAppendResult{}, false, nil
}

func reconcileStaleMarker(ctx context.Context, opts Opts) error {
	path := filepath.Join(opts.RunsBase, markerFilename)
	marker, err := readMarker(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		var legacy legacySunsetMarker
		if errors.As(err, &legacy) {
			if !legacy.RecordedStartTime.IsZero() {
				if err := writeLastSunsetAt(opts.RunsBase, legacy.RecordedStartTime); err != nil {
					return err
				}
			}
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
			return nil
		}
		return err
	}
	missing := make([]Transition, 0, len(marker.Transitions))
	prefixLen, err := markerTailProgressPrefixLen(opts.RunsBase, marker)
	if err != nil {
		return err
	}
	if prefixLen == 0 {
		if ok, err := markerSnapshotMatches(opts.RunsBase, marker); err != nil {
			return err
		} else if !ok {
			if err := markStaleMarkerDiverged(opts.RunsBase); err != nil {
				return err
			}
			return ErrStaleMarkerDiverged
		}
	}
	for _, transition := range marker.Transitions {
		found := false
		// F19: accept both legacy plain-concat and current length-prefixed
		// op-id encodings so reconcile does not re-apply transitions that
		// predate the op-id scheme change.
		for _, candidate := range opIDCandidates(marker.SunsetRunID, transition.RuleID, transitionKey(transition)) {
			if _, ok, err := findByOpID(filepath.Join(opts.RunsBase, "rules-registry.jsonl"), candidate); err != nil {
				return err
			} else if ok {
				found = true
				break
			}
		}
		if found {
			continue
		}
		missing = append(missing, transition)
	}
	if len(missing) > 0 {
		retryOpts := opts
		retryOpts.SunsetRunID = marker.SunsetRunID
		retryOpts.Transitions = missing
		retryOpts.Now = func() time.Time { return marker.RecordedStartTime }
		if _, err := RunSunset(ctx, retryOpts); err != nil {
			return err
		}
	}
	if err := writeLastSunsetAt(opts.RunsBase, marker.RecordedStartTime); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func markerTailProgressPrefixLen(runsBase string, marker sunsetMarker) (int, error) {
	registryPath := filepath.Join(runsBase, "rules-registry.jsonl")
	lines, err := readRegistryLines(registryPath)
	if err != nil {
		return 0, err
	}
	if len(lines) == 0 || len(marker.Transitions) == 0 {
		return 0, nil
	}
	// F19: accept both the current length-prefixed op-id encoding and the
	// legacy plain-concat encoding so markers written before the format
	// change are still recognised as progress.
	plannedCandidates := make([][]string, 0, len(marker.Transitions))
	for _, transition := range marker.Transitions {
		plannedCandidates = append(plannedCandidates, opIDCandidates(marker.SunsetRunID, transition.RuleID, transitionKey(transition)))
	}
	maxPrefix := len(plannedCandidates)
	if len(lines) < maxPrefix {
		maxPrefix = len(lines)
	}
	for prefix := maxPrefix; prefix > 0; prefix-- {
		matched := true
		for idx := 0; idx < prefix; idx++ {
			entryOpID := registryOpID(lines[len(lines)-prefix+idx].Entry)
			if !stringInSlice(entryOpID, plannedCandidates[idx]) {
				matched = false
				break
			}
		}
		if matched {
			return prefix, nil
		}
	}
	return 0, nil
}

func stringInSlice(needle string, haystack []string) bool {
	for _, candidate := range haystack {
		if candidate == needle {
			return true
		}
	}
	return false
}

func registryOpID(entry contracts.RuleRegistryEntry) string {
	switch v := entry.Value.(type) {
	case contracts.RuleRegistryStatusChanged:
		return v.OpID
	case contracts.RuleRegistryArchived:
		return v.OpID
	case contracts.RuleRegistryRestored:
		return v.OpID
	case *contracts.RuleRegistryStatusChanged:
		if v != nil {
			return v.OpID
		}
	case *contracts.RuleRegistryArchived:
		if v != nil {
			return v.OpID
		}
	case *contracts.RuleRegistryRestored:
		if v != nil {
			return v.OpID
		}
	}
	return ""
}

func gateAllows(opts Opts) (bool, error) {
	path := filepath.Join(opts.RunsBase, lastSunsetFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, err
	}
	last, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(data)))
	if err != nil {
		return false, fmt.Errorf("archive: parse last-sunset-at: %w", err)
	}
	return opts.Now().Sub(last) >= opts.Gate, nil
}

func writeMarker(opts Opts) error {
	path := filepath.Join(opts.RunsBase, markerFilename)
	registryHeadSHA, ruleSeqSnapshot, err := markerSnapshot(opts.RunsBase, opts.Transitions)
	if err != nil {
		return err
	}
	return internalio.WriteJSONAtomic(path, sunsetMarker{
		RecordedStartTime: opts.Now(),
		SunsetRunID:       opts.SunsetRunID,
		Transitions:       append([]Transition(nil), opts.Transitions...),
		RegistryHeadSHA:   registryHeadSHA,
		RuleSeqSnapshot:   ruleSeqSnapshot,
	})
}

func writeLastSunsetAt(runsBase string, t time.Time) error {
	return internalio.WriteAtomic(filepath.Join(runsBase, lastSunsetFilename), []byte(t.Format(time.RFC3339Nano)+"\n"))
}

func emitSizeWarnings(opts Opts) error {
	registryPath := filepath.Join(opts.RunsBase, "rules-registry.jsonl")
	count, err := registryLineCount(registryPath)
	if err != nil {
		return err
	}
	if count == 0 {
		return nil
	}
	writer, err := state.NewWriterPath(filepath.Join(opts.RunsBase, "processed.jsonl"))
	if err != nil {
		return err
	}
	source := contracts.WarningSourceSunsetTick
	cnt := int64(count)
	if count >= opts.RegistryCritAt {
		w := contracts.StateEntryWarning{
			Kind:   contracts.StateKindWarningRegistrySizeCritical,
			Source: &source,
			Count:  &cnt,
			At:     opts.Now(),
		}
		return writer.Append(contracts.StateEntry{Kind: w.Kind, Value: w})
	}
	if count >= opts.RegistryHighAt {
		w := contracts.StateEntryWarning{
			Kind:   contracts.StateKindWarningRegistrySizeHigh,
			Source: &source,
			Count:  &cnt,
			At:     opts.Now(),
		}
		return writer.Append(contracts.StateEntry{Kind: w.Kind, Value: w})
	}
	return nil
}

func sentinelExists(runsBase string) (bool, error) {
	if blocked, err := sunsetDivergedBlockExists(runsBase); err != nil {
		return false, err
	} else if blocked {
		return true, nil
	}
	processedPath := filepath.Join(runsBase, "processed.jsonl")
	latestRuns, err := state.NeedsManualRecoveryRunsPath(processedPath)
	if err != nil {
		return false, err
	}
	for _, latest := range latestRuns {
		if latest.RunID != "" {
			suppressed, err := needsRecoveryClearedMarkerExists(runsBase, latest.RunID)
			if err != nil {
				return false, err
			}
			if suppressed {
				continue
			}
		}
		return true, nil
	}
	dir := filepath.Join(runsBase, "needs-recovery")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if contracts.IsNeedsRecoverySentinelFilename(name) {
			return true, nil
		}
	}
	return false, nil
}

func needsRecoveryClearedMarkerExists(runsBase string, runID contracts.RunID) (bool, error) {
	if runID == "" {
		return false, nil
	}
	path := filepath.Join(runsBase, "needs-recovery", contracts.NeedsRecoverySentinelClearedFilename(runID))
	if _, err := os.Stat(path); err == nil {
		return true, nil
	} else if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	return false, nil
}

func sunsetDivergedBlockExists(runsBase string) (bool, error) {
	if _, err := os.Stat(filepath.Join(runsBase, divergedMarkerFile)); err == nil {
		return true, nil
	} else if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	return false, nil
}

func markStaleMarkerDiverged(runsBase string) error {
	from := filepath.Join(runsBase, markerFilename)
	to := filepath.Join(runsBase, divergedMarkerFile)
	if err := os.Rename(from, to); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return nil
}

func divergedMarkerExists(runsBase string) (bool, error) {
	if _, err := os.Stat(filepath.Join(runsBase, divergedMarkerFile)); err == nil {
		return true, nil
	} else if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	return false, nil
}

// ClearDivergedMarker removes the durable sunset divergence block after an
// operator has explicitly reconciled the stale sunset marker.
func ClearDivergedMarker(runsBase string) error {
	if err := os.Remove(filepath.Join(runsBase, divergedMarkerFile)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func markerSnapshot(runsBase string, transitions []Transition) (string, map[string]int, error) {
	registryPath := filepath.Join(runsBase, "rules-registry.jsonl")
	lines, err := readRegistryLines(registryPath)
	if err != nil {
		return "", nil, err
	}
	head := ""
	if len(lines) > 0 {
		head = lines[len(lines)-1].Sha256
	}
	entries := make([]contracts.RuleRegistryEntry, 0, len(lines))
	for _, line := range lines {
		entries = append(entries, line.Entry)
	}
	states, err := registryview.Build(entries)
	if err != nil {
		return "", nil, err
	}
	snapshot := make(map[string]int, len(transitions))
	for _, transition := range transitions {
		snapshot[transition.RuleID] = 0
		if state, ok := states[transition.RuleID]; ok {
			snapshot[transition.RuleID] = state.LastPromotionSeq
		}
	}
	return head, snapshot, nil
}

func markerSnapshotMatches(runsBase string, marker sunsetMarker) (bool, error) {
	if marker.RegistryHeadSHA == "" && len(marker.RuleSeqSnapshot) == 0 {
		return true, nil
	}
	head, snapshot, err := markerSnapshot(runsBase, marker.Transitions)
	if err != nil {
		return false, err
	}
	if head != marker.RegistryHeadSHA {
		return false, nil
	}
	if len(snapshot) != len(marker.RuleSeqSnapshot) {
		return false, nil
	}
	for ruleID, seq := range marker.RuleSeqSnapshot {
		if snapshot[ruleID] != seq {
			return false, nil
		}
	}
	return true, nil
}

func readMarker(path string) (sunsetMarker, error) {
	if marker, ok, err := readLegacyMarker(path); err != nil {
		return sunsetMarker{}, err
	} else if ok {
		return sunsetMarker{}, marker
	}
	marker, err := internalio.ReadJSON[sunsetMarker](path)
	if err != nil {
		return sunsetMarker{}, err
	}
	if marker.RecordedStartTime.IsZero() || marker.SunsetRunID == "" {
		return sunsetMarker{}, fmt.Errorf("archive: invalid stale marker contents")
	}
	return marker, nil
}

type legacySunsetMarker struct {
	RecordedStartTime time.Time
	SunsetRunID       string
}

func (m legacySunsetMarker) Error() string {
	return fmt.Sprintf("archive: legacy stale marker detected: sunset_run_id=%s", m.SunsetRunID)
}

func readLegacyMarker(path string) (legacySunsetMarker, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return legacySunsetMarker{}, false, err
		}
		return legacySunsetMarker{}, false, err
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		return legacySunsetMarker{}, false, nil
	}
	recordedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(lines[0]))
	if err != nil {
		return legacySunsetMarker{}, false, nil
	}
	sunsetRunID := strings.TrimSpace(lines[1])
	if sunsetRunID == "" {
		return legacySunsetMarker{}, false, nil
	}
	return legacySunsetMarker{
		RecordedStartTime: recordedAt,
		SunsetRunID:       sunsetRunID,
	}, true, nil
}

func syncRegistryIndex(runsBase, registryPath string, entry contracts.RuleRegistryEntry, result contracts.RegistryAppendResult) error {
	count, err := registryLineCount(registryPath)
	if err != nil {
		slog.Warn("archive: failed to inspect registry size for index sync", slog.String("error", err.Error()))
		return nil
	}
	if count < 1500 {
		return nil
	}
	indexPath := filepath.Join(runsBase, "rules-idempotency-index.jsonl")
	if err := internalio.SyncIdempotencyIndex(registryPath, indexPath, entry, result); err != nil {
		slog.Warn("archive: idempotency index sync failed; registry append remains committed", slog.String("error", err.Error()))
	}
	return nil
}

func registryLookupLines(path string) ([]registryLine, error) {
	lines, err := readRegistryLines(path)
	if err != nil {
		return nil, err
	}
	if len(lines) < 1800 {
		start := 0
		if len(lines) > internalio.RegistryTailScanN {
			start = len(lines) - internalio.RegistryTailScanN
		}
		return lines[start:], nil
	}
	indexPath := filepath.Join(filepath.Dir(path), "rules-idempotency-index.jsonl")
	indexEntries, _, err := internalio.EnsureVerifiedIdempotencyIndex(path, indexPath)
	if err != nil {
		slog.Warn("archive: idempotency index unavailable; falling back to tail scan", slog.String("error", err.Error()))
		start := 0
		if len(lines) > internalio.RegistryTailScanN {
			start = len(lines) - internalio.RegistryTailScanN
		}
		return lines[start:], nil
	}
	allowed := make(map[int64]string, len(indexEntries))
	for _, entry := range indexEntries {
		allowed[entry.RegistryOffset] = entry.RegistrySha256
	}
	filtered := make([]registryLine, 0, len(lines))
	for _, line := range lines {
		if sha, ok := allowed[line.Offset]; ok && sha == line.Sha256 {
			filtered = append(filtered, line)
		}
	}
	return filtered, nil
}

func readRegistryLines(path string) ([]registryLine, error) {
	return internalio.RegistryLines(path)
}

func registryLineCount(path string) (int, error) {
	lines, err := readRegistryLines(path)
	if err != nil {
		return 0, err
	}
	return len(lines), nil
}
