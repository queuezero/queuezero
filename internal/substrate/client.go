package substrate

import "context"

// Client is the single chokepoint to a cloud provider's elastic-fleet API.
// It wraps a raw provider SDK with idempotency, classification, and rate
// limiting. Provider-specific construction lives in subpackages (aws.New...).
type Client interface {
	// Mutations carry idempotency tokens and pass through the rate limiter.
	RunInstance(ctx context.Context, req RunRequest) (Instance, error)
	StartInstance(ctx context.Context, id string) (Instance, error)
	StopInstance(ctx context.Context, id string, hibernate bool) error
	TerminateInstance(ctx context.Context, id string) error

	// DescribeByTag is eventually consistent and ADVISORY. Callers must treat
	// a miss as lag and consult the idempotency token for ground truth.
	DescribeByTag(ctx context.Context, tags map[string]string) ([]Instance, error)
}

// RunRequest is a single-instance launch. There is deliberately no
// "RunInstances(count)" — the named entity is the unit. See ARCHITECTURE §9.
type RunRequest struct {
	InstanceType     string
	AvailZone        string
	SubnetID         string
	SecurityGroupIDs []string
	AMI              string
	Spot             bool
	IdempotencyToken string            // deterministic in (cluster, entity, generation)
	Tags             map[string]string // MUST include cluster + entity + generation
	IAMInstanceArn   string
	EFA              bool
	PlacementGroup   string
}

// Instance is the provider-agnostic view of one running thing.
type Instance struct {
	ProviderID   string
	State        string
	PrivateAddr  string
	InstanceType string
	AvailZone    string
}
