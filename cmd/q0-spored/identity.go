package main

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"

	"github.com/queuezero/queuezero/internal/spored"
	"github.com/queuezero/queuezero/internal/tags"
)

// tagReader is the slice of the substrate client this needs: read the node's own
// tags. *aws.Client.DescribeTagsByID satisfies it.
type tagReader interface {
	DescribeTagsByID(ctx context.Context, providerID string) (map[string]string, error)
}

// imdsIdentity resolves the node's identity from IMDS (instance ID) plus its own
// EC2 tags (q0:entity / q0:cluster) read back through the substrate client. It
// is the production spored.IdentitySource; the package internal/spored stays
// SDK-free and is tested with fakes instead.
type imdsIdentity struct {
	imds   *imds.Client
	reader tagReader
}

func newIMDSIdentity(imdsClient *imds.Client, reader tagReader) *imdsIdentity {
	return &imdsIdentity{imds: imdsClient, reader: reader}
}

func (s *imdsIdentity) Identify(ctx context.Context) (spored.Identity, error) {
	doc, err := s.imds.GetInstanceIdentityDocument(ctx, &imds.GetInstanceIdentityDocumentInput{})
	if err != nil {
		return spored.Identity{}, fmt.Errorf("imds identity document: %w", err)
	}
	providerID := doc.InstanceID

	// Read the node's own config tags written by ASBX at launch.
	kv, err := s.reader.DescribeTagsByID(ctx, providerID)
	if err != nil {
		return spored.Identity{}, fmt.Errorf("read own tags: %w", err)
	}
	return spored.Identity{
		ProviderID: providerID,
		Entity:     kv[tags.Entity],
		Cluster:    kv[tags.Cluster],
	}, nil
}
