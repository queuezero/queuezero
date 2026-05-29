package slurm

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/queuezero/queuezero/internal/cohort"
	"github.com/queuezero/queuezero/internal/mpi"
	"github.com/queuezero/queuezero/internal/recordstore"
	"github.com/queuezero/queuezero/internal/spec"
	awssub "github.com/queuezero/queuezero/internal/substrate/aws"
)

// fakeS3API satisfies aws.S3API so we can drive the REAL aws.S3Publisher.
type fakeS3API struct {
	mu       sync.Mutex
	objects  map[string][]byte
}

func (f *fakeS3API) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.objects == nil {
		f.objects = map[string][]byte{}
	}
	body, _ := io.ReadAll(in.Body)
	f.objects[*in.Key] = body
	return &s3.PutObjectOutput{}, nil
}

// HeadObject is unused by S3Publisher (manifest publishing always PUTs), but
// required to satisfy the aws.S3API interface.
func (f *fakeS3API) HeadObject(_ context.Context, _ *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	return &s3.HeadObjectOutput{}, nil
}

// The Step 6 unblock proof: a collective resume now reconciles through the REAL
// aws.S3Publisher (no longer nil), publishing a complete peer manifest. This is
// the production path the 2a gate refused; here it runs end to end against a
// fake S3 with no real AWS.
func TestResume_Collective_PublishesManifestViaS3Publisher(t *testing.T) {
	addrs := map[cohort.EntityID]string{
		"gpu-001": "10.0.0.10", "gpu-002": "10.0.0.11",
		"gpu-003": "10.0.0.12", "gpu-004": "10.0.0.13",
	}
	sc := newFakeScontrol("gpu-001", "gpu-002", "gpu-003", "gpu-004")
	act := &fakeActuator{addresses: addrs}
	obs := &fakeObserver{addresses: addrs}

	s3fake := &fakeS3API{}
	asm := NewAssembler(awssub.NewS3Publisher(s3fake, "gauss-q0-state"))

	part := spec.Partition{
		Name: "gpu", ExecutionAccount: "111122223333", Collective: true,
		FallbackChain: []spec.Rung{ondemand("p5.48xlarge", "us-east-1a")},
		Budget: &spec.BudgetSpec{
			LaunchAcked: time.Second, Running: time.Second, Enrolled: time.Second,
			CohortBarrier: 5 * time.Second, CohortAssembly: 5 * time.Second,
		},
	}

	store, err := recordstore.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	enr := NewEnroller(&fakeProbe{addresses: addrs})
	b := &Bridge{
		Reconciler: func(a cohort.Assembler) *cohort.Reconciler {
			return cohort.NewReconciler(act, obs, fakeClassifier{}, enr, a, nil)
		},
		Actuator:  act,
		Assembler: asm, // real S3-backed assembler — the unblock
		Scontrol:  sc,
		Records:   store,
		Cfg: Config{
			Cluster: "test", Generation: "g1",
			Partitions:       &spec.Partitions{Partitions: []spec.Partition{part}},
			DefaultPartition: "gpu",
		},
	}

	if err := b.Resume(context.Background(), "gpu", "gpu-[001-004]"); err != nil {
		t.Fatalf("collective Resume: %v", err)
	}
	// No node should have been marked down/drain — all four came up.
	if sc.updateCount() != 0 {
		t.Errorf("healthy collective should not touch scontrol, got %d updates", sc.updateCount())
	}

	// Exactly one manifest published under the expected key, with 4 ranked peers.
	s3fake.mu.Lock()
	defer s3fake.mu.Unlock()
	if len(s3fake.objects) != 1 {
		t.Fatalf("expected 1 published object, got %d (%v)", len(s3fake.objects), keysOf(s3fake.objects))
	}
	data, ok := s3fake.objects["manifests/gpu-001/peers.json"]
	if !ok {
		t.Fatalf("manifest not at expected key; got %v", keysOf(s3fake.objects))
	}
	var m mpi.PeerManifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("manifest not valid JSON: %v", err)
	}
	if len(m.Peers) != 4 {
		t.Errorf("manifest has %d peers, want 4", len(m.Peers))
	}
}

func keysOf(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
