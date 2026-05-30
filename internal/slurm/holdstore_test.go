package slurm

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/queuezero/queuezero/internal/cohort"
)

func TestFileHoldStore_RoundTrip(t *testing.T) {
	s, err := NewFileHoldStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	want := Hold{
		Entity:        "gpu-001",
		TransactionID: "txn_1",
		Account:       "111122223333",
		Partition:     "gpu",
		HourlyRate:    3.5,
		StartedAt:     time.Unix(1700000000, 0).UTC(),
	}
	if err := s.Put(want); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := s.Get("gpu-001")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != want {
		t.Errorf("round-trip mismatch:\n got %+v\nwant %+v", got, want)
	}

	if err := s.Delete("gpu-001"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get("gpu-001"); !errors.Is(err, ErrNoHold) {
		t.Errorf("after delete want ErrNoHold, got %v", err)
	}
}

func TestFileHoldStore_GetMissingIsErrNoHold(t *testing.T) {
	s, _ := NewFileHoldStore(t.TempDir())
	if _, err := s.Get("nope"); !errors.Is(err, ErrNoHold) {
		t.Errorf("want ErrNoHold, got %v", err)
	}
}

func TestFileHoldStore_DeleteAbsentIsNoError(t *testing.T) {
	s, _ := NewFileHoldStore(t.TempDir())
	if err := s.Delete("nope"); err != nil {
		t.Errorf("deleting an absent hold must be a no-op, got %v", err)
	}
}

func TestFileHoldStore_EncodesPathSeparators(t *testing.T) {
	s, _ := NewFileHoldStore(t.TempDir())
	// An exotic id with a separator must not escape Dir.
	h := Hold{Entity: cohort.EntityID("a/b"), TransactionID: "txn"}
	if err := s.Put(h); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := s.Get("a/b")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Entity != "a/b" {
		t.Errorf("entity round-trip wrong: %q", got.Entity)
	}
}

// fixedClock installs a deterministic nowFunc for the duration of a test.
func fixedClock(t *testing.T, at time.Time) {
	t.Helper()
	prev := nowFunc
	nowFunc = func() time.Time { return at }
	t.Cleanup(func() { nowFunc = prev })
}

func TestReconcileHold_ChargesRateTimesRuntime(t *testing.T) {
	dir := t.TempDir()
	holds, _ := NewFileHoldStore(dir)

	start := time.Unix(1700000000, 0).UTC()
	// Seed a hold as if resume placed it 2h ago at $1.50/hr.
	if err := holds.Put(Hold{
		Entity: "gpu-001", TransactionID: "txn_7", Account: "acct",
		Partition: "gpu", HourlyRate: 1.5, StartedAt: start,
	}); err != nil {
		t.Fatal(err)
	}
	fixedClock(t, start.Add(2*time.Hour))

	adm := &fakeReconcilingAdmitter{}
	b := &Bridge{Admitter: adm, Holds: holds, Cfg: Config{Cluster: "gauss"}}

	b.reconcileHold(context.Background(), "gpu-001")

	recs := adm.recorded()
	if len(recs) != 1 {
		t.Fatalf("want 1 reconcile, got %d", len(recs))
	}
	r := recs[0]
	if r.TransactionID != "txn_7" || r.Account != "acct" {
		t.Errorf("reconcile carried wrong hold identity: %+v", r)
	}
	if r.ActualCost != 3.0 { // 1.5/hr * 2h
		t.Errorf("actual cost = %v, want 3.0 (rate*runtime)", r.ActualCost)
	}
	if r.JobID != "gauss/gpu-001" {
		t.Errorf("job id = %q, want gauss/gpu-001", r.JobID)
	}
	// Hold removed after a successful reconcile.
	if _, err := holds.Get("gpu-001"); !errors.Is(err, ErrNoHold) {
		t.Errorf("hold should be deleted after reconcile, got %v", err)
	}
}

func TestReconcileHold_NoHoldIsNoop(t *testing.T) {
	holds, _ := NewFileHoldStore(t.TempDir())
	adm := &fakeReconcilingAdmitter{}
	b := &Bridge{Admitter: adm, Holds: holds, Cfg: Config{Cluster: "gauss"}}

	b.reconcileHold(context.Background(), "never-held")

	if len(adm.recorded()) != 0 {
		t.Error("a node with no hold must not trigger a reconcile")
	}
}

func TestReconcileHold_ServiceErrorKeepsHold(t *testing.T) {
	holds, _ := NewFileHoldStore(t.TempDir())
	_ = holds.Put(Hold{Entity: "gpu-001", TransactionID: "txn_7", HourlyRate: 1, StartedAt: nowFunc()})
	adm := &fakeReconcilingAdmitter{reconErr: errors.New("asbb down")}
	b := &Bridge{Admitter: adm, Holds: holds, Cfg: Config{Cluster: "gauss"}}

	b.reconcileHold(context.Background(), "gpu-001")

	// The hold must survive a failed reconcile so a later retry/sweep can try again.
	if _, err := holds.Get("gpu-001"); err != nil {
		t.Errorf("hold should survive a failed reconcile, got %v", err)
	}
}
