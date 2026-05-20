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
	// MinRateFraction is the floor as a fraction of BaseRate. Default: 0.1.
	MinRateFraction float64
	// MaxBurst is the token bucket capacity. Default: BaseRate.
	MaxBurst float64

	// RecoveryCooldown is how long after the last pause window ends before
	// additive rate recovery begins.
	// TUNING KNOB: 5s default is deliberately conservative — a short cooldown
	// lets a still-throttled account hammer EC2 again too quickly. Raise to 30s
	// or more for accounts that see sustained throttle bursts.
	RecoveryCooldown time.Duration
	// RecoveryStep is the tokens/s added per RecoveryInterval of elapsed time
	// once recovery is eligible. Default: 1 token/s per interval.
	RecoveryStep float64
	// RecoveryInterval is the clock period between recovery increments.
	// Default: 2s.
	RecoveryInterval time.Duration
}

// Limiter is the account-shared adaptive token bucket for one cloud account.
// All cohorts reconciling against one account share one Limiter.
//
// Rate recovery is entirely time-driven (A1): after recoveryCooldown has elapsed
// since the end of the last pause window, rate increases additively by
// recoveryStep per recoveryInterval of elapsed time up to baseRate.
// Any Backoff resets eligibility.
//
// Refill is suspended during an active pause window (A2): no tokens accrue
// while paused, so a burst cannot defeat the pause the instant it lifts.
// Backoff clamps the bucket to ≤1 token (prevents post-pause burst) and halves
// the rate (floor: minRate). Post-Backoff stall ≈ exactly d.
type Limiter struct {
	mu sync.Mutex

	tokens    float64
	maxTokens float64
	rate      float64
	baseRate  float64
	minRate   float64

	// Pause window set by Backoff; refill suspended, tokens clamped to ≤1.
	pauseUntil time.Time

	// Recovery eligibility: not before lastBackoffEnd + recoveryCooldown.
	lastBackoffEnd   time.Time
	recoveryCooldown time.Duration
	recoveryStep     float64
	recoveryInterval time.Duration

	// lastRefill tracks real-time token accrual; suspended during pause.
	lastRefill time.Time

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
	if cfg.RecoveryCooldown <= 0 {
		cfg.RecoveryCooldown = 5 * time.Second
	}
	if cfg.RecoveryStep <= 0 {
		cfg.RecoveryStep = 1.0
	}
	if cfg.RecoveryInterval <= 0 {
		cfg.RecoveryInterval = 2 * time.Second
	}
	now := clock()
	return &Limiter{
		tokens:           cfg.MaxBurst,
		maxTokens:        cfg.MaxBurst,
		rate:             cfg.BaseRate,
		baseRate:         cfg.BaseRate,
		minRate:          cfg.BaseRate * cfg.MinRateFraction,
		recoveryCooldown: cfg.RecoveryCooldown,
		recoveryStep:     cfg.RecoveryStep,
		recoveryInterval: cfg.RecoveryInterval,
		lastRefill:       now,
		clock:            clock,
	}
}

// Acquire blocks until the Limiter grants one permit or ctx is cancelled.
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

// Backoff is called on a FaultThrottle. It:
//   - halves rate (floor minRate),
//   - clamps tokens to ≤1 (no post-pause burst),
//   - sets a hard pause window of d (takes max across concurrent calls),
//   - records lastBackoffEnd = now+d to reset recovery eligibility.
func (l *Limiter) Backoff(d time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rate /= 2
	if l.rate < l.minRate {
		l.rate = l.minRate
	}
	if l.tokens > 1 {
		l.tokens = 1
	}
	end := l.clock().Add(d)
	if end.After(l.pauseUntil) {
		l.pauseUntil = end
	}
	if end.After(l.lastBackoffEnd) {
		l.lastBackoffEnd = end
	}
}

// tryAcquire attempts to consume one token without blocking.
// Returns 0 if successful; otherwise returns how long to sleep.
func (l *Limiter) tryAcquire() time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock()

	if now.Before(l.pauseUntil) {
		// Refill is suspended during the active pause window.
		return l.pauseUntil.Sub(now)
	}

	l.refill(now)
	l.applyRecovery(now)

	if l.tokens >= 1 {
		l.tokens--
		return 0
	}
	needed := 1.0 - l.tokens
	wait := time.Duration(needed / l.rate * float64(time.Second))
	if wait < time.Millisecond {
		wait = time.Millisecond
	}
	return wait
}

// refill adds tokens for elapsed time since lastRefill at the current rate.
// Must be called with l.mu held. Not called while paused.
func (l *Limiter) refill(now time.Time) {
	elapsed := now.Sub(l.lastRefill).Seconds()
	if elapsed <= 0 {
		l.lastRefill = now
		return
	}
	l.tokens += elapsed * l.rate
	if l.tokens > l.maxTokens {
		l.tokens = l.maxTokens
	}
	l.lastRefill = now
}

// applyRecovery additively increases rate toward baseRate once the cooldown
// after the last pause has elapsed. Each recoveryInterval of elapsed time
// adds recoveryStep tokens/s. Must be called with l.mu held.
func (l *Limiter) applyRecovery(now time.Time) {
	if l.rate >= l.baseRate {
		return
	}
	eligible := l.lastBackoffEnd.Add(l.recoveryCooldown)
	if now.Before(eligible) {
		return
	}
	intervals := now.Sub(eligible).Seconds() / l.recoveryInterval.Seconds()
	if intervals < 1 {
		return
	}
	l.rate += float64(int(intervals)) * l.recoveryStep
	if l.rate > l.baseRate {
		l.rate = l.baseRate
	}
}
