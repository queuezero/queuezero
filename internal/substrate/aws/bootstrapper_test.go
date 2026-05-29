package aws

import (
	"context"
	"errors"
	"testing"

	"github.com/queuezero/queuezero/internal/bootstrap"
)

const testDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestBootstrapUploader_UploadsWhenAbsent(t *testing.T) {
	f := &fakeS3{headExists: false}
	u := NewBootstrapUploader(f, "gauss-q0-scripts")

	uri, skipped, err := u.PutScriptSet(context.Background(), testDigest, []byte("tarball-bytes"))
	if err != nil {
		t.Fatalf("PutScriptSet: %v", err)
	}
	if skipped {
		t.Error("absent object should be uploaded, not skipped")
	}
	wantURI := "s3://gauss-q0-scripts/scripts/" + testDigest + ".tar.gz"
	if uri != wantURI {
		t.Errorf("uri=%q want %q", uri, wantURI)
	}
	if f.calls != 1 {
		t.Errorf("PutObject called %d times, want 1", f.calls)
	}
	if f.lastKey != "scripts/"+testDigest+".tar.gz" {
		t.Errorf("key=%q", f.lastKey)
	}
	if string(f.lastBody) != "tarball-bytes" {
		t.Errorf("body=%q", f.lastBody)
	}
}

func TestBootstrapUploader_SkipsWhenPresent(t *testing.T) {
	f := &fakeS3{headExists: true}
	u := NewBootstrapUploader(f, "gauss-q0-scripts")

	uri, skipped, err := u.PutScriptSet(context.Background(), testDigest, []byte("x"))
	if err != nil {
		t.Fatalf("PutScriptSet: %v", err)
	}
	if !skipped {
		t.Error("existing content-addressed object should be skipped")
	}
	if f.calls != 0 {
		t.Errorf("PutObject should not be called when object exists, got %d", f.calls)
	}
	if uri == "" {
		t.Error("URI should still be returned on skip")
	}
}

func TestBootstrapUploader_HeadErrorSurfaces(t *testing.T) {
	f := &fakeS3{headErr: errors.New("AccessDenied")}
	u := NewBootstrapUploader(f, "b")
	if _, _, err := u.PutScriptSet(context.Background(), testDigest, []byte("x")); err == nil {
		t.Fatal("a non-NotFound Head error must surface")
	}
}

func TestBootstrapUploader_EmptyBucket(t *testing.T) {
	u := NewBootstrapUploader(&fakeS3{}, "")
	if _, _, err := u.PutScriptSet(context.Background(), testDigest, []byte("x")); err == nil {
		t.Error("empty bucket should error")
	}
}

// The producer's key must be exactly what the consumer's parser accepts — this
// closes the produce→pin→launch contract across packages.
func TestBootstrapUploader_KeyMatchesConsumerContract(t *testing.T) {
	uri := "s3://b/" + bootstrap.ScriptKey(testDigest)
	got, err := sha256FromKey(uri)
	if err != nil {
		t.Fatalf("consumer sha256FromKey rejected producer key %q: %v", uri, err)
	}
	if got != testDigest {
		t.Errorf("round-trip digest=%q want %q", got, testDigest)
	}
}
