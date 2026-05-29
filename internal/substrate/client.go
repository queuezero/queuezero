package substrate

import (
	"context"
	"time"
)

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

	// Tag writes (creates/overwrites) tags on one instance. This is the on-node
	// reporter's signal channel (q0:phase/q0:ready/q0:detail). Idempotent.
	Tag(ctx context.Context, providerID string, tags map[string]string) error
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

	// UserData is the raw (un-encoded) instance userdata — a minimal
	// fetch-and-exec shim ONLY, never application logic (ARCHITECTURE §11,
	// non-negotiable #9). The aws layer base64-encodes it; it is immutable and
	// size-capped once launched. Empty => no userdata (bare AMI boot).
	UserData string
}

// Instance is the provider-agnostic view of one running thing.
type Instance struct {
	ProviderID   string
	State        string
	PrivateAddr  string
	InstanceType string
	AvailZone    string

	// Generation is the q0:generation tag value ("" if unset). The orphan
	// sweeper reaps instances whose generation is superseded by the current
	// spec generation. See ARCHITECTURE §12.
	Generation string
	// Entity is the q0:entity tag value ("" if unset) — the named entity this
	// instance backs. The sweeper terminates by EntityID, never by raw provider
	// ID through a bulk path (non-negotiable #2).
	Entity string
	// LaunchTime is the provider-reported launch time, used by the sweeper's
	// grace period to tolerate the eventual consistency of tag-filtered Describe.
	LaunchTime time.Time
}
