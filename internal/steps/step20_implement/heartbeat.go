package step20_implement

import (
	"context"
	"time"

	internalio "github.com/nishimoto265/auto-improve/internal/io"
)

func startHeartbeat(ctx context.Context, interval time.Duration, heartbeatPath string, store *resumeStateStore, now func() time.Time) (func(), error) {
	beat := func() error {
		at := now().UTC()
		if err := internalio.WriteAtomic(heartbeatPath, []byte{}); err != nil {
			return err
		}
		return store.UpdateLastHeartbeat(at)
	}
	if err := beat(); err != nil {
		return nil, err
	}

	hbCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-ticker.C:
				_ = beat()
			}
		}
	}()

	return func() {
		cancel()
		<-done
	}, nil
}
