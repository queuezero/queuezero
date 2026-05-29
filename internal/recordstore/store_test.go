package recordstore

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/queuezero/queuezero/internal/cohort"
)

func sampleRecord(entity cohort.EntityID) cohort.Record {
	f := cohort.Fault{Class: cohort.FaultCapacityExhausted, Code: "InsufficientInstanceCapacity", Message: "ICE"}
	return cohort.Record{
		Entity: entity, Generation: "g1", Cohort: "c1", ReachedPhase: cohort.PhaseLaunchAcked,
		Attempts: []cohort.Attempt{{
			Rung:  cohort.Rung{InstanceType: "hpc7g.16xlarge", AvailZone: "us-east-1a"},
			Phase: cohort.PhaseLaunchAcked, Fault: &f, At: time.Now(),
		}},
		Terminal: &f, StartedAt: time.Now(), FinishedAt: time.Now(),
	}
}

func TestFileStore_PutGet_RoundTrip(t *testing.T) {
	s, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	rec := sampleRecord("rank-2")
	if err := s.Put(rec); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := s.Get("rank-2")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Terminal == nil || got.Terminal.Class != cohort.FaultCapacityExhausted {
		t.Errorf("fault class not preserved: %+v", got.Terminal)
	}
	if got.Terminal.Code != "InsufficientInstanceCapacity" {
		t.Errorf("verbatim code not preserved: %q", got.Terminal.Code)
	}
	if !strings.Contains(got.Explain(), "InsufficientInstanceCapacity") {
		t.Errorf("Explain() lost the code:\n%s", got.Explain())
	}
}

func TestFileStore_Get_NotFound(t *testing.T) {
	s, _ := NewFileStore(t.TempDir())
	_, err := s.Get("nope")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestFileStore_Put_Overwrites(t *testing.T) {
	s, _ := NewFileStore(t.TempDir())
	if err := s.Put(sampleRecord("rank-2")); err != nil {
		t.Fatal(err)
	}
	// Second write: a success record for the same entity (re-reconciled).
	rec := cohort.Record{Entity: "rank-2", Cohort: "c1", ReachedPhase: cohort.PhaseReady}
	if err := s.Put(rec); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("rank-2")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Succeeded() {
		t.Errorf("latest-write-wins failed: record still shows %s", got.Summary())
	}
}

func TestFileStore_List_Sorted(t *testing.T) {
	s, _ := NewFileStore(t.TempDir())
	for _, id := range []cohort.EntityID{"rank-2", "rank-0", "rank-1"} {
		if err := s.Put(sampleRecord(id)); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	want := []cohort.EntityID{"rank-0", "rank-1", "rank-2"}
	if len(ids) != len(want) {
		t.Fatalf("List len=%d want %d (%v)", len(ids), len(want), ids)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Errorf("List[%d]=%s want %s", i, ids[i], want[i])
		}
	}
}

func TestFileStore_EntityWithSlash(t *testing.T) {
	s, _ := NewFileStore(t.TempDir())
	id := cohort.EntityID("partition/gpu-042")
	if err := s.Put(sampleRecord(id)); err != nil {
		t.Fatalf("Put with slash id: %v", err)
	}
	got, err := s.Get(id)
	if err != nil {
		t.Fatalf("Get with slash id: %v", err)
	}
	if got.Entity != id {
		t.Errorf("entity id round-trip failed: got %q want %q", got.Entity, id)
	}
}

func TestPutOutcome_PersistsAll(t *testing.T) {
	s, _ := NewFileStore(t.TempDir())
	out := cohort.Outcome{
		Cohort: "c1", Ready: false,
		Records: map[cohort.EntityID]cohort.Record{
			"rank-0": sampleRecord("rank-0"),
			"rank-1": sampleRecord("rank-1"),
		},
	}
	if err := s.PutOutcome(out); err != nil {
		t.Fatalf("PutOutcome: %v", err)
	}
	ids, _ := s.List()
	if len(ids) != 2 {
		t.Fatalf("expected 2 persisted records, got %d", len(ids))
	}
}
