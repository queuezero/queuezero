package mpi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/queuezero/queuezero/internal/cohort"
	"github.com/queuezero/queuezero/internal/mpi"
)

// This test is the Step 6 co-proof: the UNMODIFIED internal/cohort core driving
// a collective MPI cohort through the mpi-domain Enroller + Assembler, with
// fake provider ports (no AWS, no Slurm). It proves both the happy path
// (complete cohort -> published peer manifest) and the failure path (injected
// mid-launch ICE -> whole set fast-fails in seconds with per-entity Records).

// ---- fake provider ports ----------------------------------------------------

// fakeActuator launches entities, optionally failing specific ones with an
// injected error (used to inject an ICE).
type fakeActuator struct {
	mu        sync.Mutex
	failWith  map[cohort.EntityID]error // entity -> error to return from Launch
	addresses map[cohort.EntityID]string
	terminated []cohort.EntityID
}

func (a *fakeActuator) Launch(_ context.Context, intent cohort.EntityIntent) (cohort.Observation, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err, ok := a.failWith[intent.ID]; ok {
		return cohort.Observation{}, err
	}
	return cohort.Observation{
		ID: intent.ID, Generation: intent.Generation, ProviderID: "i-" + string(intent.ID),
		State: cohort.StateLaunching, Rung: intent.Rung, ObservedAt: time.Now(),
	}, nil
}

func (a *fakeActuator) Start(_ context.Context, id cohort.EntityID) (cohort.Observation, error) {
	return cohort.Observation{ID: id, State: cohort.StateRunning, ObservedAt: time.Now()}, nil
}
func (a *fakeActuator) Stop(_ context.Context, _ cohort.EntityID, _ cohort.StopMode) error { return nil }
func (a *fakeActuator) Terminate(_ context.Context, id cohort.EntityID) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.terminated = append(a.terminated, id)
	return nil
}

// fakeObserver reports every entity Running, with the address the test seeded.
type fakeObserver struct{ addresses map[cohort.EntityID]string }

func (o *fakeObserver) Observe(_ context.Context, ids []cohort.EntityID) ([]cohort.Observation, error) {
	obs := make([]cohort.Observation, len(ids))
	for i, id := range ids {
		obs[i] = cohort.Observation{
			ID: id, State: cohort.StateRunning, Address: o.addresses[id], ObservedAt: time.Now(),
		}
	}
	return obs, nil
}

// fakeClassifier maps an injected ICE error to FaultCapacityExhausted.
type fakeClassifier struct{}

func (fakeClassifier) Classify(err error) cohort.Fault {
	if err == nil {
		return cohort.Fault{Class: cohort.FaultRetryableConsistency}
	}
	if err.Error() == "InsufficientInstanceCapacity" {
		return cohort.Fault{Class: cohort.FaultCapacityExhausted, Code: "InsufficientInstanceCapacity", Message: err.Error()}
	}
	return cohort.Fault{Class: cohort.FaultTerminal, Code: "UnknownError", Message: err.Error()}
}

// fakeProbe is the ReadinessProbe: enrolled + operational once seeded.
type fakeProbe struct{ addresses map[cohort.EntityID]string }

func (p *fakeProbe) ReadReadiness(_ context.Context, id cohort.EntityID) (cohort.Readiness, error) {
	return cohort.Readiness{Enrolled: true, Operational: true, Detail: p.addresses[id]}, nil
}

// fakePublisher captures the published manifest bytes per key.
type fakePublisher struct {
	mu        sync.Mutex
	published map[string][]byte
}

func (p *fakePublisher) Publish(_ context.Context, key string, data []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.published == nil {
		p.published = map[string][]byte{}
	}
	p.published[key] = data
	return nil
}

// ---- helpers -----------------------------------------------------------------

func fastBudget() cohort.PhaseBudget {
	return cohort.PhaseBudget{
		LaunchAcked: time.Second, Running: time.Second, Enrolled: time.Second,
		CohortBarrier: time.Second, CohortAssembly: time.Second,
	}
}

func members(ids ...string) []cohort.EntityIntent {
	out := make([]cohort.EntityIntent, len(ids))
	for i, id := range ids {
		out[i] = cohort.EntityIntent{
			ID: cohort.EntityID(id), Generation: "g1", Cohort: "mpi-job-1",
			IdempotencyToken: "tok-" + id,
			Rung:             cohort.Rung{InstanceType: "hpc7g.16xlarge", AvailZone: "us-east-1a"},
		}
	}
	return out
}

// ---- the co-proof ------------------------------------------------------------

// Happy path: a 4-rank MPI cohort comes up complete; the Assembler publishes a
// peer manifest with one entry per rank, ranks assigned deterministically.
func TestMPIDomain_Collective_PublishesManifest(t *testing.T) {
	addrs := map[cohort.EntityID]string{
		"rank-0": "10.0.0.10", "rank-1": "10.0.0.11",
		"rank-2": "10.0.0.12", "rank-3": "10.0.0.13",
	}
	pub := &fakePublisher{}
	r := &cohort.Reconciler{
		Actuator:   &fakeActuator{addresses: addrs},
		Observer:   &fakeObserver{addresses: addrs},
		Classifier: fakeClassifier{},
		Enroller:   mpi.NewEnroller(&fakeProbe{addresses: addrs}),
		Assembler:  mpi.NewAssembler(pub),
	}

	c, err := cohort.NewMPICohort("mpi-job-1", members("rank-0", "rank-1", "rank-2", "rank-3"), fastBudget())
	if err != nil {
		t.Fatalf("NewMPICohort: %v", err)
	}

	out, err := r.Reconcile(context.Background(), c)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !out.Ready {
		t.Fatalf("cohort not Ready; records: %v", summarize(out))
	}
	for id, rec := range out.Records {
		if !rec.Succeeded() {
			t.Errorf("entity %s did not succeed: %s", id, rec.Summary())
		}
	}

	// Exactly one manifest published, with 4 peers and contiguous ranks.
	if len(pub.published) != 1 {
		t.Fatalf("expected 1 published manifest, got %d", len(pub.published))
	}
	var data []byte
	for _, d := range pub.published {
		data = d
	}
	var m mpi.PeerManifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	if len(m.Peers) != 4 {
		t.Fatalf("manifest has %d peers, want 4", len(m.Peers))
	}
	for i, p := range m.Peers {
		if p.Rank != i {
			t.Errorf("peer[%d] rank=%d want %d", i, p.Rank, i)
		}
		if p.Address == "" {
			t.Errorf("peer %s has empty address in manifest", p.Entity)
		}
	}
}

// Failure path: one rank hits an ICE with no fallback chain. The whole cohort
// must fast-fail as a unit, in well under the barrier budget, with the culprit
// named Terminal/CapacityExhausted and the survivors marked CohortCancelled —
// and NO manifest published.
func TestMPIDomain_Collective_ICE_FastFails(t *testing.T) {
	addrs := map[cohort.EntityID]string{
		"rank-0": "10.0.0.10", "rank-1": "10.0.0.11",
		"rank-2": "10.0.0.12", "rank-3": "10.0.0.13",
	}
	pub := &fakePublisher{}
	act := &fakeActuator{
		addresses: addrs,
		failWith:  map[cohort.EntityID]error{"rank-2": errors.New("InsufficientInstanceCapacity")},
	}
	r := &cohort.Reconciler{
		Actuator:   act,
		Observer:   &fakeObserver{addresses: addrs},
		Classifier: fakeClassifier{},
		Enroller:   mpi.NewEnroller(&fakeProbe{addresses: addrs}),
		Assembler:  mpi.NewAssembler(pub),
	}

	// Generous barrier budget — the point is we fast-fail FAR below it.
	budget := cohort.PhaseBudget{
		LaunchAcked: time.Second, Running: time.Second, Enrolled: time.Second,
		CohortBarrier: 30 * time.Second, CohortAssembly: time.Second,
	}
	c, err := cohort.NewMPICohort("mpi-job-1", members("rank-0", "rank-1", "rank-2", "rank-3"), budget)
	if err != nil {
		t.Fatalf("NewMPICohort: %v", err)
	}

	start := time.Now()
	out, err := r.Reconcile(context.Background(), c)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if out.Ready {
		t.Fatalf("cohort reported Ready despite injected ICE")
	}
	if elapsed > 5*time.Second {
		t.Errorf("fast-fail took %v — expected well under the 30s barrier budget", elapsed)
	}

	// The culprit is Terminal/CapacityExhausted; the other three are CohortCancelled.
	culprit := out.Records["rank-2"]
	if culprit.Terminal == nil || culprit.Terminal.Class != cohort.FaultCapacityExhausted {
		t.Errorf("rank-2 should be Terminal/CapacityExhausted, got %s", culprit.Summary())
	}
	cancelled := 0
	for id, rec := range out.Records {
		if id == "rank-2" {
			continue
		}
		if rec.WasCohortCancelled() {
			cancelled++
			if rec.CohortCancelled.CulpritID != "rank-2" {
				t.Errorf("%s names culprit %s, want rank-2", id, rec.CohortCancelled.CulpritID)
			}
		}
	}
	if cancelled != 3 {
		t.Errorf("expected 3 cohort-cancelled survivors, got %d", cancelled)
	}

	// No manifest may be published on a failed cohort.
	if len(pub.published) != 0 {
		t.Errorf("manifest published despite cohort fast-fail: %v", pub.published)
	}
}

func summarize(out cohort.Outcome) string {
	s := ""
	for id, rec := range out.Records {
		s += fmt.Sprintf("[%s: %s] ", id, rec.Summary())
	}
	return s
}
