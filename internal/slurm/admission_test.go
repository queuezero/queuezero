package slurm

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/queuezero/queuezero/internal/cohort"
	"github.com/queuezero/queuezero/internal/spec"
)

func admissionBridge(t *testing.T, adm Admitter, failMode string) (*Bridge, *fakeScontrol, *fakeActuator) {
	t.Helper()
	addrs := map[cohort.EntityID]string{"cpu-001": "10.0.0.5", "cpu-002": "10.0.0.6"}
	sc := newFakeScontrol("cpu-001", "cpu-002")
	act := &fakeActuator{addresses: addrs}
	obs := &fakeObserver{addresses: addrs}
	b := buildBridge(t, sc, act, obs, nil, serialPartition([]spec.Rung{ondemand("m7i.xlarge", "us-east-1a")}))
	b.Admitter = adm
	b.Cfg.FailMode = failMode
	return b, sc, act
}

// Allowed admission proceeds to a normal reconcile (nodes come up; no writeback).
func TestResume_Admission_Allowed_Proceeds(t *testing.T) {
	adm := &fakeAdmitter{allowed: true}
	b, sc, act := admissionBridge(t, adm, FailGraceful)

	if err := b.Resume(context.Background(), "serial", "cpu-[001-002]"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if adm.calls != 1 {
		t.Errorf("admitter called %d times, want 1", adm.calls)
	}
	if len(act.launched()) == 0 {
		t.Error("allowed admission should launch entities (reconcile ran)")
	}
	if sc.updateCount() != 0 {
		t.Error("allowed + healthy => no scontrol writeback")
	}
}

// Refused admission marks every node down with a BudgetExhausted reason, never
// launches, and persists the refusal so q0 explain can show it.
func TestResume_Admission_Refused_MarksNodesDown(t *testing.T) {
	adm := &fakeAdmitter{allowed: false, reason: "project gauss burn rate exceeded: $42/hr > $30/hr ceiling"}
	b, sc, act := admissionBridge(t, adm, FailGraceful)

	if err := b.Resume(context.Background(), "serial", "cpu-[001-002]"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if len(act.launched()) != 0 {
		t.Errorf("refused admission must not launch anything, got %v", act.launched())
	}
	for _, n := range []string{"cpu-001", "cpu-002"} {
		st, ok := sc.get(n)
		if !ok || st.state != "down" {
			t.Errorf("%s should be down, got %+v ok=%v", n, st, ok)
		}
		if !strings.Contains(st.reason, "BudgetExhausted") {
			t.Errorf("%s reason should name BudgetExhausted, got %q", n, st.reason)
		}
	}
	rec, err := b.Records.Get("cpu-001")
	if err != nil {
		t.Fatalf("refusal record not persisted: %v", err)
	}
	if rec.Terminal == nil || rec.Terminal.Code != "BudgetExhausted" {
		t.Errorf("record should be terminal/BudgetExhausted, got %s", rec.Summary())
	}
}

// A graceful Admitter error allows the launch (a budget-service outage must not
// block the cluster).
func TestResume_Admission_GracefulError_Proceeds(t *testing.T) {
	adm := &fakeAdmitter{err: errors.New("connection refused")}
	b, _, act := admissionBridge(t, adm, FailGraceful)

	if err := b.Resume(context.Background(), "serial", "cpu-[001-002]"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if len(act.launched()) == 0 {
		t.Error("graceful fail-mode should proceed despite admitter error")
	}
}

// A strict Admitter error refuses the launch (fail closed).
func TestResume_Admission_StrictError_Refuses(t *testing.T) {
	adm := &fakeAdmitter{err: errors.New("connection refused")}
	b, sc, act := admissionBridge(t, adm, FailStrict)

	if err := b.Resume(context.Background(), "serial", "cpu-[001-002]"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if len(act.launched()) != 0 {
		t.Error("strict fail-mode must not launch on admitter error")
	}
	if st, ok := sc.get("cpu-001"); !ok || st.state != "down" {
		t.Errorf("strict refusal should mark nodes down, got %+v", st)
	}
}

// No Admitter configured => no gate (the no-regression path).
func TestResume_Admission_NilAdmitter_NoGate(t *testing.T) {
	b, _, act := admissionBridge(t, nil, "")
	if err := b.Resume(context.Background(), "serial", "cpu-[001-002]"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if len(act.launched()) == 0 {
		t.Error("no admitter => normal launch")
	}
}
