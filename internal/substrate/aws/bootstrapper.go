package aws

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/queuezero/queuezero/internal/bootstrap"
)

// BootstrapUploader publishes content-addressed bootstrap script-sets to S3 —
// the producer side of the §11 payload channel, consumed by the userdata shim
// (internal/bootstrap.Shim) at launch. It mirrors S3Publisher's discipline (no
// rate limiter, no fault classifier — those are EC2-control-plane concerns; an
// S3 payload write fails loudly with the verbatim wrapped error).
type BootstrapUploader struct {
	s3     S3API
	bucket string
}

// NewBootstrapUploader constructs an uploader that writes script-sets under
// bucket (the Q0_SCRIPTS_BUCKET).
func NewBootstrapUploader(s3api S3API, bucket string) *BootstrapUploader {
	return &BootstrapUploader{s3: s3api, bucket: bucket}
}

// PutScriptSet uploads data under scripts/<sha256>.tar.gz and returns the full
// s3:// URI to pin in Q0_BOOTSTRAP_S3. Because the key is content-addressed, an
// object that already exists is byte-identical, so PutScriptSet HEADs first and
// skips the upload when present (returning the URI and skipped=true). The digest
// MUST be the lowercase-hex sha256 of data (bootstrap.Pack guarantees this).
func (u *BootstrapUploader) PutScriptSet(ctx context.Context, sha256hex string, data []byte) (uri string, skipped bool, err error) {
	if u.bucket == "" {
		return "", false, errors.New("bootstrapper: empty bucket — set Q0_SCRIPTS_BUCKET or --bucket")
	}
	key := bootstrap.ScriptKey(sha256hex)
	uri = fmt.Sprintf("s3://%s/%s", u.bucket, key)

	exists, err := u.head(ctx, key)
	if err != nil {
		return "", false, err
	}
	if exists {
		return uri, true, nil
	}

	if _, err := u.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      awssdk.String(u.bucket),
		Key:         awssdk.String(key),
		Body:        bytes.NewReader(data),
		ContentType: awssdk.String("application/gzip"),
	}); err != nil {
		return "", false, fmt.Errorf("bootstrapper: put %s/%s: %w", u.bucket, key, err)
	}
	return uri, false, nil
}

// head reports whether key exists in the bucket. A NotFound is "absent" (not an
// error); any other error surfaces wrapped.
func (u *BootstrapUploader) head(ctx context.Context, key string) (bool, error) {
	_, err := u.s3.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: awssdk.String(u.bucket),
		Key:    awssdk.String(key),
	})
	if err == nil {
		return true, nil
	}
	var notFound *s3types.NotFound
	if errors.As(err, &notFound) {
		return false, nil
	}
	return false, fmt.Errorf("bootstrapper: head %s/%s: %w", u.bucket, key, err)
}
