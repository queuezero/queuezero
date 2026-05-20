package cohort

import (
	"context"
	"time"
)

// Reconciler converges one Cohort against eventually-consistent infrastructure.
//
// It is the core loop: declare intent (a set of named entities), observe
// actual state tolerating consistency gaps, diff PER-ENTITY, correct, repeat
// until the cohort converges or a phase budget runs out. This is ASG done
// right and visible — and its unit is the named entity, never a count.
//
// The Reconciler holds NO provider or domain knowledge. Everything it needs
// arrives through the ports interfaces.
type Reconciler struct {
	Actuator   Actuator
	Observer   Observer
	Classifier Classifier
	Enroller   Enroller
	Assembler  Assembler

	// Limiter gates outbound provider mutations. It is account-shared: all
	// cohorts reconciling against one account share one budget, because
	// throttling is a property of the account, not the call site.
	Limiter RateLimiter

	// Clock is injectable for deterministic tests.
	Clock func() time.Time
}

// RateLimiter is the account-shared client-side throttle. On a FaultThrottle
// the whole limiter backs off, not just one caller.
type RateLimiter interface {
	Acquire(ctx context.Context) error
	Backoff(d time.Duration)
}

// Reconcile drives a cohort to PhaseReady or returns an Outcome explaining,
// per entity, exactly where and why it stopped.
//
// CONTRACT (implement to this; see docs/ARCHITECTURE.md §7):
//
//  1. Per-entity phases run concurrently across members. Each member advances
//     PhaseLaunchAcked -> PhaseRunning -> PhaseEnrolled independently.
//
//  2. Fault handling is table-driven via Classifier — never ad-hoc string
//     matching:
//       - RetryableConsistency: bounded retry, short backoff, tight ceiling.
//       - Throttle: Limiter.Backoff; exponential + jitter.
//       - CapacityExhausted: do NOT retry in place — call advanceRung; if the
//         approved chain is exhausted, the entity fails. Record the attempt.
//       - Terminal: fail the entity immediately, verbatim code preserved.
//       - Ambiguous: must not occur here — substrate.Client collapses it via
//         the idempotency token before classification reaches this loop.
//
//  3. Phase budgets are enforced individually. A member that blows
//     PhaseLaunchAcked failed for a throttle/API reason; one that blows
//     PhaseEnrolled failed for a bootstrap/network/storage reason. The Record
//     names which. Never collapse these into a generic "node failed".
//
//  4. PhaseCohortBarrier: wait until every member (or MinViable members) has
//     reached PhaseEnrolled. The instant the gate becomes UNSATISFIABLE —
//     enough members terminally failed that MinViable can't be met — fast-fail
//     the WHOLE cohort. Do not leave healthy members idling on a straggler
//     that will never arrive; that is the expensive, illegible failure mode
//     queuezero exists to kill.
//
//  5. PhaseCohortAssembly: with full membership known-good and known-
//     simultaneous, invoke Assembler.Assemble exactly once over all member
//     Observations. The Reconciler learns only pass/fail — it never inspects
//     topology. A 1-cohort skips straight through (no-op Assembler).
//
//  6. Every entity, success or failure, ends with a populated Record.
//
//  7. On any cohort-level failure, Drain the partial fleet so nothing idles
//     and bills. Suspend/teardown reconciliation (the sweeper) is a separate
//     path; see internal/substrate.
func (r *Reconciler) Reconcile(ctx context.Context, c Cohort) (Outcome, error) {
	// TODO(phase-1): implement the loop to the contract above.
	// Suggested internal structure:
	//   - tracker per EntityID holding current Phase, current Rung index,
	//     accumulated []Attempt, and a *Fault when terminal.
	//   - errgroup over members for phases 1-3.
	//   - a barrier check that recomputes satisfiability after every member
	//     transition, so fast-fail triggers the instant the gate dies.
	//   - assemble() guarded by IsCollective() / MinViable.
	panic("cohort.Reconciler.Reconcile: not yet implemented — see CONTRACT in reconcile.go")
}

// advanceRung selects the next rung from the entity's approved fallback chain
// after a FaultCapacityExhausted. It returns false when the chain is
// exhausted. queuezero NEVER substitutes a rung outside the chain declared in
// partitions.yaml — that is the "legible and approved fallback" guarantee.
func (r *Reconciler) advanceRung(intent *EntityIntent, chain []Rung) bool {
	// TODO(phase-1): track chain position on the tracker, not the intent.
	panic("cohort.Reconciler.advanceRung: not yet implemented")
}

// Drain marks the given entities for teardown after a cohort failure, so no
// member is left Running-but-useless and billing.
func (r *Reconciler) Drain(ctx context.Context, ids []EntityID) error {
	// TODO(phase-1): Actuator.Terminate (or Stop, if a warm pool wants them)
	// each entity; tolerate already-absent.
	panic("cohort.Reconciler.Drain: not yet implemented")
}
