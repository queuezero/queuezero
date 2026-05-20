package substrate

import (
	"context"
	"testing"
	"time"
)

// advancingClock returns a clock that advances by step every call.
func advancingClock(start time.Time, step time.Duration) func() time.Time {
	t := start
	return func() time.Time {
		now := t
		t = t.Add(step)
		return now
	}
}

// fixedClock returns a clock pinned to t; callers advance via the pointer.
func fixedClock(t *time.Time) func() time.Time {
	return func() time.Time { return *t }
}

func TestLimiter_Acquire_ImmediateWhenFull(t *testing.T) {
	now := time.Unix(0, 0)
	l := NewLimiter(LimiterConfig{BaseRate: 10, MaxBurst: 10}, fixedClock(&now))
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		if err := l.Acquire(ctx); err != nil {
			t.Fatalf("acquire %d: unexpected error: %v", i, err)
		}
	}
	// bucket is now empty; tryAcquire must return non-zero wait without blocking
	wait := l.tryAcquire()
	if wait == 0 {
		t.Fatal("expected non-zero wait with empty bucket, got 0")
	}
}

func TestLimiter_Acquire_RefillOverTime(t *testing.T) {
	// One token per second; start with 0 tokens; advance clock by 1 s each call.
	step := time.Second
	start := time.Unix(1_000, 0)
	l := NewLimiter(LimiterConfig{BaseRate: 1, MaxBurst: 1}, advancingClock(start, step))
	// First call: refill happens (elapsed=1s), 1 token arrives, consumed.
	ctx := context.Background()
	if err := l.Acquire(ctx); err != nil {
		t.Fatalf("acquire after 1s: %v", err)
	}
}

func TestLimiter_Backoff_HalvesRate(t *testing.T) {
	now := time.Unix(0, 0)
	l := NewLimiter(LimiterConfig{BaseRate: 20, MinRateFraction: 0.1}, fixedClock(&now))
	before := l.rate
	l.Backoff(0)
	if l.rate != before/2 {
		t.Fatalf("rate after 1 backoff: got %.1f want %.1f", l.rate, before/2)
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
	base := time.Unix(1_000, 0)
	l := NewLimiter(LimiterConfig{BaseRate: 10, MaxBurst: 10}, fixedClock(&base))
	// Drain bucket.
	for i := 0; i < 10; i++ {
		_ = l.tryAcquire()
	}
	pause := 200 * time.Millisecond
	l.Backoff(pause)
	wait := l.tryAcquire()
	if wait < pause {
		t.Fatalf("wait %v is less than pause %v", wait, pause)
	}
}

func TestLimiter_Recovery_NudgesRateUp(t *testing.T) {
	// 1s step; threshold=5 so recovery triggers every 5 acquires.
	step := time.Second
	start := time.Unix(0, 0)
	l := NewLimiter(LimiterConfig{
		BaseRate:          20,
		MinRateFraction:   0.1,
		MaxBurst:          100,
		RecoveryThreshold: 5,
	}, advancingClock(start, step))

	// Slam rate to floor.
	for i := 0; i < 50; i++ {
		l.Backoff(0)
	}
	floorRate := l.rate

	// Drive enough successful acquires to trigger several recovery nudges.
	ctx := context.Background()
	for i := 0; i < 50; i++ {
		if err := l.Acquire(ctx); err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
	}
	if l.rate <= floorRate {
		t.Fatalf("rate %.3f did not recover above floor %.3f", l.rate, floorRate)
	}
}

func TestLimiter_Acquire_Cancellation(t *testing.T) {
	now := time.Unix(0, 0)
	l := NewLimiter(LimiterConfig{BaseRate: 1, MaxBurst: 0}, fixedClock(&now))
	// MaxBurst=0 is clamped to BaseRate; drain immediately.
	l.tokens = 0
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := l.Acquire(ctx)
	if err == nil {
		t.Fatal("expected cancellation error, got nil")
	}
}

func TestLimiter_ConcurrentAcquire(t *testing.T) {
	// Smoke test: 50 goroutines all Acquire from one Limiter with a generous
	// budget. No deadlock, no panic, no data race (run with -race).
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

// Table-driven: verify that Backoff accumulates correctly across multiple calls
// and that the pause window is always the maximum of concurrent callers.
func TestLimiter_Backoff_AccumulatesMax(t *testing.T) {
	cases := []struct {
		name    string
		pauses  []time.Duration
		wantMin time.Duration // wait must be >= this
	}{
		{"single 100ms", []time.Duration{100 * time.Millisecond}, 100 * time.Millisecond},
		{"two: 50ms then 200ms", []time.Duration{50 * time.Millisecond, 200 * time.Millisecond}, 200 * time.Millisecond},
		{"three ascending", []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 30 * time.Millisecond}, 30 * time.Millisecond},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := time.Unix(1_000, 0)
			l := NewLimiter(LimiterConfig{BaseRate: 10, MaxBurst: 10}, fixedClock(&base))
			// Drain.
			for i := 0; i < 10; i++ {
				_ = l.tryAcquire()
			}
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
