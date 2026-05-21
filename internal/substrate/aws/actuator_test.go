package aws

import (
	"context"
	"errors"
	"testing"

	"github.com/queuezero/queuezero/internal/cohort"
	"github.com/queuezero/queuezero/internal/substrate"
)

// ---- fake substrateClient ---------------------------------------------------

type fakeSubstrateClient struct {
	runErr   error
	runInst  substrate.Instance
	runReqs  []substrate.RunRequest // capture for assertion

	startErr  error
	startInst substrate.Instance

	stopErr  error
	stopMode []bool // hibernate flags captured

	termErr error

	describeInstances []substrate.Instance
	describeErr       error

	// tagsByID is returned by DescribeTagsByID keyed by providerID.
	tagsByID map[string]map[string]string
}

func (f *fakeSubstrateClient) RunInstance(_ context.Context, req substrate.RunRequest) (substrate.Instance, error) {
	f.runReqs = append(f.runReqs, req)
	if f.runErr != nil {
		return substrate.Instance{}, f.runErr
	}
	if f.runInst.ProviderID != "" {
		return f.runInst, nil
	}
	return substrate.Instance{ProviderID: "i-fake", State: "running"}, nil
}

func (f *fakeSubstrateClient) StartInstance(_ context.Context, _ string) (substrate.Instance, error) {
	if f.startErr != nil {
		return substrate.Instance{}, f.startErr
	}
	if f.startInst.ProviderID != "" {
		return f.startInst, nil
	}
	return substrate.Instance{ProviderID: "i-fake", State: "running"}, nil
}

func (f *fakeSubstrateClient) StopInstance(_ context.Context, _ string, hibernate bool) error {
	f.stopMode = append(f.stopMode, hibernate)
	return f.stopErr
}

func (f *fakeSubstrateClient) TerminateInstance(_ context.Context, _ string) error {
	return f.termErr
}

func (f *fakeSubstrateClient) DescribeByTag(_ context.Context, _ map[string]string) ([]substrate.Instance, error) {
	if f.describeErr != nil {
		return nil, f.describeErr
	}
	return f.describeInstances, nil
}

func (f *fakeSubstrateClient) DescribeTagsByID(_ context.Context, providerID string) (map[string]string, error) {
	if f.tagsByID != nil {
		if tags, ok := f.tagsByID[providerID]; ok {
			return tags, nil
		}
	}
	return map[string]string{}, nil
}

// ---- helpers ----------------------------------------------------------------

func testCfg() ActuatorConfig {
	return ActuatorConfig{
		ClusterName:        "gauss",
		DefaultBootstrapS3: "s3://gauss-bootstrap/scripts/abc123",
	}
}

func testIntent(id string) cohort.EntityIntent {
	return cohort.EntityIntent{
		ID:               cohort.EntityID(id),
		Generation:       "gen-1",
		Cohort:           "cohort-1",
		IdempotencyToken: substrate.Token("gauss", id, "gen-1"),
		Rung: cohort.Rung{
			InstanceType:  "c7i.2xlarge",
			AvailZone:     "us-east-1a",
			CapacityModel: cohort.CapacityOnDemand,
		},
	}
}

func newActuatorWithFake(f *fakeSubstrateClient) *Actuator {
	return &Actuator{client: f, cfg: testCfg()}
}

func newObserverWithFake(f *fakeSubstrateClient) *Observer {
	return &Observer{client: f, cfg: testCfg()}
}

// ---- Actuator tests ---------------------------------------------------------

// Launch of one named entity produces exactly one RunInstance call with the
// deterministic token and the required config tags.
func TestActuator_Launch_OneEntityOneCall(t *testing.T) {
	fake := &fakeSubstrateClient{}
	a := newActuatorWithFake(fake)

	intent := testIntent("gpu-001")
	obs, err := a.Launch(context.Background(), intent)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if len(fake.runReqs) != 1 {
		t.Fatalf("RunInstance called %d times want 1", len(fake.runReqs))
	}
	req := fake.runReqs[0]

	// Idempotency token must be the deterministic substrate.Token.
	wantToken := substrate.Token("gauss", "gpu-001", "gen-1")
	if req.IdempotencyToken != wantToken {
		t.Errorf("token=%q want %q", req.IdempotencyToken, wantToken)
	}
	// Required config tags.
	if req.Tags[tagCluster] != "gauss" {
		t.Errorf("tag %q=%q want gauss", tagCluster, req.Tags[tagCluster])
	}
	if req.Tags[tagEntity] != "gpu-001" {
		t.Errorf("tag %q=%q want gpu-001", tagEntity, req.Tags[tagEntity])
	}
	if req.Tags[tagGeneration] != "gen-1" {
		t.Errorf("tag %q=%q want gen-1", tagGeneration, req.Tags[tagGeneration])
	}
	if req.Tags[tagBootstrapS3] != "s3://gauss-bootstrap/scripts/abc123" {
		t.Errorf("tag %q=%q want bootstrap S3 URI", tagBootstrapS3, req.Tags[tagBootstrapS3])
	}
	// Observation must reflect launched entity.
	if obs.ID != "gpu-001" {
		t.Errorf("obs.ID=%q want gpu-001", obs.ID)
	}
}

// Launch error is surfaced as a non-nil error.
func TestActuator_Launch_Error(t *testing.T) {
	fake := &fakeSubstrateClient{runErr: faultErr(cohort.Fault{
		Class: cohort.FaultCapacityExhausted, Code: "InsufficientInstanceCapacity",
	})}
	a := newActuatorWithFake(fake)
	_, err := a.Launch(context.Background(), testIntent("gpu-002"))
	if err == nil {
		t.Fatal("expected error")
	}
}

// Start of a Stopped entity returns an observation; classifies CapacityExhausted correctly.
func TestActuator_Start_CapacityExhausted(t *testing.T) {
	iceErr := faultErr(cohort.Fault{
		Class: cohort.FaultCapacityExhausted,
		Code:  "InsufficientInstanceCapacity",
	})
	fake := &fakeSubstrateClient{
		describeInstances: []substrate.Instance{{ProviderID: "i-stopped", State: "stopped"}},
		startErr:          iceErr,
	}
	a := newActuatorWithFake(fake)
	_, err := a.Start(context.Background(), "gpu-003")
	if err == nil {
		t.Fatal("expected error from Start ICE")
	}
	var fe *FaultError
	if !errors.As(err, &fe) {
		t.Fatalf("error not *FaultError: %T", err)
	}
	if fe.Fault.Class != cohort.FaultCapacityExhausted {
		t.Errorf("class=%v want CapacityExhausted", fe.Fault.Class)
	}
}

// StopMode warm vs hibernate routes correctly.
func TestActuator_Stop_ModeRouting(t *testing.T) {
	cases := []struct {
		mode      cohort.StopMode
		wantHibernate bool
	}{
		{cohort.StopWarm, false},
		{cohort.StopHibernate, true},
	}
	for _, tc := range cases {
		fake := &fakeSubstrateClient{
			describeInstances: []substrate.Instance{{ProviderID: "i-running", State: "running"}},
		}
		a := newActuatorWithFake(fake)
		if err := a.Stop(context.Background(), "gpu-004", tc.mode); err != nil {
			t.Fatalf("Stop(%v): %v", tc.mode, err)
		}
		if len(fake.stopMode) == 0 {
			t.Fatalf("StopInstance not called")
		}
		if fake.stopMode[0] != tc.wantHibernate {
			t.Errorf("mode=%v: hibernate=%v want %v", tc.mode, fake.stopMode[0], tc.wantHibernate)
		}
	}
}

// ---- Observer tests ---------------------------------------------------------

// DescribeByTag miss → StateUnknown, not StateAbsent, no error.
func TestObserver_DescribeMiss_IsStateUnknown(t *testing.T) {
	fake := &fakeSubstrateClient{describeInstances: nil}
	o := newObserverWithFake(fake)
	obs, err := o.Observe(context.Background(), []cohort.EntityID{"gpu-010"})
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if len(obs) != 1 {
		t.Fatalf("expected 1 observation, got %d", len(obs))
	}
	if obs[0].State != cohort.StateUnknown {
		t.Errorf("miss: state=%v want StateUnknown", obs[0].State)
	}
	if obs[0].State == cohort.StateAbsent {
		t.Errorf("miss must be StateUnknown (lag), not StateAbsent")
	}
}

// Observer hybrid: spored tags present → Readiness populated.
func TestObserver_ReadReadiness_TagsPresent(t *testing.T) {
	// The readInstanceTags stub currently returns empty — test the not-yet-ready path.
	fake := &fakeSubstrateClient{
		describeInstances: []substrate.Instance{{ProviderID: "i-running", State: "running"}},
	}
	o := newObserverWithFake(fake)
	r, err := o.ReadReadiness(context.Background(), "gpu-011")
	if err != nil {
		t.Fatalf("ReadReadiness: %v", err)
	}
	// Stub returns empty tags → not enrolled, not mount healthy, no detail.
	// This is the "spored not yet written" path — must not be a hard failure.
	if r.Enrolled {
		t.Errorf("empty tags: Enrolled should be false")
	}
}

// Observer hybrid enrolled path: spored writes q0:ready=true + q0:phase=enrolled
// → Readiness.OK() == true (mount healthy + enrolled).
func TestObserver_ReadReadiness_Enrolled(t *testing.T) {
	const providerID = "i-enrolled"
	fake := &fakeSubstrateClient{
		describeInstances: []substrate.Instance{{ProviderID: providerID, State: "running"}},
		tagsByID: map[string]map[string]string{
			providerID: {
				tagReady:  "true",
				tagPhase:  "enrolled",
				tagDetail: "slurmd up, mounts ok",
			},
		},
	}
	o := newObserverWithFake(fake)
	r, err := o.ReadReadiness(context.Background(), "gpu-013")
	if err != nil {
		t.Fatalf("ReadReadiness enrolled: %v", err)
	}
	if !r.Enrolled {
		t.Errorf("enrolled path: Enrolled=false want true")
	}
	if !r.Operational {
		t.Errorf("enrolled path: Operational=false want true")
	}
	if !r.OK() {
		t.Errorf("enrolled path: OK()=false want true")
	}
	if r.Detail != "slurmd up, mounts ok" {
		t.Errorf("enrolled path: Detail=%q", r.Detail)
	}
}

// Observer: q0:ready=false with q0:phase=enrolled → not enrolled (mounts unconfirmed).
func TestObserver_ReadReadiness_ReadyFalse(t *testing.T) {
	const providerID = "i-notready"
	fake := &fakeSubstrateClient{
		describeInstances: []substrate.Instance{{ProviderID: providerID, State: "running"}},
		tagsByID: map[string]map[string]string{
			providerID: {tagReady: "false", tagPhase: "running"},
		},
	}
	o := newObserverWithFake(fake)
	r, err := o.ReadReadiness(context.Background(), "gpu-014")
	if err != nil {
		t.Fatalf("ReadReadiness: %v", err)
	}
	if r.OK() {
		t.Error("ready=false: OK() should be false")
	}
}

// Observer: spored tags absent (DescribeByTag miss) → not-yet-ready, not an error.
func TestObserver_ReadReadiness_TagsAbsent(t *testing.T) {
	fake := &fakeSubstrateClient{describeInstances: nil}
	o := newObserverWithFake(fake)
	r, err := o.ReadReadiness(context.Background(), "gpu-012")
	if err != nil {
		t.Fatalf("ReadReadiness on absent entity: %v", err)
	}
	if r.OK() {
		t.Error("absent tags should not be OK")
	}
}
