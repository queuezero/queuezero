package slurm

import (
	"context"
	"sync"
	"time"

	"github.com/queuezero/queuezero/internal/cohort"
)

// Fakes shared across slurm tests. Package `slurm` (not slurm_test) so resume.go
// internals and these fakes share scope. cohort's own fakes are unexported, so
// the slurm domain needs its own — mirroring internal/mpi/domain_test.go.

// fakeAdmitter is a programmable spend-rate gate.
type fakeAdmitter struct {
	allowed       bool
	reason        string
	err           error
	transactionID string  // returned on the allow path
	estimatedCost float64 // whole-fleet $/hr
	calls         int
}

func (a *fakeAdmitter) Admit(_ context.Context, _ AdmissionRequest) (AdmissionResult, error) {
	a.calls++
	if a.err != nil {
		return AdmissionResult{}, a.err
	}
	return AdmissionResult{
		Allowed:       a.allowed,
		Reason:        a.reason,
		TransactionID: a.transactionID,
		EstimatedCost: a.estimatedCost,
	}, nil
}

// fakeReconcilingAdmitter is a fakeAdmitter that also satisfies Reconciler,
// recording the reconcile requests it received.
type fakeReconcilingAdmitter struct {
	fakeAdmitter
	mu         sync.Mutex
	reconciles []ReconcileRequest
	reconErr   error
}

func (a *fakeReconcilingAdmitter) Reconcile(_ context.Context, req ReconcileRequest) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.reconErr != nil {
		return a.reconErr
	}
	a.reconciles = append(a.reconciles, req)
	return nil
}

func (a *fakeReconcilingAdmitter) recorded() []ReconcileRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]ReconcileRequest(nil), a.reconciles...)
}

// fakeActuator launches entities, optionally failing specific ones, and records
// terminate/stop calls for the suspend proof.
type fakeActuator struct {
	mu          sync.Mutex
	failWith    map[cohort.EntityID]error // entity -> error from Launch
	addresses   map[cohort.EntityID]string
	launchedIDs []cohort.EntityID
	terminated  []cohort.EntityID
	stopped     []cohort.EntityID
}

func (a *fakeActuator) Launch(_ context.Context, intent cohort.EntityIntent) (cohort.Observation, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err, ok := a.failWith[intent.ID]; ok {
		return cohort.Observation{}, err
	}
	a.launchedIDs = append(a.launchedIDs, intent.ID)
	return cohort.Observation{
		ID: intent.ID, Generation: intent.Generation, ProviderID: "i-" + string(intent.ID),
		State: cohort.StateLaunching, Rung: intent.Rung, ObservedAt: time.Now(),
	}, nil
}

// launched returns the entities Launch was called for (admission-gate proof).
func (a *fakeActuator) launched() []cohort.EntityID {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]cohort.EntityID(nil), a.launchedIDs...)
}

func (a *fakeActuator) Start(_ context.Context, id cohort.EntityID) (cohort.Observation, error) {
	return cohort.Observation{ID: id, State: cohort.StateRunning, ObservedAt: time.Now()}, nil
}

func (a *fakeActuator) Stop(_ context.Context, id cohort.EntityID, _ cohort.StopMode) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stopped = append(a.stopped, id)
	return nil
}

func (a *fakeActuator) Terminate(_ context.Context, id cohort.EntityID) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.terminated = append(a.terminated, id)
	return nil
}

// chainActuator fails the FIRST Launch call (simulating the first rung ICEing)
// and succeeds on every subsequent call (the next rung). It returns an ICE error
// so fakeClassifier classifies it CapacityExhausted, driving advanceRung.
type chainActuator struct {
	mu        sync.Mutex
	failFirst bool
	launches  int
	addresses map[cohort.EntityID]string
}

func (a *chainActuator) Launch(_ context.Context, intent cohort.EntityIntent) (cohort.Observation, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.launches++
	if a.failFirst && a.launches == 1 {
		return cohort.Observation{}, errInjected("InsufficientInstanceCapacity")
	}
	return cohort.Observation{
		ID: intent.ID, Generation: intent.Generation, ProviderID: "i-" + string(intent.ID),
		State: cohort.StateLaunching, Rung: intent.Rung, ObservedAt: time.Now(),
	}, nil
}

func (a *chainActuator) Start(_ context.Context, id cohort.EntityID) (cohort.Observation, error) {
	return cohort.Observation{ID: id, State: cohort.StateRunning, ObservedAt: time.Now()}, nil
}
func (a *chainActuator) Stop(_ context.Context, _ cohort.EntityID, _ cohort.StopMode) error { return nil }
func (a *chainActuator) Terminate(_ context.Context, _ cohort.EntityID) error               { return nil }

// fakeObserver reports every entity Running with its seeded address.
type fakeObserver struct{ addresses map[cohort.EntityID]string }

func (o *fakeObserver) Observe(_ context.Context, ids []cohort.EntityID) ([]cohort.Observation, error) {
	obs := make([]cohort.Observation, len(ids))
	for i, id := range ids {
		obs[i] = cohort.Observation{ID: id, State: cohort.StateRunning, Address: o.addresses[id], ObservedAt: time.Now()}
	}
	return obs, nil
}

// fakeClassifier maps the injected ICE message to FaultCapacityExhausted.
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

// fakeProbe satisfies mpi.ReadinessProbe: enrolled + operational once seeded.
type fakeProbe struct{ addresses map[cohort.EntityID]string }

func (p *fakeProbe) ReadReadiness(_ context.Context, id cohort.EntityID) (cohort.Readiness, error) {
	return cohort.Readiness{Enrolled: true, Operational: true, Detail: p.addresses[id]}, nil
}

// fakePublisher captures published manifest keys for the collective proof.
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

func (p *fakePublisher) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.published)
}

// fakeScontrol records UpdateNode calls and serves a seeded hostname expansion.
type fakeScontrol struct {
	mu       sync.Mutex
	hosts    []string                 // returned by ShowHostnames
	updates  map[string]scontrolState // node -> last state written
}

type scontrolState struct{ state, reason string }

func newFakeScontrol(hosts ...string) *fakeScontrol {
	return &fakeScontrol{hosts: hosts, updates: map[string]scontrolState{}}
}

func (s *fakeScontrol) ShowHostnames(_ context.Context, _ string) ([]string, error) {
	return s.hosts, nil
}

func (s *fakeScontrol) UpdateNode(_ context.Context, node, state, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updates[node] = scontrolState{state: state, reason: reason}
	return nil
}

func (s *fakeScontrol) get(node string) (scontrolState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.updates[node]
	return v, ok
}

func (s *fakeScontrol) updateCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.updates)
}
