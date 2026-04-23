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
	_, err = archive.RunSunsetWithLock(ctx, archive.Opts{
		RunsBase:       runsBase,
		SunsetRunID:    fmt.Sprintf("sunset-%d", time.Now().UTC().Unix()),
		Now:            func() time.Time { return time.Now().UTC() },
		RegistryHighAt: cfg.RegistryHighThreshold,
		RegistryCritAt: cfg.RegistryCriticalThreshold,
	})
	return err
}
