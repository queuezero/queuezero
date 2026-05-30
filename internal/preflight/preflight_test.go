package preflight

import (
	"context"
	"errors"
	"testing"

	"github.com/queuezero/queuezero/internal/spec"
)

// fakeChecker answers from in-memory sets; unknown => not offered/exists.
type fakeChecker struct {
	zones      []string
	offered    map[string]bool // "type@az" -> offered
	images     map[string]bool
	subnets    map[string]bool
	azErr      error
}

func (f fakeChecker) AvailabilityZones(context.Context) ([]string, error) {
	if f.azErr != nil {
		return nil, f.azErr
	}
	return f.zones, nil
}
func (f fakeChecker) InstanceTypeOffered(_ context.Context, it, az string) (bool, error) {
	return f.offered[it+"@"+az], nil
}
func (f fakeChecker) ImageExists(_ context.Context, ami string) (bool, error) { return f.images[ami], nil }
func (f fakeChecker) SubnetExists(_ context.Context, id string) (bool, error) { return f.subnets[id], nil }

func cluster() *spec.Cluster {
	return &spec.Cluster{
		Name: "gauss", Region: "us-east-1",
		Network:    spec.NetworkSpec{BYO: true, SubnetIDs: []string{"subnet-1"}},
		Controller: spec.ControllerSpec{InstanceType: "m7i.large", AMIHash: "ami-1"},
	}
}

func partitions() *spec.Partitions {
	return &spec.Partitions{Partitions: []spec.Partition{
		{Name: "gpu", FallbackChain: []spec.Rung{
			{InstanceType: "p5.48xlarge", AvailZone: "us-east-1a", CapacityModel: "ondemand"},
			{InstanceType: "p5.48xlarge", AvailZone: "us-east-1b", CapacityModel: "spot"},
		}},
	}}
}

func TestRun_AllPass(t *testing.T) {
	c := fakeChecker{
		zones:   []string{"us-east-1a", "us-east-1b"},
		offered: map[string]bool{"p5.48xlarge@us-east-1a": true, "p5.48xlarge@us-east-1b": true},
		images:  map[string]bool{"ami-1": true},
		subnets: map[string]bool{"subnet-1": true},
	}
	rep, err := Run(context.Background(), cluster(), partitions(), c)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !rep.OK() {
		t.Errorf("expected all OK, got %+v", rep.Results)
	}
	// ami + subnet + 2 rungs = 4 checks.
	if len(rep.Results) != 4 {
		t.Errorf("want 4 results, got %d", len(rep.Results))
	}
}

func TestRun_CollectsAllFailures(t *testing.T) {
	c := fakeChecker{
		zones:   []string{"us-east-1a", "us-east-1b"},
		offered: map[string]bool{"p5.48xlarge@us-east-1a": true}, // 1b NOT offered
		images:  map[string]bool{},                              // ami missing
		subnets: map[string]bool{},                              // subnet missing
	}
	rep, _ := Run(context.Background(), cluster(), partitions(), c)
	if rep.OK() {
		t.Fatal("expected failures")
	}
	fails := map[string]bool{}
	for _, r := range rep.Results {
		if !r.OK {
			fails[r.Check] = true
		}
	}
	for _, want := range []string{"ami-exists", "subnet-exists", "instance-type-offered"} {
		if !fails[want] {
			t.Errorf("expected a %q failure; results=%+v", want, rep.Results)
		}
	}
}

func TestRun_BadAZ(t *testing.T) {
	parts := &spec.Partitions{Partitions: []spec.Partition{
		{Name: "gpu", FallbackChain: []spec.Rung{{InstanceType: "p5.48xlarge", AvailZone: "us-east-1z"}}},
	}}
	c := fakeChecker{zones: []string{"us-east-1a"}, images: map[string]bool{"ami-1": true}, subnets: map[string]bool{"subnet-1": true}}
	rep, _ := Run(context.Background(), cluster(), parts, c)
	var sawAZ bool
	for _, r := range rep.Results {
		if r.Check == "az-valid" && !r.OK {
			sawAZ = true
		}
	}
	if !sawAZ {
		t.Errorf("expected an az-valid failure for us-east-1z; got %+v", rep.Results)
	}
}

func TestRun_NoPartitions_ClusterChecksOnly(t *testing.T) {
	c := fakeChecker{zones: []string{"us-east-1a"}, images: map[string]bool{"ami-1": true}, subnets: map[string]bool{"subnet-1": true}}
	rep, err := Run(context.Background(), cluster(), nil, c)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK() || len(rep.Results) != 2 { // ami + subnet only
		t.Errorf("cluster-only run = %+v", rep.Results)
	}
}

func TestRun_AZListingError(t *testing.T) {
	c := fakeChecker{azErr: errors.New("AuthFailure")}
	if _, err := Run(context.Background(), cluster(), partitions(), c); err == nil {
		t.Error("AZ listing failure should return an error (checks could not run)")
	}
}
