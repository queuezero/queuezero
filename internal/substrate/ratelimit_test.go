package substrate

import (
	"context"
	"testing"
	"time"
)

// fixedClock returns a clock pinned to *t; advance *t to move time.
func fixedClock(t *time.Time) func() time.Time {
	return func() time.Time { return *t }
}

// cfg returns a baseline LimiterConfig with short recovery settings for tests.
func cfg(base float64) LimiterConfig {
	return LimiterConfig{
		BaseRate:         base,
		MinRateFraction:  0.1,
		MaxBurst:         base,
		RecoveryCooldown: 5 * time.Second,
		RecoveryStep:     1.0,
		RecoveryInterval: 2 * time.Second,
	}
}

// ---- original 7 tests -------------------------------------------------------

func TestLimiter_Acquire_ImmediateWhenFull(t *testing.T) {
	now := time.Unix(1000, 0)
	l := NewLimiter(cfg(10), fixedClock(&now))
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		if err := l.Acquire(ctx); err != nil {
			t.Fatalf("acquire %d: unexpected error: %v", i, err)
		}
	}
	wait := l.tryAcquire()
	if wait == 0 {
		t.Fatal("expected non-zero wait with empty bucket, got 0")
	}
}

func TestLimiter_Acquire_RefillOverTime(t *testing.T) {
	now := time.Unix(1000, 0)
	l := NewLimiter(cfg(1), fixedClock(&now))
	// Drain the bucket.
	_ = l.tryAcquire()
	// Advance 1 second — one token should refill.
	now = now.Add(time.Second)
	if err := l.Acquire(context.Background()); err != nil {
		t.Fatalf("acquire after 1s: %v", err)
	}
}

func TestLimiter_Backoff_HalvesRate(t *testing.T) {
	now := time.Unix(1000, 0)
	l := NewLimiter(cfg(20), fixedClock(&now))
	before := l.rate
	l.Backoff(0)
	if l.rate != before/2 {
		t.Fatalf("rate after backoff: got %.1f want %.1f", l.rate, before/2)
	}
	// Repeated Backoff must not go below minRate.
	for i := 0; i < 20; i++ {
		l.Backoff(0)
	}
	if l.rate < l.minRate {
		t.Fatalf("rate %.3f fell below minRate %.3f", l.rate, l.minRate)
	}
}

func TestLimiter_Backoff_SetsPause(t *testing.T) {
	now := time.Unix(1000, 0)
	l := NewLimiter(cfg(10), fixedClock(&now))
	pause := 200 * time.Millisecond
	l.Backoff(pause)
	wait := l.tryAcquire()
	if wait < pause {
		t.Fatalf("wait %v < pause %v", wait, pause)
	}
}

func TestLimiter_Recovery_IncreasesRateOverTime(t *testing.T) {
	now := time.Unix(1000, 0)
	c := cfg(20)
	c.RecoveryCooldown = 5 * time.Second
	c.RecoveryStep = 2.0
	c.RecoveryInterval = 2 * time.Second
	l := NewLimiter(c, fixedClock(&now))

	// Drive rate to floor via many Backoffs.
	for i := 0; i < 50; i++ {
		l.Backoff(0)
	}
	floorRate := l.rate
	pauseEnd := l.lastBackoffEnd

	// Move clock past cooldown and two recovery intervals.
	now = pauseEnd.Add(5*time.Second + 4*time.Second + 1)
	// Trigger applyRecovery via tryAcquire.
	_ = l.tryAcquire()
	if l.rate <= floorRate {
		t.Fatalf("rate %.3f did not recover above floor %.3f", l.rate, floorRate)
	}
}

func TestLimiter_Acquire_Cancellation(t *testing.T) {
	now := time.Unix(1000, 0)
	l := NewLimiter(cfg(1), fixedClock(&now))
	l.tokens = 0
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := l.Acquire(ctx); err == nil {
		t.Fatal("expected cancellation error, got nil")
	}
}

func TestLimiter_ConcurrentAcquire(t *testing.T) {
	l := NewLimiter(LimiterConfig{BaseRate: 100, MaxBurst: 100}, nil)
	ctx := context.Background()
	done := make(chan struct{}, 50)
	for i := 0; i < 50; i++ {
		go func() {
			_ = l.Acquire(ctx)
			done <- struct{}{}
		}()
	}
	deadline := time.After(5 * time.Second)
	for i := 0; i < 50; i++ {
		select {
		case <-done:
		case <-deadline:
			t.Fatal("timeout waiting for concurrent acquires")
		}
	}
}

func TestLimiter_Backoff_AccumulatesMax(t *testing.T) {
	cases := []struct {
		name    string
		pauses  []time.Duration
		wantMin time.Duration
	}{
		{"single 100ms", []time.Duration{100 * time.Millisecond}, 100 * time.Millisecond},
		{"two: 50ms then 200ms", []time.Duration{50 * time.Millisecond, 200 * time.Millisecond}, 200 * time.Millisecond},
		{"three ascending", []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 30 * time.Millisecond}, 30 * time.Millisecond},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Unix(1000, 0)
			l := NewLimiter(cfg(10), fixedClock(&now))
			for _, p := range tc.pauses {
				l.Backoff(p)
			}
			wait := l.tryAcquire()
			if wait < tc.wantMin {
				t.Fatalf("wait %v < wantMin %v", wait, tc.wantMin)
			}
		})
	}
}

// ---- 3 new tests (A4) -------------------------------------------------------

// A4a: recovery interrupted by Backoff resets cooldown.
func TestLimiter_Recovery_InterruptedByBackoff(t *testing.T) {
	now := time.Unix(1000, 0)
	c := cfg(20)
	c.RecoveryCooldown = 5 * time.Second
	c.RecoveryStep = 2.0
	c.RecoveryInterval = 2 * time.Second
	l := NewLimiter(c, fixedClock(&now))

	// Drive rate down.
	for i := 0; i < 10; i++ {
		l.Backoff(0)
	}
	afterBackoffsRate := l.rate
	pauseEnd := l.lastBackoffEnd

	// Advance past cooldown + 2 intervals so recovery partially happens.
	now = pauseEnd.Add(5*time.Second + 4*time.Second + 1)
	_ = l.tryAcquire()
	partialRate := l.rate
	if partialRate <= afterBackoffsRate {
		t.Fatalf("expected partial recovery: got %.3f <= %.3f", partialRate, afterBackoffsRate)
	}

	// Inject Backoff — rate halves from the PARTIALLY-RECOVERED value, cooldown reset.
	l.Backoff(100 * time.Millisecond)
	wantRate := partialRate / 2
	if l.rate > wantRate+0.001 || l.rate < wantRate-0.001 {
		t.Fatalf("rate after interrupting backoff: got %.3f want %.3f", l.rate, wantRate)
	}
	// Cooldown must be reset: last backoff end is now, so recovery is not yet eligible.
	newPauseEnd := l.lastBackoffEnd
	eligibleAt := newPauseEnd.Add(c.RecoveryCooldown)
	if now.After(eligibleAt) || now.Equal(eligibleAt) {
		t.Fatal("recovery should not be eligible immediately after new Backoff")
	}
}

// A4b: no tokens accrue during an active pause window.
func TestLimiter_NoPauseRefill(t *testing.T) {
	now := time.Unix(1000, 0)
	l := NewLimiter(cfg(10), fixedClock(&now))
	// Drain the bucket.
	for {
		if l.tryAcquire() != 0 {
			break
		}
	}
	tokensBefore := l.tokens

	pause := 500 * time.Millisecond
	l.Backoff(pause)
	// Advance clock well into the pause window — tokens must not accrue.
	now = now.Add(400 * time.Millisecond)
	_ = l.tryAcquire() // internally calls refill, but pause is active so no-op
	if l.tokens > tokensBefore+0.001 {
		t.Fatalf("tokens accrued during pause: %.3f > %.3f", l.tokens, tokensBefore)
	}
}

