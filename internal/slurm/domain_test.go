package slurm

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/queuezero/queuezero/internal/cohort"
	"github.com/queuezero/queuezero/internal/recordstore"
	"github.com/queuezero/queuezero/internal/spec"
)

func fastBudget() *spec.BudgetSpec {
	return &spec.BudgetSpec{
		LaunchAcked: time.Second, Running: time.Second, Enrolled: time.Second,
		CohortBarrier: time.Second, CohortAssembly: time.Second,
	}
}

// buildBridge wires a Bridge from fakes. chain is the partition's fallback chain;
// collective toggles the MPI cohort path; asm is the Assembler (nil unless collective).
func buildBridge(t *testing.T, sc *fakeScontrol, act cohort.Actuator, obs *fakeObserver, asm cohort.Assembler, part spec.Partition) *Bridge {
	t.Helper()
	store, err := recordstore.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("recordstore: %v", err)
	}
	enr := NewEnroller(&fakeProbe{addresses: obs.addresses})
	parts := &spec.Partitions{Partitions: []spec.Partition{part}}
	return &Bridge{
		Reconciler: func(a cohort.Assembler) *cohort.Reconciler {
			return cohort.NewReconciler(act, obs, fakeClassifier{}, enr, a, nil)
		},
		Actuator:  act,
		Assembler: asm,
		Scontrol:  sc,
		Records:   store,
		Cfg: Config{
			Cluster: "test", Region: "us-east-1", Generation: "g1",
			Partitions: parts, DefaultPartition: part.Name,
		},
	}
}

func serialPartition(chain []spec.Rung) spec.Partition {
	return spec.Partition{Name: "serial", ExecutionAccount: "111122223333", FallbackChain: chain, Budget: fastBudget()}
}

func ondemand(it, az string) spec.Rung {
	return spec.Rung{InstanceType: it, AvailZone: az, CapacityModel: "ondemand"}
}

// 1. Serial resume reaches ready: no scontrol writeback, record persisted Succeeded.
func TestResume_Serial_ReachesReady(t *testing.T) {
	addrs := map[cohort.EntityID]string{"cpu-001": "10.0.0.5"}
	sc := newFakeScontrol("cpu-001")
	act := &fakeActuator{addresses: addrs}
	obs := &fakeObserver{addresses: addrs}
	b := buildBridge(t, sc, act, obs, nil, serialPartition([]spec.Rung{ondemand("m7i.xlarge", "us-east-1a")}))

	if err := b.Resume(context.Background(), "serial", "cpu-001"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if sc.updateCount() != 0 {
		t.Errorf("successful node should not be touched via scontrol; got %d updates", sc.updateCount())
	}
	rec, err := b.Records.Get("cpu-001")
	if err != nil {
		t.Fatalf("record not persisted: %v", err)
	}
	if !rec.Succeeded() {
		t.Errorf("record should show success, got %s", rec.Summary())
	}
}

// 2. Collective ICE: culprit -> down, survivors -> drain naming the culprit,
//    fast (<< barrier), no manifest published.
func TestResume_Collective_ICE_MarksCulpritDownSurvivorsDrain(t *testing.T) {
	addrs := map[cohort.EntityID]string{
		"gpu-001": "10.0.0.10", "gpu-002": "10.0.0.11", "gpu-003": "10.0.0.12", "gpu-004": "10.0.0.13",
	}
	sc := newFakeScontrol("gpu-001", "gpu-002", "gpu-003", "gpu-004")
	act := &fakeActuator{addresses: addrs, failWith: map[cohort.EntityID]error{
		"gpu-003": errInjected("InsufficientInstanceCapacity"),
	}}
	obs := &fakeObserver{addresses: addrs}
	pub := &fakePublisher{}
	asm := NewAssembler(pub)

	part := spec.Partition{
		Name: "gpu", ExecutionAccount: "111122223333", Collective: true,
		FallbackChain: []spec.Rung{ondemand("p5.48xlarge", "us-east-1a")},
		// generous barrier: prove we fast-fail far below it
		Budget: &spec.BudgetSpec{LaunchAcked: time.Second, Running: time.Second, Enrolled: time.Second, CohortBarrier: 30 * time.Second, CohortAssembly: time.Second},
	}
	b := buildBridge(t, sc, act, obs, asm, part)

	start := time.Now()
	if err := b.Resume(context.Background(), "gpu", "gpu-[001-004]"); err != nil {
		t.Fatalf("Resume returned error (should signal via node state): %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > 5*time.Second {
		t.Errorf("fast-fail took %v, expected << 30s barrier", elapsed)
	}

	culprit, ok := sc.get("gpu-003")
	if !ok || culprit.state != "down" {
		t.Errorf("gpu-003 should be down, got %+v ok=%v", culprit, ok)
	}
	if !strings.Contains(culprit.reason, "capacity-exhausted") && !strings.Contains(culprit.reason, "InsufficientInstanceCapacity") {
		t.Errorf("gpu-003 reason should name the capacity fault, got %q", culprit.reason)
	}
	for _, n := range []string{"gpu-001", "gpu-002", "gpu-004"} {
		s, ok := sc.get(n)
		if !ok || s.state != "drain" {
			t.Errorf("%s should be drained, got %+v ok=%v", n, s, ok)
		}
		if !strings.Contains(s.reason, "gpu-003") {
			t.Errorf("%s drain reason should name culprit gpu-003, got %q", n, s.reason)
		}
	}
	if pub.count() != 0 {
		t.Errorf("no manifest should be published on fast-fail, got %d", pub.count())
	}
}

// 3. Fallback chain advances then succeeds: first rung ICEs, second launches.
func TestResume_FallbackChainAdvances(t *testing.T) {
	addrs := map[cohort.EntityID]string{"cpu-007": "10.0.0.7"}
	sc := newFakeScontrol("cpu-007")
	// Fail only the first rung (us-east-1a); succeed on the second (us-east-1b).
	// fakeActuator keys failure by entity, so simulate chain advance via a
	// per-call toggle: fail the first Launch, succeed the rest.
	act := &chainActuator{failFirst: true, addresses: addrs}
	obs := &fakeObserver{addresses: addrs}
	chain := []spec.Rung{ondemand("m7i.xlarge", "us-east-1a"), ondemand("m7i.xlarge", "us-east-1b")}
	b := buildBridge(t, sc, act, obs, nil, serialPartition(chain))

	if err := b.Resume(context.Background(), "serial", "cpu-007"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if sc.updateCount() != 0 {
		t.Errorf("node should succeed after chain advance, no scontrol writeback; got %d", sc.updateCount())
	}
	rec, _ := b.Records.Get("cpu-007")
	if !rec.Succeeded() {
		t.Errorf("expected success after fallback, got %s", rec.Summary())
	}
	if len(rec.Attempts) < 2 {
		t.Errorf("expected >=2 attempts (rung advance), got %d", len(rec.Attempts))
	}
}

// 4. SuspendProgram terminates each named entity (no warm pool => terminate).
func TestSuspend_TerminatesNamedEntities(t *testing.T) {
	sc := newFakeScontrol("cpu-001", "cpu-002", "cpu-003")
	act := &fakeActuator{}
	obs := &fakeObserver{addresses: map[cohort.EntityID]string{}}
	b := buildBridge(t, sc, act, obs, nil, serialPartition([]spec.Rung{ondemand("m7i.xlarge", "us-east-1a")}))

	if err := b.Suspend(context.Background(), "serial", "cpu-[001-003]"); err != nil {
		t.Fatalf("Suspend: %v", err)
	}
	if len(act.terminated) != 3 {
		t.Errorf("expected 3 terminated, got %d (%v)", len(act.terminated), act.terminated)
	}
}

// errInjected is a tiny error type so fakeClassifier can match by message.
type errInjected string

func (e errInjected) Error() string { return string(e) }
