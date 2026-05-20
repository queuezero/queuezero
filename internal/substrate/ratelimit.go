package substrate

import (
	"context"
	"sync"
	"time"
)

// LimiterConfig tunes a Limiter; zero values use safe defaults.
type LimiterConfig struct {
	// BaseRate is the unconstrained refill rate in tokens/s. Default: 20.
	BaseRate float64
	// MinRateFraction is the floor as a fraction of BaseRate after sustained
	// Backoff calls. Default: 0.1 (10 % of base).
	MinRateFraction float64
	// MaxBurst is the token bucket capacity (burst ceiling). Default: BaseRate.
	MaxBurst float64
	// RecoveryThreshold is the number of successful Acquires before the rate
	// nudges back toward BaseRate. Default: 10.
	RecoveryThreshold int
}

// Limiter is the account-shared, adaptive client-side throttle. Throttling is
// a property of the ACCOUNT, not the call site: all cohorts reconciling
// against one account share one Limiter, so a Throttle fault backs the whole
// client off rather than letting each goroutine hammer in parallel (which is
// precisely what ParallelCluster gets wrong).
type Limiter struct {
	mu sync.Mutex

	tokens    float64
	maxTokens float64
	rate      float64 // current tokens/s
	baseRate  float64 // ceiling: rate recovers toward this after backoffs
	minRate   float64 // floor: rate never falls below this

	// pauseUntil is set by Backoff; tryAcquire returns the remaining wait until
	// this time even if tokens are available. This is the hard pause window.
	pauseUntil time.Time
	lastRefill time.Time

	// successSince counts successful Acquires since the last Backoff. When it
	// reaches recoveryThreshold, rate is nudged toward baseRate and the counter
	// resets. This is "recovers gradually on sustained success."
	successSince      int
	recoveryThreshold int

	clock func() time.Time
}

// NewLimiter creates an account-shared adaptive token bucket.
// Pass clock=nil to use time.Now; inject a deterministic clock in tests.
func NewLimiter(cfg LimiterConfig, clock func() time.Time) *Limiter {
	if clock == nil {
		clock = time.Now
	}
	if cfg.BaseRate <= 0 {
		cfg.BaseRate = 20
	}
	if cfg.MinRateFraction <= 0 {
		cfg.MinRateFraction = 0.1
	}
	if cfg.MaxBurst <= 0 {
		cfg.MaxBurst = cfg.BaseRate
	}
	if cfg.RecoveryThreshold <= 0 {
		cfg.RecoveryThreshold = 10
	}
	now := clock()
	return &Limiter{
		tokens:            cfg.MaxBurst,
		maxTokens:         cfg.MaxBurst,
		rate:              cfg.BaseRate,
		baseRate:          cfg.BaseRate,
		minRate:           cfg.BaseRate * cfg.MinRateFraction,
		recoveryThreshold: cfg.RecoveryThreshold,
		lastRefill:        now,
		clock:             clock,
	}
}

// Acquire blocks until the Limiter grants one permit. It respects ctx
// cancellation, which returns ctx.Err() without consuming a token.
func (l *Limiter) Acquire(ctx context.Context) error {
	for {
		wait := l.tryAcquire()
		if wait == 0 {
			return nil
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}

// Backoff is called on a FaultThrottle to slow the whole client.
// d is the caller-computed backoff window (exponential + jitter from the
// reconciler). Backoff: halves the refill rate (floor minRate), drains the
// bucket, and sets a hard pause for d before the next Acquire can proceed.
func (l *Limiter) Backoff(d time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rate /= 2
	if l.rate < l.minRate {
		l.rate = l.minRate
	}
	l.tokens = 0
	if d > 0 {
		resume := l.clock().Add(d)
		if resume.After(l.pauseUntil) {
			l.pauseUntil = resume
		}
	}
	l.successSince = 0
}

// tryAcquire attempts to take one token without blocking.
// Returns 0 if a token was acquired; otherwise returns how long to wait.
func (l *Limiter) tryAcquire() time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock()
	l.refill(now)
	if now.Before(l.pauseUntil) {
		return l.pauseUntil.Sub(now)
	}
	if l.tokens >= 1 {
		l.tokens--
		l.successSince++
		if l.successSince >= l.recoveryThreshold {
			l.recoverRate()
			l.successSince = 0
		}
		return 0
	}
	needed := 1.0 - l.tokens
	wait := time.Duration(needed / l.rate * float64(time.Second))
	if wait < time.Millisecond {
		wait = time.Millisecond
	}
	return wait
}

// refill adds tokens for elapsed time at the current rate, capped at maxTokens.
// Must be called with l.mu held.
func (l *Limiter) refill(now time.Time) {
	elapsed := now.Sub(l.lastRefill).Seconds()
	if elapsed <= 0 {
		return
	}
	l.tokens += elapsed * l.rate
	if l.tokens > l.maxTokens {
		l.tokens = l.maxTokens
	}
	l.lastRefill = now
}

// recoverRate nudges rate toward baseRate by 25 % of the remaining gap.
// Called every recoveryThreshold successful Acquires.
// Must be called with l.mu held.
func (l *Limiter) recoverRate() {
	gap := l.baseRate - l.rate
	if gap <= 0 {
		return
	}
	l.rate += gap * 0.25
	if l.rate > l.baseRate {
		l.rate = l.baseRate
	}
}
