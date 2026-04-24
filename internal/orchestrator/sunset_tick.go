package orchestrator

import (
	"context"
	"fmt"
	"time"

	"github.com/nishimoto265/auto-improve/internal/archive"
	"github.com/nishimoto265/auto-improve/internal/config"
)

func RunSunsetTick(ctx context.Context) error {
	cfg, err := config.LoadDefault()
	if err != nil {
		return err
	}
	runsBase, err := cfg.RunsBase()
	if err != nil {
		return err
	}
	transitions, err := archive.BuildTransitionPlan(runsBase)
	if err != nil {
		return err
	}
	if len(transitions) == 0 {
		return nil
	}
	now := time.Now().UTC()
	_, err = archive.RunSunsetWithLock(ctx, archive.Opts{
		RunsBase:       runsBase,
		SunsetRunID:    fmt.Sprintf("sunset-%d", now.Unix()),
		Transitions:    transitions,
		Now:            func() time.Time { return now },
		RegistryHighAt: cfg.RegistryHighThreshold,
		RegistryCritAt: cfg.RegistryCriticalThreshold,
	})
	return err
}
