package archive

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
	"github.com/nishimoto265/auto-improve/internal/registryview"
)

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

func removeSunsetMarker(runsBase string) error {
	if err := os.Remove(filepath.Join(runsBase, markerFilename)); err != nil && !os.IsNotExist(err) {
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
