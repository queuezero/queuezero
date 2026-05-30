package slurm

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/queuezero/queuezero/internal/cohort"
	"github.com/queuezero/queuezero/internal/spec"
)

// The full spend-rate loop on the consumer side: resume places a hold (persisted
// per node), suspend reconciles it against actuals and clears it.
func TestResumeThenSuspend_ClosesHoldAgainstActuals(t *testing.T) {
	start := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	fixedClock(t, start)

	addrs := map[cohort.EntityID]string{"cpu-001": "10.0.0.5"}
	sc := newFakeScontrol("cpu-001")
	act := &fakeActuator{addresses: addrs}
	obs := &fakeObserver{addresses: addrs}
	part := serialPartition([]spec.Rung{ondemand("m7i.xlarge", "us-east-1a")})
	b := buildBridge(t, sc, act, obs, nil, part)

	// Wire a reconciling admitter that allows and places a $2/hr fleet hold, plus
	// a hold store.
	adm := &fakeReconcilingAdmitter{}
	adm.allowed = true
	adm.transactionID = "txn_loop"
	adm.estimatedCost = 2.0 // whole-fleet $/hr; 1 node => $2/hr per node
	b.Admitter = adm
	holds, err := NewFileHoldStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b.Holds = holds

	// --- Resume: admit + persist hold ---
	if err := b.Resume(context.Background(), "serial", "cpu-001"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if adm.calls != 1 {
		t.Fatalf("admitter should be consulted once, got %d", adm.calls)
	}
	h, err := holds.Get("cpu-001")
	if err != nil {
		t.Fatalf("resume should persist a hold: %v", err)
	}
	if h.TransactionID != "txn_loop" || h.HourlyRate != 2.0 || !h.StartedAt.Equal(start) {
		t.Errorf("persisted hold wrong: %+v", h)
	}

	// --- Suspend 3h later: reconcile rate*runtime, clear hold ---
	fixedClock(t, start.Add(3*time.Hour))
	// serialPartition has no warm pool => suspend terminates.
	if err := b.Suspend(context.Background(), "serial", "cpu-001"); err != nil {
		t.Fatalf("Suspend: %v", err)
	}
	recs := adm.recorded()
	if len(recs) != 1 {
		t.Fatalf("suspend should reconcile once, got %d", len(recs))
	}
	if recs[0].TransactionID != "txn_loop" {
		t.Errorf("reconcile keyed on wrong txn: %+v", recs[0])
	}
	if recs[0].ActualCost != 6.0 { // 2/hr * 3h
		t.Errorf("actual cost = %v, want 6.0", recs[0].ActualCost)
	}
	if _, err := holds.Get("cpu-001"); !errors.Is(err, ErrNoHold) {
		t.Errorf("hold should be cleared after reconcile, got %v", err)
	}
}

// A refused admission places no hold (the launch is short-circuited).
func TestResume_Refused_PlacesNoHold(t *testing.T) {
	addrs := map[cohort.EntityID]string{"cpu-001": "10.0.0.5"}
	sc := newFakeScontrol("cpu-001")
	act := &fakeActuator{addresses: addrs}
	obs := &fakeObserver{addresses: addrs}
	b := buildBridge(t, sc, act, obs, nil, serialPartition([]spec.Rung{ondemand("m7i.xlarge", "us-east-1a")}))

	adm := &fakeReconcilingAdmitter{}
	adm.allowed = false
	adm.reason = "budget exhausted"
	b.Admitter = adm
	holds, _ := NewFileHoldStore(t.TempDir())
	b.Holds = holds

	if err := b.Resume(context.Background(), "serial", "cpu-001"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	// Refusal => node marked down, nothing launched, no hold.
	if len(act.launched()) != 0 {
		t.Error("a refused admission must not launch")
	}
	if _, err := holds.Get("cpu-001"); !errors.Is(err, ErrNoHold) {
		t.Errorf("refused admission must place no hold, got %v", err)
	}
}

// Graceful-degradation allow (admitter errors, FailGraceful) places no hold:
// there is no transaction to reconcile.
func TestResume_GracefulAllow_PlacesNoHold(t *testing.T) {
	addrs := map[cohort.EntityID]string{"cpu-001": "10.0.0.5"}
	sc := newFakeScontrol("cpu-001")
	act := &fakeActuator{addresses: addrs}
	obs := &fakeObserver{addresses: addrs}
	b := buildBridge(t, sc, act, obs, nil, serialPartition([]spec.Rung{ondemand("m7i.xlarge", "us-east-1a")}))

	adm := &fakeReconcilingAdmitter{}
	adm.err = errors.New("asbb unreachable")
	b.Admitter = adm
	b.Cfg.FailMode = FailGraceful
	holds, _ := NewFileHoldStore(t.TempDir())
	b.Holds = holds

	if err := b.Resume(context.Background(), "serial", "cpu-001"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	// Graceful allow => the launch proceeds, but with no hold to track.
	if len(act.launched()) != 1 {
		t.Errorf("graceful allow should still launch, launched=%v", act.launched())
	}
	if _, err := holds.Get("cpu-001"); !errors.Is(err, ErrNoHold) {
		t.Errorf("graceful allow places no hold, got %v", err)
	}
}
