package poll

import (
	"context"
	"testing"
	"time"
)

func TestScheduleAndCancellation(t *testing.T) {
	now := time.Now()
	if !Due(time.Time{}, time.Hour, now) || Due(now, time.Hour, now) || !Due(now.Add(-time.Hour), time.Hour, now) {
		t.Fatal("incorrect due calculation")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err := Wait(ctx, time.Hour); err != context.Canceled || time.Since(start) > 2*time.Second {
		t.Fatalf("wait: %v", err)
	}
	if err := Wait(ctx, 0); err != nil {
		t.Fatal(err)
	}
}
