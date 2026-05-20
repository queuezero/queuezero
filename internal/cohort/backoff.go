package cohort

import (
	"math"
	"math/rand"
	"time"
)

// BackoffPolicy computes exponential-with-jitter durations for bounded retries.
// It is provider-agnostic: the reconciler uses it for RetryableConsistency
// retries; substrate constructs a separate instance (longer cap) for the
// throttle path that feeds Limiter.Backoff.
//
// This file imports nothing provider- or scheduler-specific.
type BackoffPolicy struct {
	Base   time.Duration // duration for attempt 0. Default: 100ms.
	Cap    time.Duration // maximum pre-jitter duration. Default: 30s.
	Jitter float64       // fraction of computed value added as uniform noise. Default: 0.25.
}

// DefaultBackoffPolicy returns a policy for RetryableConsistency retries in
// the reconciler: 100ms base, 30s cap, 25% jitter.
func DefaultBackoffPolicy() BackoffPolicy {
	return BackoffPolicy{
		Base:   100 * time.Millisecond,
		Cap:    30 * time.Second,
		Jitter: 0.25,
	}
}

// Duration returns the backoff duration for attempt (0-indexed).
// Computes base × 2^attempt, caps at Cap, then adds uniform jitter in
// [0, Jitter × capped). Result is bounded by Cap × (1 + Jitter).
func (p BackoffPolicy) Duration(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	d := time.Duration(float64(p.Base) * math.Pow(2, float64(attempt)))
	if d > p.Cap || d < 0 { // d<0 guards int64 overflow at high attempt counts
		d = p.Cap
	}
	jitter := time.Duration(float64(d) * p.Jitter * rand.Float64())
	return d + jitter
}
