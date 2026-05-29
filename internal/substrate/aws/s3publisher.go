package aws

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3API is the narrow S3 surface the manifest publisher programs against —
// PutObject only. The real *s3.Client satisfies it; tests inject a fake. Kept
// minimal in the same spirit as EC2API (ec2iface.go): the publisher's whole job
// is to write one object, so it needs nothing else.
type S3API interface {
	PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	// HeadObject reports whether an object exists (and its metadata). Used by the
	// content-addressed bootstrap uploader to skip a redundant upload.
	HeadObject(ctx context.Context, in *s3.HeadObjectInput, opts ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
}

// S3Publisher implements mpi.ManifestPublisher (structurally — no mpi import
// here) by writing the converged peer manifest to S3. This is the §11 payload
// channel: tags carry small signals, S3 carries payloads. The manifest is
// written once per assembly, after the cohort barrier, for members to re-fetch.
//
// Unlike the EC2 substrate.Client, the publisher does NOT pass through the
// account rate limiter or the fault classifier: those are EC2-control-plane
// concerns (throttle-prone mutations, capacity classification). An S3 PutObject
// is a payload write on a separate service with its own SDK retry/throttle and a
// far higher request ceiling; a failure simply fails the assembly phase with the
// verbatim provider error wrapped via %w. No fault-class translation is useful
// here, and routing it through the EC2 token bucket would conflate two unrelated
// rate budgets.
type S3Publisher struct {
	s3     S3API
	bucket string
}

// NewS3Publisher constructs a publisher that writes manifests under bucket.
func NewS3Publisher(s3api S3API, bucket string) *S3Publisher {
	return &S3Publisher{s3: s3api, bucket: bucket}
}

// Publish writes data to s3://<bucket>/<key>. key is built by the Assembler as
// "manifests/<cohort>/peers.json"; data is the serialized PeerManifest JSON.
func (p *S3Publisher) Publish(ctx context.Context, key string, data []byte) error {
	if p.bucket == "" {
		return errors.New("s3publisher: empty bucket — manifest publishing not configured")
	}
	_, err := p.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      awssdk.String(p.bucket),
		Key:         awssdk.String(key),
		Body:        bytes.NewReader(data),
		ContentType: awssdk.String("application/json"),
	})
	if err != nil {
		return fmt.Errorf("s3publisher: put %s/%s: %w", p.bucket, key, err)
	}
	return nil
}
