// Package archive contains the rule sunset business logic described in
// docs/design/io-contracts.md §archive.
package archive

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
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
	AutoPlan    bool
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
		if diverged, err := divergedMarkerExists(opts.RunsBase); err != nil {
			return result, err
		} else if diverged {
			return result, ErrStaleMarkerDiverged
		}
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
		if diverged, err := divergedMarkerExists(opts.RunsBase); err != nil {
			return result, err
		} else if diverged {
			return result, ErrStaleMarkerDiverged
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

	if diverged, err := divergedMarkerExists(opts.RunsBase); err != nil {
		return result, err
	} else if diverged {
		return result, ErrStaleMarkerDiverged
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

func RunSunsetWithLock(ctx context.Context, opts Opts) (Result, error) {
	opts = applyDefaults(opts)
	if opts.RunsBase == "" {
		return Result{}, errors.New("archive: runs_base is required")
	}
	if opts.SunsetRunID == "" {
		return Result{}, errors.New("archive: sunset_run_id is required")
	}
	if diverged, err := divergedMarkerExists(opts.RunsBase); err != nil {
		return Result{}, err
	} else if diverged {
		return Result{}, ErrStaleMarkerDiverged
	}
	if blocked, err := sentinelExists(opts.RunsBase); err != nil {
		return Result{}, err
	} else if blocked {
		return Result{}, nil
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

	if diverged, err := divergedMarkerExists(opts.RunsBase); err != nil {
		return Result{}, err
	} else if diverged {
		return Result{}, ErrStaleMarkerDiverged
	}
	if blocked, err := sentinelExists(opts.RunsBase); err != nil {
		return Result{}, err
	} else if blocked {
		return Result{}, nil
	}

	if err := reconcileStaleMarker(ctx, opts); err != nil {
		return Result{}, err
	}
	if diverged, err := divergedMarkerExists(opts.RunsBase); err != nil {
		return Result{}, err
	} else if diverged {
		return Result{}, ErrStaleMarkerDiverged
	}
	if blocked, err := sentinelExists(opts.RunsBase); err != nil {
		return Result{}, err
	} else if blocked {
		return Result{}, nil
	}
	if opts.AutoPlan {
		transitions, err := BuildTransitionPlan(opts.RunsBase)
		if err != nil {
			return Result{}, err
		}
		opts.Transitions = transitions
	}
	if len(opts.Transitions) == 0 {
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

	if err := writeMarker(opts); err != nil {
		return Result{}, err
	}
	result, runErr := RunSunset(ctx, opts)
	if errors.Is(runErr, errBlockedBySentinel) {
		if len(result.AppendedOpIDs) == 0 {
			if err := removeSunsetMarker(opts.RunsBase); err != nil {
				return result, err
			}
		}
		return result, nil
	}
	if runErr != nil {
		if len(result.AppendedOpIDs) == 0 {
			if err := removeSunsetMarker(opts.RunsBase); err != nil {
				return result, errors.Join(runErr, err)
			}
		}
		return result, runErr
	}
	if err := writeLastSunsetAt(opts.RunsBase, opts.Now()); err != nil {
		return result, err
	}
	if err := removeSunsetMarker(opts.RunsBase); err != nil {
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
