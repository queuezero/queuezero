package slurm

import (
	"context"
	"testing"
	"time"

	"github.com/queuezero/queuezero/internal/cohort"
	"github.com/queuezero/queuezero/internal/substrate"
)

// fakeClusterDescriber returns a fixed instance set.
type fakeClusterDescriber struct {
	instances []substrate.Instance
	err       error
}

func (f *fakeClusterDescriber) DescribeCluster(_ context.Context, _ string) ([]substrate.Instance, error) {
	return f.instances, f.err
}

// fixedNow returns a clock function pinned to t.
func fixedNow(t time.Time) func() time.Time { return func() time.Time { return t } }

func sweepBridge(desc ClusterDescriber, act cohort.Actuator, generation string) *Bridge {
	return &Bridge{
		Actuator:  act,
		Describer: desc,
		Cfg:       Config{Cluster: "test", Generation: cohort.Generation(generation)},
	}
}

func TestSweep_ReapsStalePastGrace_SparesEverythingElse(t *testing.T) {
	now := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	old := now.Add(-1 * time.Hour) // well past a 10m grace
	fresh := now.Add(-2 * time.Minute)

	desc := &fakeClusterDescriber{instances: []substrate.Instance{
		// stale gen, old, has entity -> REAP
		{ProviderID: "i-stale", State: "running", Generation: "g3", Entity: "gpu-001", LaunchTime: old},
		// current gen -> spare (live node), even though old
		{ProviderID: "i-live", State: "running", Generation: "g5", Entity: "gpu-002", LaunchTime: old},
		// stale gen but within grace -> spare
		{ProviderID: "i-young", State: "running", Generation: "g3", Entity: "gpu-003", LaunchTime: fresh},
		// stale gen, old, but no entity tag -> spare (can't terminate by name)
		{ProviderID: "i-noent", State: "running", Generation: "g3", Entity: "", LaunchTime: old},
		// no generation tag -> spare (not q0-managed / mid-launch)
		{ProviderID: "i-notag", State: "running", Generation: "", Entity: "x", LaunchTime: old},
		// already terminating -> spare
		{ProviderID: "i-gone", State: "shutting-down", Generation: "g3", Entity: "gpu-004", LaunchTime: old},
	}}
	act := &fakeActuator{}
	b := sweepBridge(desc, act, "g5")

	res, err := b.Sweep(context.Background(), SweepOptions{Grace: 10 * time.Minute, Now: fixedNow(now)})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(res.Reaped) != 1 || res.Reaped[0].Entity != "gpu-001" {
		t.Fatalf("expected exactly gpu-001 reaped, got %+v", res.Reaped)
	}
	if len(act.terminated) != 1 || act.terminated[0] != "gpu-001" {
		t.Errorf("expected Terminate(gpu-001), got %v", act.terminated)
	}
	if len(res.Spared) != 5 {
		t.Errorf("expected 5 spared, got %d (%+v)", len(res.Spared), res.Spared)
	}
}

func TestSweep_DryRun_RecordsButDoesNotTerminate(t *testing.T) {
	now := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	desc := &fakeClusterDescriber{instances: []substrate.Instance{
		{ProviderID: "i-stale", State: "running", Generation: "g3", Entity: "gpu-001", LaunchTime: now.Add(-time.Hour)},
	}}
	act := &fakeActuator{}
	b := sweepBridge(desc, act, "g5")

	res, err := b.Sweep(context.Background(), SweepOptions{Grace: 10 * time.Minute, DryRun: true, Now: fixedNow(now)})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(res.Reaped) != 1 {
		t.Errorf("dry-run should record 1 reap decision, got %d", len(res.Reaped))
	}
	if len(act.terminated) != 0 {
		t.Errorf("dry-run must not terminate, got %v", act.terminated)
	}
}

func TestSweep_EmptyGeneration_Refuses(t *testing.T) {
	desc := &fakeClusterDescriber{}
	b := sweepBridge(desc, &fakeActuator{}, "")
	if _, err := b.Sweep(context.Background(), SweepOptions{Grace: time.Minute}); err == nil {
		t.Fatal("empty current generation should refuse to sweep")
	}
}

func TestSweep_DescribeError_DoesNotReap(t *testing.T) {
	desc := &fakeClusterDescriber{err: context.DeadlineExceeded}
	act := &fakeActuator{}
	b := sweepBridge(desc, act, "g5")
	if _, err := b.Sweep(context.Background(), SweepOptions{Grace: time.Minute}); err == nil {
		t.Fatal("describe error should surface, not be swallowed")
	}
	if len(act.terminated) != 0 {
		t.Error("must not terminate when describe failed")
	}
}

func TestSweep_NilDescriber_Errors(t *testing.T) {
	b := &Bridge{Actuator: &fakeActuator{}, Cfg: Config{Generation: "g5"}}
	if _, err := b.Sweep(context.Background(), SweepOptions{}); err == nil {
		t.Fatal("nil Describer should error")
	}
}

// staleInstance is a stale-generation, past-grace, entity-tagged orphan — the
// shape Sweep reaps. LaunchTime is the EC2 launch; the budget hold's StartedAt
// (resume time) is what reconcile charges against, so the two are seeded
// independently below.
func staleInstance(entity string, launch time.Time) substrate.Instance {
	return substrate.Instance{
		ProviderID: "i-" + entity, State: "running",
		Generation: "g3", Entity: entity, LaunchTime: launch,
	}
}

// reapBridge wires a sweep Bridge with a hold store + reconciling admitter, so a
// reaped orphan can close its budget hold the way the suspend path does.
func reapBridge(t *testing.T, desc ClusterDescriber, act cohort.Actuator, holds HoldStore, adm Admitter) *Bridge {
	t.Helper()
	return &Bridge{
		Actuator: act, Describer: desc, Holds: holds, Admitter: adm,
		Cfg: Config{Cluster: "gauss", Generation: "g5"},
	}
}

func TestSweep_ReapedOrphan_ReconcilesHold(t *testing.T) {
	now := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	resumedAt := now.Add(-2 * time.Hour) // hold placed 2h ago
	fixedClock(t, now)                   // reconcileHold's runtime clock == sweep's clock

	holds, _ := NewFileHoldStore(t.TempDir())
	if err := holds.Put(Hold{
		Entity: "gpu-001", TransactionID: "txn_9", Account: "acct", Partition: "gpu",
		HourlyRate: 2.0, StartedAt: resumedAt,
	}); err != nil {
		t.Fatal(err)
	}
	desc := &fakeClusterDescriber{instances: []substrate.Instance{staleInstance("gpu-001", now.Add(-3*time.Hour))}}
	act := &fakeActuator{}
	adm := &fakeReconcilingAdmitter{}
	b := reapBridge(t, desc, act, holds, adm)

	res, err := b.Sweep(context.Background(), SweepOptions{Grace: 10 * time.Minute, Now: fixedNow(now)})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(res.Reaped) != 1 || len(act.terminated) != 1 {
		t.Fatalf("expected gpu-001 reaped+terminated, got reaped=%+v terminated=%v", res.Reaped, act.terminated)
	}
	recs := adm.recorded()
	if len(recs) != 1 {
		t.Fatalf("expected 1 reconcile for the reaped orphan, got %d", len(recs))
	}
	if recs[0].ActualCost != 4.0 { // 2.0/hr * 2h (now - StartedAt)
		t.Errorf("actual cost = %v, want 4.0 (rate*runtime since resume)", recs[0].ActualCost)
	}
	if recs[0].TransactionID != "txn_9" {
		t.Errorf("reconcile carried wrong txn: %q", recs[0].TransactionID)
	}
	if _, err := holds.Get("gpu-001"); err == nil {
		t.Error("hold should be deleted after a successful reconcile")
	}
}

func TestSweep_TerminateFails_DoesNotReconcile(t *testing.T) {
	now := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	fixedClock(t, now)

	holds, _ := NewFileHoldStore(t.TempDir())
	_ = holds.Put(Hold{Entity: "gpu-001", TransactionID: "txn_9", HourlyRate: 1, StartedAt: now.Add(-time.Hour)})
	desc := &fakeClusterDescriber{instances: []substrate.Instance{staleInstance("gpu-001", now.Add(-3*time.Hour))}}
	act := &fakeActuator{termFail: map[cohort.EntityID]error{"gpu-001": context.DeadlineExceeded}}
	adm := &fakeReconcilingAdmitter{}
	b := reapBridge(t, desc, act, holds, adm)

	if _, err := b.Sweep(context.Background(), SweepOptions{Grace: 10 * time.Minute, Now: fixedNow(now)}); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(act.terminated) != 0 {
		t.Fatalf("terminate was supposed to fail; got %v", act.terminated)
	}
	if len(adm.recorded()) != 0 {
		t.Error("a terminate failure (instance still running) must NOT reconcile the hold")
	}
	if _, err := holds.Get("gpu-001"); err != nil {
		t.Error("hold must be left in place when terminate failed")
	}
}

func TestSweep_DryRun_DoesNotReconcile(t *testing.T) {
	now := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	fixedClock(t, now)

	holds, _ := NewFileHoldStore(t.TempDir())
	_ = holds.Put(Hold{Entity: "gpu-001", TransactionID: "txn_9", HourlyRate: 1, StartedAt: now.Add(-time.Hour)})
	desc := &fakeClusterDescriber{instances: []substrate.Instance{staleInstance("gpu-001", now.Add(-3*time.Hour))}}
	act := &fakeActuator{}
	adm := &fakeReconcilingAdmitter{}
	b := reapBridge(t, desc, act, holds, adm)

	if _, err := b.Sweep(context.Background(), SweepOptions{Grace: 10 * time.Minute, DryRun: true, Now: fixedNow(now)}); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(act.terminated) != 0 || len(adm.recorded()) != 0 {
		t.Errorf("dry-run must neither terminate nor reconcile (terminated=%v reconciles=%d)", act.terminated, len(adm.recorded()))
	}
	if _, err := holds.Get("gpu-001"); err != nil {
		t.Error("dry-run must leave the hold untouched")
	}
}

func TestSweep_ReapedOrphan_NoHold_NoReconcile(t *testing.T) {
	now := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	fixedClock(t, now)

	holds, _ := NewFileHoldStore(t.TempDir()) // empty: orphan was launched before admission, or hold already closed
	desc := &fakeClusterDescriber{instances: []substrate.Instance{staleInstance("gpu-001", now.Add(-3*time.Hour))}}
	act := &fakeActuator{}
	adm := &fakeReconcilingAdmitter{}
	b := reapBridge(t, desc, act, holds, adm)

	res, err := b.Sweep(context.Background(), SweepOptions{Grace: 10 * time.Minute, Now: fixedNow(now)})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(res.Reaped) != 1 || len(act.terminated) != 1 {
		t.Fatalf("orphan with no hold should still be reaped, got reaped=%+v terminated=%v", res.Reaped, act.terminated)
	}
	if len(adm.recorded()) != 0 {
		t.Error("an orphan with no persisted hold must not trigger a reconcile")
	}
}
