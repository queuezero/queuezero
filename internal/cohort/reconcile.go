package cohort

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
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

// entityTracker holds per-entity reconciliation state.
type entityTracker struct {
	mu        sync.Mutex
	intent    EntityIntent
	phase     Phase
	attempts  []Attempt
	terminal  *Fault // set when this entity cannot recover
	cancelled *CohortCancelInfo // set when cohort fast-fails around this healthy entity
	obs       Observation
	startedAt time.Time
}

func (t *entityTracker) addAttempt(rung Rung, phase Phase, f *Fault) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.attempts = append(t.attempts, Attempt{Rung: rung, Phase: phase, Fault: f, At: time.Now()})
}

func (t *entityTracker) setTerminal(phase Phase, f Fault) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.phase = phase
	cp := f
	t.terminal = &cp
}

func (t *entityTracker) setPhase(p Phase) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.phase = p
}

func (t *entityTracker) getPhase() Phase {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.phase
}

func (t *entityTracker) isTerminal() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.terminal != nil
}

func (t *entityTracker) setCancelled(info CohortCancelInfo) {
	t.mu.Lock()
	defer t.mu.Unlock()
	cp := info
	cp.SurvivorPhase = t.phase
	t.cancelled = &cp
}

// Reconcile drives a cohort to PhaseReady or returns an Outcome explaining,
// per entity, exactly where and why it stopped.
func (r *Reconciler) Reconcile(ctx context.Context, c Cohort) (Outcome, error) {
	now := r.clock()
	minViable := c.MinViable
	if minViable == 0 {
		minViable = len(c.Members)
	}

	// Build per-entity trackers.
	trackers := make([]*entityTracker, len(c.Members))
	for i, m := range c.Members {
		trackers[i] = &entityTracker{
			intent:    m,
			phase:     PhaseLaunchAcked,
			startedAt: now,
		}
	}

	// Phase 1–3: per-entity, concurrent.
	// fastFailCancel cancels all sibling goroutines the instant the gate goes
	// unsatisfiable. culpritOnce ensures only the first failing entity records
	// itself as culprit.
	fastFailCtx, fastFailCancel := context.WithCancel(ctx)
	defer fastFailCancel()

	var mu sync.Mutex
	failedCount := 0
	var culprit culpritInfo // first entity that made the gate unsatisfiable

	eg, egCtx := errgroup.WithContext(fastFailCtx)
	for _, tr := range trackers {
		tr := tr
		eg.Go(func() error {
			r.reconcileEntity(egCtx, tr, c.Budget)
			if tr.isTerminal() {
				mu.Lock()
				failedCount++
				satisfiable := (len(trackers) - failedCount) >= minViable
				if !satisfiable && culprit.id == "" {
					// Capture the culprit before cancelling siblings.
					tr.mu.Lock()
					culprit = culpritInfo{
						id:    tr.intent.ID,
						fault: *tr.terminal,
						phase: tr.phase,
						at:    time.Now(),
					}
					tr.mu.Unlock()
				}
				mu.Unlock()
				if !satisfiable {
					fastFailCancel()
				}
			}
			return nil
		})
	}
	_ = eg.Wait()

	// Count survivors (non-terminal) now that all goroutines have returned.
	enrolledCount := 0
	for _, tr := range trackers {
		if !tr.isTerminal() {
			enrolledCount++
		}
	}
	gateSatisfied := enrolledCount >= minViable

	outcome := Outcome{
		Cohort:  c.ID,
		Records: make(map[EntityID]Record, len(trackers)),
	}

	if !gateSatisfied {
		// Drain any surviving instances so nothing idles and bills.
		var surviving []EntityID
		for _, tr := range trackers {
			if !tr.isTerminal() && tr.obs.ProviderID != "" {
				surviving = append(surviving, tr.intent.ID)
			}
		}
		if len(surviving) > 0 {
			_ = r.Drain(ctx, surviving)
		}

		// Mark survivors with CohortCancelled — not a fault, a distinct outcome.
		// Each survivor records the phase IT was at when cancelled.
		cancelBase := CohortCancelInfo{
			CulpritID:    culprit.id,
			CulpritFault: culprit.fault,
			CulpritPhase: culprit.phase,
			At:           culprit.at,
		}
		for _, tr := range trackers {
			if !tr.isTerminal() {
				tr.setCancelled(cancelBase) // SurvivorPhase set from tr.phase inside setCancelled
			}
		}

		for _, tr := range trackers {
			outcome.Records[tr.intent.ID] = r.buildRecord(tr, c.ID)
		}
		outcome.Ready = false
		return outcome, nil
	}

	// Phase 4 passed: barrier satisfied. Run assembly if collective.
	if c.IsCollective() && r.Assembler != nil {
		var members []Observation
		for _, tr := range trackers {
			if !tr.isTerminal() {
				tr.mu.Lock()
				obs := tr.obs
				tr.mu.Unlock()
				members = append(members, obs)
			}
		}
		assembleCtx, assembleCancel := context.WithTimeout(ctx, c.Budget.CohortAssembly)
		defer assembleCancel()
		if err := r.Assembler.Assemble(assembleCtx, members); err != nil {
			f := Fault{
				Class:   FaultTerminal,
				Code:    "AssemblyFailed",
				Message: err.Error(),
			}
			for _, tr := range trackers {
				if !tr.isTerminal() {
					tr.setTerminal(PhaseCohortAssembly, f)
				}
			}
			for _, tr := range trackers {
				outcome.Records[tr.intent.ID] = r.buildRecord(tr, c.ID)
			}
			outcome.Ready = false
			return outcome, nil
		}
	}

	// All phases complete.
	for _, tr := range trackers {
		if !tr.isTerminal() {
			tr.setPhase(PhaseReady)
		}
		outcome.Records[tr.intent.ID] = r.buildRecord(tr, c.ID)
	}
	outcome.Ready = true
	return outcome, nil
}

// reconcileEntity drives one entity through phases 1–3 within budget.
// ctx is egCtx (derived from fastFailCtx); phase-scoped sub-contexts add deadlines.
// On return: tr.terminal is set if this entity failed; tr.phase reflects how far
// it got. If ctx is cancelled by fastFailCancel (Canceled, not DeadlineExceeded),
// the entity is left non-terminal so the caller can mark it CohortCancelled.
func (r *Reconciler) reconcileEntity(ctx context.Context, tr *entityTracker, budget PhaseBudget) {
	// Phase 1: launch-acked.
	phase1Ctx, cancel1 := context.WithTimeout(ctx, budget.LaunchAcked)
	defer cancel1()

	if !r.doLaunch(phase1Ctx, tr) {
		// If terminal was set, this is a real failure. Otherwise the entity was
		// cancelled by fastFailCancel — leave it non-terminal.
		return
	}
	tr.setPhase(PhaseRunning)

	// Phase 2: running.
	phase2Ctx, cancel2 := context.WithTimeout(ctx, budget.Running)
	defer cancel2()

	if !r.waitRunning(phase2Ctx, tr) {
		return
	}
	tr.setPhase(PhaseEnrolled)

	// Phase 3: enrolled.
	phase3Ctx, cancel3 := context.WithTimeout(ctx, budget.Enrolled)
	defer cancel3()

	r.waitEnrolled(phase3Ctx, tr)
}

// doLaunch issues Launch (or Start if PreferWarm) for one entity,
// retrying on RetryableConsistency and advancing the chain on CapacityExhausted.
// Returns true if launch was acknowledged.
func (r *Reconciler) doLaunch(ctx context.Context, tr *entityTracker) bool {
	consistencyAttempt := 0
	for {
		if err := ctx.Err(); err != nil {
			if isFastFailCancel(err) {
				return false // leave non-terminal; caller marks CohortCancelled
			}
			r.recordDeadline(tr, PhaseLaunchAcked)
			return false
		}

		var obs Observation
		var err error

		if tr.intent.Rung.WarmStart {
			obs, err = r.Actuator.Start(ctx, tr.intent.ID)
		} else {
			obs, err = r.Actuator.Launch(ctx, tr.intent)
		}

		if err == nil {
			tr.mu.Lock()
			tr.obs = obs
			tr.mu.Unlock()
			tr.addAttempt(tr.intent.Rung, PhaseLaunchAcked, nil)
			return true
		}

		f := r.Classifier.Classify(err)

		switch f.Class {
		case FaultRetryableConsistency:
			consistencyAttempt++
			if consistencyAttempt > maxConsistencyRetries {
				tr.addAttempt(tr.intent.Rung, PhaseLaunchAcked, &f)
				tr.setTerminal(PhaseLaunchAcked, f)
				return false
			}
			sleep(ctx, DefaultBackoffPolicy().Duration(consistencyAttempt))

		case FaultThrottle:
			d := DefaultBackoffPolicy().Duration(0)
			if r.Limiter != nil {
				r.Limiter.Backoff(d)
			}
			sleep(ctx, d)

		case FaultCapacityExhausted:
			tr.addAttempt(tr.intent.Rung, PhaseLaunchAcked, &f)
			if !r.advanceRung(&tr.intent, tr.intent.FallbackChain) {
				tr.setTerminal(PhaseLaunchAcked, f)
				return false
			}
			consistencyAttempt = 0

		case FaultAmbiguous:
			// Must not reach here — Step 3 Client consumed Ambiguous.
			// Treat as terminal with a loud message to surface Step 3 regressions.
			f.Code = "AmbiguousReachedReconciler"
			f.Message = "BUG: FaultAmbiguous escaped substrate.Client — Step 3 regression"
			tr.addAttempt(tr.intent.Rung, PhaseLaunchAcked, &f)
			tr.setTerminal(PhaseLaunchAcked, f)
			return false

		default: // Terminal
			tr.addAttempt(tr.intent.Rung, PhaseLaunchAcked, &f)
			tr.setTerminal(PhaseLaunchAcked, f)
			return false
		}
	}
}

// waitRunning polls Observer until the entity reaches StateRunning or the budget expires.
func (r *Reconciler) waitRunning(ctx context.Context, tr *entityTracker) bool {
	consistencyAttempt := 0
	for {
		if err := ctx.Err(); err != nil {
			if isFastFailCancel(err) {
				return false
			}
			r.recordDeadline(tr, PhaseRunning)
			return false
		}

		obs, err := r.Observer.Observe(ctx, []EntityID{tr.intent.ID})
		if err != nil {
			f := r.Classifier.Classify(err)
			if f.Class == FaultTerminal {
				tr.addAttempt(tr.intent.Rung, PhaseRunning, &f)
				tr.setTerminal(PhaseRunning, f)
				return false
			}
			sleep(ctx, DefaultBackoffPolicy().Duration(consistencyAttempt))
			consistencyAttempt++
			continue
		}
		if len(obs) > 0 {
			tr.mu.Lock()
			tr.obs = obs[0]
			tr.mu.Unlock()

			switch obs[0].State {
			case StateRunning:
				return true
			case StateFailed, StateDraining:
				f := Fault{Class: FaultTerminal, Code: "InstanceFailed",
					Message: fmt.Sprintf("instance entered state %s during phase 2", obs[0].State)}
				tr.addAttempt(tr.intent.Rung, PhaseRunning, &f)
				tr.setTerminal(PhaseRunning, f)
				return false
			}
		}
		sleep(ctx, pollInterval)
	}
}

// waitEnrolled polls Enroller until the entity is enrolled or budget expires.
func (r *Reconciler) waitEnrolled(ctx context.Context, tr *entityTracker) {
	for {
		if err := ctx.Err(); err != nil {
			if isFastFailCancel(err) {
				return
			}
			r.recordDeadline(tr, PhaseEnrolled)
			return
		}

		if r.Enroller != nil {
			readiness, err := r.Enroller.IsEnrolled(ctx, tr.intent.ID)
			if err == nil && readiness.OK() {
				tr.mu.Lock()
				tr.obs.Address = readiness.Detail
				tr.mu.Unlock()
				return // enrolled successfully; caller sets PhaseEnrolled
			}
		} else {
			// No Enroller — the 1-cohort / no-domain case; trivially enrolled.
			return
		}
		sleep(ctx, pollInterval)
	}
}

// advanceRung selects the next rung from the entity's approved fallback chain
// after a FaultCapacityExhausted. Returns false when the chain is exhausted.
// chain is nil when the entity only has one rung in intent.Rung (common case);
// the caller passes the full chain from partitions.yaml when available.
func (r *Reconciler) advanceRung(intent *EntityIntent, chain []Rung) bool {
	if chain == nil || len(chain) == 0 {
		// No approved chain — single-rung intent, nothing to advance.
		return false
	}
	// Find current rung in chain.
	for i, rung := range chain {
		if rung == intent.Rung && i+1 < len(chain) {
			intent.Rung = chain[i+1]
			return true
		}
	}
	return false
}

// Drain marks the given entities for teardown after a cohort failure,
// so no member is left Running-but-useless and billing.
func (r *Reconciler) Drain(ctx context.Context, ids []EntityID) error {
	var lastErr error
	for _, id := range ids {
		if err := r.Actuator.Terminate(ctx, id); err != nil {
			lastErr = err // best-effort: continue draining others
		}
	}
	return lastErr
}

// culpritInfo captures the first entity that made the cohort gate unsatisfiable.
type culpritInfo struct {
	id    EntityID
	fault Fault
	phase Phase
	at    time.Time
}

func (r *Reconciler) recordDeadline(tr *entityTracker, phase Phase) {
	f := Fault{
		Class:   FaultTerminal,
		Code:    "PhaseBudgetExceeded",
		Message: fmt.Sprintf("phase %s budget exceeded", phase),
	}
	tr.addAttempt(tr.intent.Rung, phase, &f)
	tr.setTerminal(phase, f)
}

func (r *Reconciler) buildRecord(tr *entityTracker, cohortID CohortID) Record {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return Record{
		Entity:          tr.intent.ID,
		Generation:      tr.intent.Generation,
		Cohort:          cohortID,
		ReachedPhase:    tr.phase,
		Attempts:        append([]Attempt(nil), tr.attempts...),
		Terminal:        tr.terminal,
		CohortCancelled: tr.cancelled,
		StartedAt:       tr.startedAt,
		FinishedAt:      time.Now(),
	}
}

func (r *Reconciler) clock() time.Time {
	if r.Clock != nil {
		return r.Clock()
	}
	return time.Now()
}

// isFastFailCancel returns true when ctx was cancelled by fastFailCancel()
// rather than by a per-phase DeadlineExceeded. context.Canceled means a
// sibling cancelled us; context.DeadlineExceeded means our own budget ran out.
// The distinction matters for Record: fast-fail survivors are CohortCancelled,
// not Terminal.
func isFastFailCancel(err error) bool {
	return errors.Is(err, context.Canceled)
}

// sleep sleeps for d or until ctx is done.
func sleep(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

const (
	maxConsistencyRetries = 5
	pollInterval          = 100 * time.Millisecond
)
