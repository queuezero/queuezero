package substrate

import (
	"context"
	"time"
)

// Limiter is the account-shared, adaptive client-side throttle. Throttling is
// a property of the ACCOUNT, not the call site: all cohorts reconciling
// against one account share one Limiter, so a Throttle fault backs the whole
// client off rather than letting each goroutine hammer in parallel (which is
// precisely what ParallelCluster gets wrong).
type Limiter struct {
	// TODO(phase-1): token bucket with adaptive refill. On Backoff, shrink the
	// effective rate; recover it gradually on sustained success.
}

// Acquire blocks until the limiter permits one provider call.
func (l *Limiter) Acquire(ctx context.Context) error {
	// TODO(phase-1): implement.
	panic("substrate.Limiter.Acquire: not yet implemented")
}

// Backoff is called on a FaultThrottle to slow the whole client.
func (l *Limiter) Backoff(d time.Duration) {
	// TODO(phase-1): implement.
	panic("substrate.Limiter.Backoff: not yet implemented")
}
