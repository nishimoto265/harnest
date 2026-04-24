package orchestrator

import (
	"context"
	"fmt"
	"time"

	"github.com/nishimoto265/auto-improve/internal/archive"
	"github.com/nishimoto265/auto-improve/internal/config"
)

type SunsetTickOptions struct {
	Force bool
}

func RunSunsetTick(ctx context.Context) error {
	return RunSunsetTickWithOptions(ctx, SunsetTickOptions{})
}

func RunSunsetTickWithOptions(ctx context.Context, tickOpts SunsetTickOptions) error {
	cfg, err := config.LoadDefault()
	if err != nil {
		return err
	}
	runsBase, err := cfg.RunsBase()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	_, err = archive.RunSunsetWithLock(ctx, archive.Opts{
		RunsBase:       runsBase,
		SunsetRunID:    fmt.Sprintf("sunset-%d", now.Unix()),
		AutoPlan:       true,
		Force:          tickOpts.Force,
		Now:            func() time.Time { return now },
		RegistryHighAt: cfg.RegistryHighThreshold,
		RegistryCritAt: cfg.RegistryCriticalThreshold,
	})
	return err
}
