package spored

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/queuezero/queuezero/internal/tags"
)

// ---- fakes ------------------------------------------------------------------

type fakeIdentity struct {
	id  Identity
	err error
}

func (f fakeIdentity) Identify(_ context.Context) (Identity, error) { return f.id, f.err }

type fakeTagWriter struct {
	lastID   string
	lastTags map[string]string
	writes   int
	err      error
}

func (f *fakeTagWriter) Tag(_ context.Context, providerID string, kv map[string]string) error {
	if f.err != nil {
		return f.err
	}
	f.writes++
	f.lastID = providerID
	f.lastTags = kv
	return nil
}

type fakeProbe struct {
	name string
	err  error
}

func (p fakeProbe) Name() string                       { return p.name }
func (p fakeProbe) Check(_ context.Context) error       { return p.err }

func ident() fakeIdentity {
	return fakeIdentity{id: Identity{ProviderID: "i-node", Entity: "gpu-001", Cluster: "test"}}
}

// observerVerdict replicates EXACTLY what aws.Observer.ReadReadiness derives from
// the readiness tags, so the round-trip test proves writer/reader agreement
// without standing up the AWS client. If ReadReadiness's parsing ever changes,
// this must change with it — both reference the same internal/tags constants.
func observerVerdict(kv map[string]string) (ok bool) {
	ready, _ := strconv.ParseBool(kv[tags.Ready])
	mountHealthy := strings.EqualFold(kv[tags.Phase], tags.PhaseEnrolled)
	enrolled := ready && mountHealthy
	operational := mountHealthy
	return enrolled && operational // cohort.Readiness.OK()
}

// ---- tests ------------------------------------------------------------------

func TestReportOnce_AllProbesPass_WritesEnrolledReady(t *testing.T) {
	w := &fakeTagWriter{}
	r := NewReporter(ident(), w, fakeProbe{name: "mount", err: nil}, fakeProbe{name: "slurmd", err: nil})

	ready, err := r.ReportOnce(context.Background())
	if err != nil {
		t.Fatalf("ReportOnce: %v", err)
	}
	if !ready {
		t.Error("expected ready=true when all probes pass")
	}
	if w.lastID != "i-node" {
		t.Errorf("wrote to %q, want i-node", w.lastID)
	}
	if w.lastTags[tags.Phase] != tags.PhaseEnrolled || w.lastTags[tags.Ready] != "true" {
		t.Errorf("tags wrong: %v", w.lastTags)
	}
	// Round-trip: the Observer must read this as OK.
	if !observerVerdict(w.lastTags) {
		t.Error("Observer would NOT see this as ready — writer/reader disagree")
	}
}

func TestReportOnce_ProbeFails_WritesBootingNotReady(t *testing.T) {
	w := &fakeTagWriter{}
	r := NewReporter(ident(), w,
		fakeProbe{name: "mount", err: nil},
		fakeProbe{name: "slurmd", err: errors.New("inactive")},
	)

	ready, err := r.ReportOnce(context.Background())
	if err != nil {
		t.Fatalf("ReportOnce: %v", err)
	}
	if ready {
		t.Error("expected ready=false when a probe fails")
	}
	if w.lastTags[tags.Phase] != tags.PhaseBooting || w.lastTags[tags.Ready] != "false" {
		t.Errorf("tags wrong: %v", w.lastTags)
	}
	if !strings.Contains(w.lastTags[tags.Detail], "slurmd") {
		t.Errorf("detail should name the failing probe, got %q", w.lastTags[tags.Detail])
	}
	// Round-trip: the Observer must NOT see this as ready.
	if observerVerdict(w.lastTags) {
		t.Error("Observer would wrongly see a failed node as ready")
	}
}

func TestReportOnce_NoProbes_TriviallyReady(t *testing.T) {
	w := &fakeTagWriter{}
	r := NewReporter(ident(), w)
	ready, err := r.ReportOnce(context.Background())
	if err != nil || !ready {
		t.Fatalf("no-probe node should be trivially ready, got ready=%v err=%v", ready, err)
	}
	if !observerVerdict(w.lastTags) {
		t.Error("no-probe node should read as ready")
	}
}

func TestReportOnce_IdentityError_NoWrite(t *testing.T) {
	w := &fakeTagWriter{}
	r := NewReporter(fakeIdentity{err: errors.New("imds down")}, w, fakeProbe{name: "mount"})
	if _, err := r.ReportOnce(context.Background()); err == nil {
		t.Fatal("expected error when identity fails")
	}
	if w.writes != 0 {
		t.Error("must not write tags when identity is unknown")
	}
}

func TestRun_StopsOnContextCancel(t *testing.T) {
	w := &fakeTagWriter{}
	r := NewReporter(ident(), w, fakeProbe{name: "mount"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	err := r.Run(ctx, time.Millisecond)
	if err == nil {
		t.Error("Run should return ctx.Err() on cancel")
	}
	// It reports once immediately before observing cancellation.
	if w.writes == 0 {
		t.Error("Run should report at least once before returning")
	}
}
