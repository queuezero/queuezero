package aws

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/queuezero/queuezero/internal/mpi"
)

// fakeS3 captures the last PutObject call and can inject an error. It also
// models HeadObject for the content-addressed uploader's skip-if-exists check.
type fakeS3 struct {
	putErr     error
	lastBucket string
	lastKey    string
	lastBody   []byte
	calls      int

	headExists bool  // HeadObject reports the object present
	headErr    error // a non-NotFound error to inject from HeadObject
	headCalls  int
}

func (f *fakeS3) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.calls++
	if in.Bucket != nil {
		f.lastBucket = *in.Bucket
	}
	if in.Key != nil {
		f.lastKey = *in.Key
	}
	if in.Body != nil {
		f.lastBody, _ = io.ReadAll(in.Body)
	}
	if f.putErr != nil {
		return nil, f.putErr
	}
	return &s3.PutObjectOutput{}, nil
}

func (f *fakeS3) HeadObject(_ context.Context, _ *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	f.headCalls++
	if f.headErr != nil {
		return nil, f.headErr
	}
	if f.headExists {
		return &s3.HeadObjectOutput{}, nil
	}
	return nil, &s3types.NotFound{}
}

// *S3Publisher must satisfy mpi.ManifestPublisher (the seam slurm.NewAssembler needs).
var _ mpi.ManifestPublisher = (*S3Publisher)(nil)

func TestS3Publisher_PutsKeyBucketBody(t *testing.T) {
	manifest := mpi.PeerManifest{
		Cohort: "gpu-001",
		Peers: []mpi.Peer{
			{Rank: 0, Entity: "gpu-001", Address: "10.0.0.10"},
			{Rank: 1, Entity: "gpu-002", Address: "10.0.0.11"},
		},
	}
	data, _ := json.Marshal(manifest)

	f := &fakeS3{}
	p := NewS3Publisher(f, "gauss-q0-state")
	key := "manifests/gpu-001/peers.json"
	if err := p.Publish(context.Background(), key, data); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if f.lastBucket != "gauss-q0-state" {
		t.Errorf("bucket=%q want gauss-q0-state", f.lastBucket)
	}
	if f.lastKey != key {
		t.Errorf("key=%q want %q", f.lastKey, key)
	}
	var got mpi.PeerManifest
	if err := json.Unmarshal(f.lastBody, &got); err != nil {
		t.Fatalf("body not valid manifest JSON: %v", err)
	}
	if len(got.Peers) != 2 || got.Peers[1].Entity != "gpu-002" {
		t.Errorf("round-tripped manifest wrong: %+v", got)
	}
}

func TestS3Publisher_WrapsError(t *testing.T) {
	f := &fakeS3{putErr: errors.New("AccessDenied")}
	p := NewS3Publisher(f, "b")
	err := p.Publish(context.Background(), "manifests/c/peers.json", []byte("{}"))
	if err == nil {
		t.Fatal("expected error")
	}
	if got := err.Error(); !strings.Contains(got, "AccessDenied") || !strings.Contains(got, "manifests/c/peers.json") {
		t.Errorf("error should wrap key + verbatim provider message, got %q", got)
	}
}

func TestS3Publisher_EmptyBucket(t *testing.T) {
	p := NewS3Publisher(&fakeS3{}, "")
	if err := p.Publish(context.Background(), "k", []byte("{}")); err == nil {
		t.Error("empty bucket should error")
	}
}
