package poll

import (
	"context"
	"time"
)

// Due reports whether a target has never been polled or its interval has elapsed.
func Due(last time.Time, interval time.Duration, now time.Time) bool {
	return last.IsZero() || now.Sub(last) >= interval
}

// Wait waits for a duration or cancellation, whichever comes first.
func Wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
