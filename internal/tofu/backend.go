package tofu

import (
	"context"
	"errors"
	"fmt"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// BackendConfig is the S3 + DynamoDB state backend for a tofu working dir
// (ARCHITECTURE §2: "state in S3 + DynamoDB lock"). It is chicken-and-egg —
// tofu cannot create the bucket that holds its own state — so EnsureBackend
// provisions it via the SDK before `tofu init`.
type BackendConfig struct {
	Bucket    string // S3 bucket holding tofu state
	LockTable string // DynamoDB table for state locking
	Region    string
	Key       string // state object key, e.g. "cluster/terraform.tfstate"
}

// initArgs renders the -backend-config key=value pairs for `tofu init`.
func (b BackendConfig) initArgs() []string {
	return []string{
		"bucket=" + b.Bucket,
		"key=" + b.Key,
		"region=" + b.Region,
		"dynamodb_table=" + b.LockTable,
	}
}

// RenderBackendHCL renders the `terraform { backend "s3" {} }` block. The
// concrete values are supplied at init time via -backend-config (initArgs), so
// the generated HCL stays free of account-specific names.
func RenderBackendHCL() string {
	return "terraform {\n  backend \"s3\" {}\n}\n"
}

// s3BackendAPI is the slice of S3 the backend bootstrap needs.
type s3BackendAPI interface {
	HeadBucket(ctx context.Context, in *s3.HeadBucketInput, opts ...func(*s3.Options)) (*s3.HeadBucketOutput, error)
	CreateBucket(ctx context.Context, in *s3.CreateBucketInput, opts ...func(*s3.Options)) (*s3.CreateBucketOutput, error)
}

// ddbBackendAPI is the slice of DynamoDB the backend bootstrap needs.
type ddbBackendAPI interface {
	DescribeTable(ctx context.Context, in *dynamodb.DescribeTableInput, opts ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error)
	CreateTable(ctx context.Context, in *dynamodb.CreateTableInput, opts ...func(*dynamodb.Options)) (*dynamodb.CreateTableOutput, error)
}

// EnsureBackend creates the state bucket and lock table if absent, idempotently.
// It is safe to call before every apply: existing resources are left untouched.
func EnsureBackend(ctx context.Context, cfg BackendConfig, s3c s3BackendAPI, ddb ddbBackendAPI) error {
	if cfg.Bucket == "" || cfg.LockTable == "" || cfg.Region == "" {
		return errors.New("tofu: backend requires bucket, lock table, and region")
	}
	if err := ensureBucket(ctx, s3c, cfg.Bucket, cfg.Region); err != nil {
		return err
	}
	return ensureLockTable(ctx, ddb, cfg.LockTable)
}

func ensureBucket(ctx context.Context, s3c s3BackendAPI, bucket, region string) error {
	_, err := s3c.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: awssdk.String(bucket)})
	if err == nil {
		return nil // already exists
	}
	var notFound *s3types.NotFound
	if !errors.As(err, &notFound) {
		return fmt.Errorf("tofu: head state bucket %s: %w", bucket, err)
	}
	in := &s3.CreateBucketInput{Bucket: awssdk.String(bucket)}
	// us-east-1 must NOT carry a location constraint; every other region must.
	if region != "us-east-1" {
		in.CreateBucketConfiguration = &s3types.CreateBucketConfiguration{
			LocationConstraint: s3types.BucketLocationConstraint(region),
		}
	}
	if _, err := s3c.CreateBucket(ctx, in); err != nil {
		return fmt.Errorf("tofu: create state bucket %s: %w", bucket, err)
	}
	return nil
}

func ensureLockTable(ctx context.Context, ddb ddbBackendAPI, table string) error {
	_, err := ddb.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: awssdk.String(table)})
	if err == nil {
		return nil // already exists
	}
	var notFound *ddbtypes.ResourceNotFoundException
	if !errors.As(err, &notFound) {
		return fmt.Errorf("tofu: describe lock table %s: %w", table, err)
	}
	// The tofu S3 backend locks on a "LockID" string hash key.
	_, err = ddb.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:   awssdk.String(table),
		BillingMode: ddbtypes.BillingModePayPerRequest,
		AttributeDefinitions: []ddbtypes.AttributeDefinition{
			{AttributeName: awssdk.String("LockID"), AttributeType: ddbtypes.ScalarAttributeTypeS},
		},
		KeySchema: []ddbtypes.KeySchemaElement{
			{AttributeName: awssdk.String("LockID"), KeyType: ddbtypes.KeyTypeHash},
		},
	})
	if err != nil {
		return fmt.Errorf("tofu: create lock table %s: %w", table, err)
	}
	return nil
}
