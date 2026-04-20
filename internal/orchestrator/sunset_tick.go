package orchestrator

import "context"

func RunSunsetTick(ctx context.Context) error {
	return ctx.Err()
}
