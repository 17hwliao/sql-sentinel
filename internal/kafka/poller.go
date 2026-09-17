package kafka

import (
	"context"
	"errors"
	"time"
)

// PollResult contains only safe operational counters. Event payloads remain
// in the control plane and are never emitted by this loop.
type PollResult struct {
	Published int
	Err       error
}

// RunPolling turns one Relay scan into a cancellable service loop. The
// outbox's available_at timestamp controls retry eligibility; interval only
// controls how frequently the durable store is scanned.
func RunPolling(ctx context.Context, interval time.Duration, runOnce func(context.Context) (int, error), observe func(PollResult)) error {
	if ctx == nil {
		return errors.New("relay context is required")
	}
	if interval <= 0 {
		return errors.New("relay poll interval must be positive")
	}
	if runOnce == nil {
		return errors.New("relay run-once function is required")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		published, err := runOnce(ctx)
		if observe != nil {
			observe(PollResult{Published: published, Err: err})
		}
		if ctx.Err() != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
