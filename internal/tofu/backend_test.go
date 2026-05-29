package tofu

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type fakeS3Backend struct {
	bucketExists bool
	headErr      error
	created      bool
}

func (f *fakeS3Backend) HeadBucket(_ context.Context, _ *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
	if f.headErr != nil {
		return nil, f.headErr
	}
	if f.bucketExists {
		return &s3.HeadBucketOutput{}, nil
	}
	return nil, &s3types.NotFound{}
}

func (f *fakeS3Backend) CreateBucket(_ context.Context, _ *s3.CreateBucketInput, _ ...func(*s3.Options)) (*s3.CreateBucketOutput, error) {
	f.created = true
	return &s3.CreateBucketOutput{}, nil
}

type fakeDDB struct {
	tableExists  bool
	describeErr  error
	created      bool
}

func (f *fakeDDB) DescribeTable(_ context.Context, _ *dynamodb.DescribeTableInput, _ ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error) {
	if f.describeErr != nil {
		return nil, f.describeErr
	}
	if f.tableExists {
		return &dynamodb.DescribeTableOutput{}, nil
	}
	return nil, &ddbtypes.ResourceNotFoundException{}
}

func (f *fakeDDB) CreateTable(_ context.Context, _ *dynamodb.CreateTableInput, _ ...func(*dynamodb.Options)) (*dynamodb.CreateTableOutput, error) {
	f.created = true
	return &dynamodb.CreateTableOutput{}, nil
}

func cfg() BackendConfig {
	return BackendConfig{Bucket: "gauss-q0-state", LockTable: "gauss-q0-lock", Region: "us-east-1", Key: "cluster/terraform.tfstate"}
}

func TestEnsureBackend_CreatesWhenAbsent(t *testing.T) {
	s3c := &fakeS3Backend{bucketExists: false}
	ddb := &fakeDDB{tableExists: false}
	if err := EnsureBackend(context.Background(), cfg(), s3c, ddb); err != nil {
		t.Fatalf("EnsureBackend: %v", err)
	}
	if !s3c.created {
		t.Error("absent bucket should be created")
	}
	if !ddb.created {
		t.Error("absent lock table should be created")
	}
}

func TestEnsureBackend_SkipsWhenPresent(t *testing.T) {
	s3c := &fakeS3Backend{bucketExists: true}
	ddb := &fakeDDB{tableExists: true}
	if err := EnsureBackend(context.Background(), cfg(), s3c, ddb); err != nil {
		t.Fatalf("EnsureBackend: %v", err)
	}
	if s3c.created || ddb.created {
		t.Error("existing backend resources must not be recreated")
	}
}

func TestEnsureBackend_HeadErrorSurfaces(t *testing.T) {
	s3c := &fakeS3Backend{headErr: errors.New("AccessDenied")}
	if err := EnsureBackend(context.Background(), cfg(), s3c, &fakeDDB{}); err == nil {
		t.Fatal("a non-NotFound head error must surface")
	}
}

func TestEnsureBackend_RequiresFields(t *testing.T) {
	if err := EnsureBackend(context.Background(), BackendConfig{}, &fakeS3Backend{}, &fakeDDB{}); err == nil {
		t.Error("empty backend config should error")
	}
}

func TestBackendConfig_InitArgs(t *testing.T) {
	args := cfg().initArgs()
	want := map[string]bool{
		"bucket=gauss-q0-state": true, "key=cluster/terraform.tfstate": true,
		"region=us-east-1": true, "dynamodb_table=gauss-q0-lock": true,
	}
	for _, a := range args {
		if !want[a] {
			t.Errorf("unexpected init arg %q", a)
		}
		delete(want, a)
	}
	if len(want) != 0 {
		t.Errorf("missing init args: %v", want)
	}
}
