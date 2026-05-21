package cohort

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// ---- fake ports (no AWS, no Slurm) ------------------------------------------

// fakeActuator controls per-entity responses.
type fakeActuator struct {
	// launchFn called per Launch; if nil returns a running observation.
	launchFn func(intent EntityIntent) (Observation, error)
	// startFn called per Start.
	startFn func(id EntityID) (Observation, error)
	// terminateCalls records entities Terminate was called for.
	terminateCalls []EntityID
}

func (a *fakeActuator) Launch(_ context.Context, intent EntityIntent) (Observation, error) {
	if a.launchFn != nil {
		return a.launchFn(intent)
	}
	return Observation{ID: intent.ID, Generation: intent.Generation,
		ProviderID: "i-" + string(intent.ID), State: StateLaunching, Rung: intent.Rung,
		ObservedAt: time.Now()}, nil
}

func (a *fakeActuator) Start(_ context.Context, id EntityID) (Observation, error) {
	if a.startFn != nil {
		return a.startFn(id)
	}
	return Observation{ID: id, State: StateRunning, ObservedAt: time.Now()}, nil
}

func (a *fakeActuator) Stop(_ context.Context, _ EntityID, _ StopMode) error { return nil }

func (a *fakeActuator) Terminate(_ context.Context, id EntityID) error {
	a.terminateCalls = append(a.terminateCalls, id)
	return nil
}

// fakeObserver returns StateRunning for every entity immediately.
type fakeObserver struct {
	// stateFn lets tests inject per-entity lifecycle state.
	stateFn func(id EntityID) LifecycleState
}

func (o *fakeObserver) Observe(_ context.Context, ids []EntityID) ([]Observation, error) {
	obs := make([]Observation, len(ids))
	for i, id := range ids {
		st := StateRunning
		if o.stateFn != nil {
			st = o.stateFn(id)
		}
		obs[i] = Observation{ID: id, State: st, ObservedAt: time.Now()}
	}
	return obs, nil
}

// fakeClassifier maps errors by message prefix.
type fakeClassifier struct {
	faults map[string]Fault // keyed by error message
}

func (c *fakeClassifier) Classify(err error) Fault {
	if err == nil {
		return Fault{Class: FaultRetryableConsistency}
	}
	if c.faults != nil {
		if f, ok := c.faults[err.Error()]; ok {
			return f
		}
	}
	return Fault{Class: FaultTerminal, Code: "UnknownError", Message: err.Error()}
}

// fakeEnroller reports enrolled for all entities.
type fakeEnroller struct {
	// enrolledFn lets tests control per-entity enrollment.
	enrolledFn func(id EntityID) Readiness
}

func (e *fakeEnroller) IsEnrolled(_ context.Context, id EntityID) (Readiness, error) {
	if e.enrolledFn != nil {
		return e.enrolledFn(id), nil
	}
	return Readiness{Enrolled: true, MountHealthy: true}, nil
}

// fakeAssembler counts invocations and records members.
type fakeAssembler struct {
	calls   int32
	members []Observation
	err     error
}

func (a *fakeAssembler) Assemble(_ context.Context, members []Observation) error {
	atomic.AddInt32(&a.calls, 1)
	a.members = append(a.members, members...)
	return a.err
}

// ---- helpers ----------------------------------------------------------------

func newReconciler(act *fakeActuator, obs *fakeObserver, enr *fakeEnroller, asm *fakeAssembler) *Reconciler {
	return &Reconciler{
		Actuator:   act,
		Observer:   obs,
		Classifier: &fakeClassifier{},
		Enroller:   enr,
		Assembler:  asm,
	}
}

func fastBudget() PhaseBudget {
	return PhaseBudget{
		LaunchAcked:    2 * time.Second,
		Running:        2 * time.Second,
		Enrolled:       2 * time.Second,
		CohortBarrier:  2 * time.Second,
		CohortAssembly: 2 * time.Second,
	}
}

func member(id string) EntityIntent {
	return EntityIntent{
		ID:               EntityID(id),
		Generation:       "g1",
		Cohort:           "c1",
		IdempotencyToken: "tok-" + id,
		Rung:             Rung{InstanceType: "m5.xlarge", AvailZone: "us-east-1a"},
	}
}

// ---- S5.3 tests -------------------------------------------------------------

// 1-cohort (serial): trivial barrier, no-op assembler, reaches Ready.
func TestReconciler_Serial_ReachesReady(t *testing.T) {
	act := &fakeActuator{}
	obs := &fakeObserver{}
	enr := &fakeEnroller{}
	r := newReconciler(act, obs, enr, nil)

	c := Cohort{
		ID:      "c-serial",
		Members: []EntityIntent{member("gpu-001")},
		Budget:  fastBudget(),
	}
	outcome, err := r.Reconcile(context.Background(), c)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !outcome.Ready {
		t.Errorf("serial cohort: Ready=false")
	}
	rec := outcome.Records["gpu-001"]
	if !rec.Succeeded() {
		t.Errorf("serial entity: Succeeded()=false; summary=%q", rec.Summary())
	}
	if rec.ReachedPhase != PhaseReady {
		t.Errorf("serial entity: ReachedPhase=%v want PhaseReady", rec.ReachedPhase)
	}
}

// Collective cohort, all healthy: barrier satisfied, assembly runs once, Ready.
func TestReconciler_Collective_AllHealthy(t *testing.T) {
	act := &fakeActuator{}
	obs := &fakeObserver{}
	enr := &fakeEnroller{}
	asm := &fakeAssembler{}
	r := newReconciler(act, obs, enr, asm)

	c := Cohort{
		ID:        "c-coll",
		Members:   []EntityIntent{member("n-0"), member("n-1"), member("n-2")},
		Budget:    fastBudget(),
		MinViable: 3,
	}
	outcome, err := r.Reconcile(context.Background(), c)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !outcome.Ready {
		t.Errorf("collective: Ready=false")
	}
	if atomic.LoadInt32(&asm.calls) != 1 {
		t.Errorf("Assemble called %d times want 1", asm.calls)
	}
	if len(asm.members) != 3 {
		t.Errorf("Assemble received %d members want 3", len(asm.members))
	}
}

// Collective with injected ICE that exhausts chain: whole cohort fast-fails as
// a unit promptly — not after waiting out the barrier deadline.
func TestReconciler_Collective_ICE_FastFails(t *testing.T) {
	iceErr := &iceError{}

	rung0 := Rung{InstanceType: "p5.48xlarge", AvailZone: "us-east-1a", CapacityModel: CapacityOnDemand}
	rung1 := Rung{InstanceType: "p5.48xlarge", AvailZone: "us-east-1b", CapacityModel: CapacityOnDemand}
	chain := []Rung{rung0, rung1}

	var launchCount int32
	act := &fakeActuator{
		launchFn: func(intent EntityIntent) (Observation, error) {
			atomic.AddInt32(&launchCount, 1)
			// Always ICE on both rungs.
			return Observation{}, iceErr
		},
	}
	obs := &fakeObserver{}
	enr := &fakeEnroller{}
	clf := &fakeClassifier{faults: map[string]Fault{
		iceErr.Error(): {Class: FaultCapacityExhausted, Code: "InsufficientInstanceCapacity"},
	}}
	r := &Reconciler{
		Actuator:   act,
		Observer:   obs,
		Classifier: clf,
		Enroller:   enr,
	}

	m0 := member("n-0")
	m0.Rung = rung0
	m0.FallbackChain = chain
	m1 := member("n-1")
	m1.Rung = rung0
	m1.FallbackChain = chain

	c := Cohort{
		ID:        "c-ice",
		Members:   []EntityIntent{m0, m1},
		Budget:    PhaseBudget{LaunchAcked: 5 * time.Second, Running: 5 * time.Second, Enrolled: 5 * time.Second, CohortBarrier: 30 * time.Second},
		MinViable: 2,
	}

	start := time.Now()
	outcome, err := r.Reconcile(context.Background(), c)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if outcome.Ready {
		t.Error("ICE-exhausted cohort: Ready=true want false")
	}

	// Fast-fail must be prompt — well under the 30s barrier deadline.
	if elapsed > 5*time.Second {
		t.Errorf("fast-fail took %v — expected < 5s (barrier deadline was 30s)", elapsed)
	}

	// Every entity has a record.
	for _, id := range []EntityID{"n-0", "n-1"} {
		rec, ok := outcome.Records[id]
		if !ok {
			t.Errorf("entity %s: no record", id)
			continue
		}
		if rec.Terminal == nil {
			t.Errorf("entity %s: Terminal=nil want a fault", id)
		}
	}

	// Each entity tried both rungs (two Attempt entries or a fast-fail record).
	rec0 := outcome.Records["n-0"]
	if len(rec0.Attempts) == 0 {
		t.Error("entity n-0: no Attempts recorded")
	}
}

// Per-phase attribution: phase-1 failure and phase-3 failure produce different,
// correctly-named reasons. Each runs in its own 1-cohort to avoid fast-fail
// cross-entity interference.
func TestReconciler_PhaseAttribution(t *testing.T) {
	termErr := &termError{}

	// --- phase-1 failure ---
	act1 := &fakeActuator{
		launchFn: func(intent EntityIntent) (Observation, error) {
			return Observation{}, termErr
		},
	}
	clf := &fakeClassifier{faults: map[string]Fault{
		termErr.Error(): {Class: FaultTerminal, Code: "UnauthorizedOperation"},
	}}
	r1 := &Reconciler{
		Actuator:   act1,
		Observer:   &fakeObserver{},
		Classifier: clf,
		Enroller:   &fakeEnroller{},
	}
	out1, _ := r1.Reconcile(context.Background(), Cohort{
		ID:      "c-p1",
		Members: []EntityIntent{member("phase1-fail")},
		Budget:  fastBudget(),
	})
	rec1 := out1.Records["phase1-fail"]
	if rec1.Terminal == nil {
		t.Fatal("phase1-fail: expected terminal fault")
	}
	if rec1.Terminal.Code != "UnauthorizedOperation" {
		t.Errorf("phase1-fail: Code=%q want UnauthorizedOperation", rec1.Terminal.Code)
	}
	if rec1.ReachedPhase != PhaseLaunchAcked {
		t.Errorf("phase1-fail: ReachedPhase=%v want PhaseLaunchAcked", rec1.ReachedPhase)
	}

	// --- phase-3 failure (enrollment timeout) ---
	r3 := &Reconciler{
		Actuator: &fakeActuator{},
		Observer: &fakeObserver{},
		Classifier: &fakeClassifier{},
		Enroller: &fakeEnroller{
			enrolledFn: func(id EntityID) Readiness {
				return Readiness{Enrolled: false, MountHealthy: false}
			},
		},
	}
	out3, _ := r3.Reconcile(context.Background(), Cohort{
		ID:      "c-p3",
		Members: []EntityIntent{member("phase3-fail")},
		Budget: PhaseBudget{
			LaunchAcked:    2 * time.Second,
			Running:        2 * time.Second,
			Enrolled:       150 * time.Millisecond, // short so enrollment times out
			CohortBarrier:  2 * time.Second,
			CohortAssembly: 2 * time.Second,
		},
	})
	rec3 := out3.Records["phase3-fail"]
	if rec3.Terminal == nil {
		t.Fatal("phase3-fail: expected terminal fault")
	}
	if rec3.ReachedPhase != PhaseEnrolled {
		t.Errorf("phase3-fail: ReachedPhase=%v want PhaseEnrolled", rec3.ReachedPhase)
	}

	// The two failures name different phases.
	if rec1.ReachedPhase == rec3.ReachedPhase {
		t.Error("phase1 and phase3 failures have the same ReachedPhase — attribution broken")
	}
}

// Chain discipline: advanceRung walks only approved rungs; never outside the chain.
func TestReconciler_ChainDiscipline(t *testing.T) {
	rung0 := Rung{InstanceType: "p4d.24xlarge", AvailZone: "us-east-1a"}
	rung1 := Rung{InstanceType: "p4d.24xlarge", AvailZone: "us-east-1b"}
	rung2 := Rung{InstanceType: "p4de.24xlarge", AvailZone: "us-east-1a"}
	chain := []Rung{rung0, rung1, rung2}

	iceErr := &iceError{}
	var rungs []Rung
	act := &fakeActuator{
		launchFn: func(intent EntityIntent) (Observation, error) {
			rungs = append(rungs, intent.Rung)
			if intent.Rung == rung2 {
				// Last rung succeeds.
				return Observation{ID: intent.ID, State: StateLaunching,
					ProviderID: "i-ok", Rung: intent.Rung, ObservedAt: time.Now()}, nil
			}
			return Observation{}, iceErr
		},
	}
	obs := &fakeObserver{}
	enr := &fakeEnroller{}
	clf := &fakeClassifier{faults: map[string]Fault{
		iceErr.Error(): {Class: FaultCapacityExhausted, Code: "InsufficientInstanceCapacity"},
	}}
	r := &Reconciler{Actuator: act, Observer: obs, Classifier: clf, Enroller: enr}

	m := member("gpu-chain")
	m.Rung = rung0
	m.FallbackChain = chain

	c := Cohort{
		ID:      "c-chain",
		Members: []EntityIntent{m},
		Budget:  fastBudget(),
	}
	outcome, err := r.Reconcile(context.Background(), c)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !outcome.Ready {
		t.Errorf("chain: Ready=false, record=%s", outcome.Records["gpu-chain"].Summary())
	}

	// Must have tried exactly rung0, rung1, rung2 — in that order.
	if len(rungs) != 3 {
		t.Fatalf("tried %d rungs want 3: %v", len(rungs), rungs)
	}
	expected := []Rung{rung0, rung1, rung2}
	for i, got := range rungs {
		if got != expected[i] {
			t.Errorf("rung[%d]=%v want %v (chain discipline broken)", i, got, expected[i])
		}
	}
}

// ---- test error types -------------------------------------------------------

type iceError struct{}

func (e *iceError) Error() string { return "InsufficientInstanceCapacity" }

type termError struct{}

func (e *termError) Error() string { return "UnauthorizedOperation" }
