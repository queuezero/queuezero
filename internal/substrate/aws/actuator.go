package aws

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/queuezero/queuezero/internal/cohort"
	"github.com/queuezero/queuezero/internal/substrate"
	"github.com/queuezero/queuezero/internal/tags"
)

// Tag key constants for the q0 control channel — aliased from internal/tags,
// the single source of truth shared with the on-node reporter (internal/spored)
// so the writer and this reader can never drift. Tags carry small signals; S3
// carries payloads (ARCHITECTURE §11).
const (
	tagCluster     = tags.Cluster
	tagEntity      = tags.Entity
	tagGeneration  = tags.Generation
	tagCohort      = tags.Cohort
	tagBootstrapS3 = tags.BootstrapS3

	// spored-written readiness tags (phase 3, read-only for the observer).
	tagPhase  = tags.Phase
	tagReady  = tags.Ready
	tagDetail = tags.Detail
)

// ActuatorConfig holds the account + cluster configuration needed to launch
// an entity. A single Actuator serves one execution account; queuezero creates
// one per (execution account, region) pair.
type ActuatorConfig struct {
	// ClusterName is included in every tag set for scoping.
	ClusterName string
	// DefaultBootstrapS3 is the S3 URI of the hash-pinned bootstrap script-set.
	// Overridden per-entity if the Rung specifies one.
	DefaultBootstrapS3 string
	// Region is the AWS region for this Actuator's account.
	Region string

	// CrossAccountRoleARN is the IAM role assumed to reach the execution account.
	// TODO(step-4-cross-account): assume this role before constructing the
	// substrate.Client for each Launch/Start call. Empty = single-account path.
	CrossAccountRoleARN string
}

// substrateClient is the interface the Actuator and Observer program against.
// *Client satisfies it; tests inject a fake. Kept package-internal so it
// cannot be satisfied by external callers bypassing the production Client.
type substrateClient interface {
	RunInstance(ctx context.Context, req substrate.RunRequest) (substrate.Instance, error)
	StartInstance(ctx context.Context, id string) (substrate.Instance, error)
	StopInstance(ctx context.Context, id string, hibernate bool) error
	TerminateInstance(ctx context.Context, id string) error
	DescribeByTag(ctx context.Context, tags map[string]string) ([]substrate.Instance, error)
	DescribeTagsByID(ctx context.Context, providerID string) (map[string]string, error)
}

// Actuator implements cohort.Actuator over substrate.Client. Every call
// names exactly one entity — no count-shaped call anywhere in this file.
//
// spawn integration note (B2): spawn's off-node launch API does not exist as
// a callable Go library function; spawn's pkg/provider/ec2.go operates on-node
// via IMDS self-identity. The Step 6 transplant goes in the other direction:
// spawn's orchestrator.scaleUp(count) is replaced by cohort.Reconciler calling
// this Actuator. This file implements the Actuator directly over substrate.Client
// and marks the cross-account seam for completion in Step 4 / Phase 2.
type Actuator struct {
	client substrateClient
	cfg    ActuatorConfig
}

// NewActuator constructs an Actuator. client must be a *Client built for the
// correct execution account (cross-account role assumption is the caller's
// responsibility until the TODO above is resolved).
func NewActuator(client *Client, cfg ActuatorConfig) *Actuator {
	return &Actuator{client: client, cfg: cfg}
}

// Launch creates a new entity. Exactly one entity per call.
// Writes the config tags ASBX owns at launch: cluster, entity, generation,
// cohort, and S3 location of the bootstrap script-set (B5).
func (a *Actuator) Launch(ctx context.Context, intent cohort.EntityIntent) (cohort.Observation, error) {
	launchTags := a.baseTags(intent)
	if a.cfg.DefaultBootstrapS3 != "" {
		launchTags[tagBootstrapS3] = a.cfg.DefaultBootstrapS3
	}

	req := substrate.RunRequest{
		AMI:              intent.Rung.InstanceType, // NOTE: AMI lookup from partitions.yaml is caller's job
		InstanceType:     intent.Rung.InstanceType,
		AvailZone:        intent.Rung.AvailZone,
		Spot:             intent.Rung.CapacityModel == cohort.CapacitySpot,
		IdempotencyToken: intent.IdempotencyToken,
		Tags:             launchTags,
	}

	inst, err := a.client.RunInstance(ctx, req)
	if err != nil {
		return cohort.Observation{}, translateFaultErr(err, intent.ID)
	}
	return observationFromInstance(inst, intent), nil
}

// Start resumes a Stopped or Hibernated entity. May itself classify
// FaultCapacityExhausted — a warm entity is not reserved capacity (B3).
func (a *Actuator) Start(ctx context.Context, id cohort.EntityID) (cohort.Observation, error) {
	providerID, err := a.resolveProviderID(ctx, id)
	if err != nil {
		return cohort.Observation{}, err
	}
	inst, err := a.client.StartInstance(ctx, providerID)
	if err != nil {
		return cohort.Observation{}, translateFaultErr(err, id)
	}
	return cohort.Observation{
		ID:         id,
		ProviderID: inst.ProviderID,
		State:      lifecycleFromEC2State(inst.State),
		Address:    inst.PrivateAddr,
		ObservedAt: time.Now(),
	}, nil
}

// Stop transitions an entity to Stopped or Hibernated per mode.
// spawn supports hibernation natively; StopModeHibernate maps directly (B3).
func (a *Actuator) Stop(ctx context.Context, id cohort.EntityID, mode cohort.StopMode) error {
	providerID, err := a.resolveProviderID(ctx, id)
	if err != nil {
		return err
	}
	hibernate := mode == cohort.StopHibernate
	if err := a.client.StopInstance(ctx, providerID, hibernate); err != nil {
		return translateFaultErr(err, id)
	}
	return nil
}

// Terminate destroys an entity. Idempotent: already-absent entities succeed.
func (a *Actuator) Terminate(ctx context.Context, id cohort.EntityID) error {
	providerID, err := a.resolveProviderID(ctx, id)
	if err != nil {
		// Not-yet-visible after tag lookup means already absent — idempotent success.
		if isNotYetVisible(err) {
			return nil
		}
		return err
	}
	if err := a.client.TerminateInstance(ctx, providerID); err != nil {
		return translateFaultErr(err, id)
	}
	return nil
}

// Observer implements cohort.Observer over substrate.Client.
// It is HYBRID (B4, ARCHITECTURE §11):
//   - Lifecycle state (phases 1-2): DescribeByTag. Miss → StateUnknown.
//   - Readiness (phase 3): spored-written tags q0:phase/q0:ready/q0:detail.
//     If absent, readiness is not-yet-ready (not a failure — spored may not
//     have written them yet). A hibernated node reports q0:ready=false until
//     mounts re-verify, defeating the "instant slurmd check-in" false positive.
type Observer struct {
	client substrateClient
	cfg    ActuatorConfig
}

// NewObserver constructs an Observer for the same account as its paired Actuator.
func NewObserver(client *Client, cfg ActuatorConfig) *Observer {
	return &Observer{client: client, cfg: cfg}
}

// Observe returns the current infrastructure-truth state of each entity.
// A miss on any entity is StateUnknown, never StateAbsent (B4 / Step 3 discipline).
func (o *Observer) Observe(ctx context.Context, ids []cohort.EntityID) ([]cohort.Observation, error) {
	obs := make([]cohort.Observation, 0, len(ids))
	for _, id := range ids {
		ob, err := o.observeOne(ctx, id)
		if err != nil {
			return nil, err
		}
		obs = append(obs, ob)
	}
	return obs, nil
}

func (o *Observer) observeOne(ctx context.Context, id cohort.EntityID) (cohort.Observation, error) {
	filter := map[string]string{
		tagCluster: o.cfg.ClusterName,
		tagEntity:  string(id),
	}
	instances, err := o.client.DescribeByTag(ctx, filter)
	if err != nil {
		return cohort.Observation{ID: id, State: cohort.StateUnknown, ObservedAt: time.Now()}, err
	}
	if len(instances) == 0 {
		// Tag-filter Describe miss = lag, not absence (ARCHITECTURE §11).
		return cohort.Observation{ID: id, State: cohort.StateUnknown, ObservedAt: time.Now()}, nil
	}
	inst := instances[0]
	return cohort.Observation{
		ID:         id,
		ProviderID: inst.ProviderID,
		State:      lifecycleFromEC2State(inst.State),
		Rung: cohort.Rung{
			InstanceType: inst.InstanceType,
			AvailZone:    inst.AvailZone,
		},
		Address:    inst.PrivateAddr,
		ObservedAt: time.Now(),
	}, nil
}

// DescribeCluster returns every instance currently tagged for this cluster,
// regardless of generation or entity. It is the read the orphan sweeper needs:
// generation inequality ("superseded") cannot be a server-side EC2 tag filter,
// so the sweeper describes the whole cluster and filters generation in-process.
// Advisory and eventually consistent, like all Describe data.
func (o *Observer) DescribeCluster(ctx context.Context, cluster string) ([]substrate.Instance, error) {
	return o.client.DescribeByTag(ctx, map[string]string{tagCluster: cluster})
}

// ReadReadiness reads the spored-written tags off a running entity to populate
// phase-3 Readiness. Called by the reconciler after an entity reaches
// StateRunning; it is separate from Observe because the two have different
// call frequencies.
//
// Tag absence → not-yet-ready, never a hard failure (spored may be starting).
// q0:ready=false → not ready (spored is running but mounts/slurmd haven't passed).
// q0:ready=true AND q0:phase=enrolled → Readiness.OK() == true.
func (o *Observer) ReadReadiness(ctx context.Context, id cohort.EntityID) (cohort.Readiness, error) {
	filter := map[string]string{
		tagCluster: o.cfg.ClusterName,
		tagEntity:  string(id),
	}
	instances, err := o.client.DescribeByTag(ctx, filter)
	if err != nil {
		// Describe error → treat as not-yet-ready, not a failure.
		return cohort.Readiness{}, nil
	}
	if len(instances) == 0 {
		return cohort.Readiness{}, nil // lag, not failure
	}

	// Extract readiness tags from the raw instance. DescribeByTag returns
	// substrate.Instance which carries instance-level tags in the Tags field
	// (added below). For now we re-describe to read tags; Step 5 can cache.
	rawTags, err := o.readInstanceTags(ctx, instances[0].ProviderID)
	if err != nil {
		return cohort.Readiness{}, nil // advisory; don't fail the phase
	}

	ready, _ := strconv.ParseBool(rawTags[tagReady])
	phase := rawTags[tagPhase]
	detail := rawTags[tagDetail]

	// Mount health is signalled by q0:phase=enrolled (spored only advances to
	// enrolled after confirming mounts). A node can be running+idle with a dead
	// Lustre mount; this is the probe that catches it (ARCHITECTURE §7).
	mountHealthy := strings.EqualFold(phase, tags.PhaseEnrolled)

	return cohort.Readiness{
		Enrolled:     ready && mountHealthy,
		Operational: mountHealthy,
		Detail:       detail,
	}, nil
}

// readInstanceTags fetches all tags for a provider instance ID via DescribeTags.
// Returns a map of tag key → value; empty map on any error (advisory).
func (o *Observer) readInstanceTags(ctx context.Context, providerID string) (map[string]string, error) {
	return o.client.DescribeTagsByID(ctx, providerID)
}

// ---- helpers ----------------------------------------------------------------

func (a *Actuator) baseTags(intent cohort.EntityIntent) map[string]string {
	return map[string]string{
		"Name":          fmt.Sprintf("%s-%s", a.cfg.ClusterName, string(intent.ID)),
		tagCluster:      a.cfg.ClusterName,
		tagEntity:       string(intent.ID),
		tagGeneration:   string(intent.Generation),
		tagCohort:       string(intent.Cohort),
	}
}

// resolveProviderID looks up the EC2 instance ID for a named entity by tag.
// Returns an error classified as FaultRetryableConsistency when not yet visible.
func (a *Actuator) resolveProviderID(ctx context.Context, id cohort.EntityID) (string, error) {
	instances, err := a.client.DescribeByTag(ctx, map[string]string{
		tagCluster: a.cfg.ClusterName,
		tagEntity:  string(id),
	})
	if err != nil {
		return "", err
	}
	if len(instances) == 0 {
		return "", faultErr(cohort.Fault{
			Class:     cohort.FaultRetryableConsistency,
			Code:      "EntityNotYetVisible",
			Retryable: true,
			Message:   fmt.Sprintf("entity %s not visible via DescribeByTag (consistency lag)", id),
		})
	}
	return instances[0].ProviderID, nil
}

// translateFaultErr converts a *FaultError from the Client into a plain error
// the reconciler will re-classify. Passes through non-FaultErrors unchanged.
func translateFaultErr(err error, id cohort.EntityID) error {
	var fe *FaultError
	if errors.As(err, &fe) {
		return fmt.Errorf("entity %s: %w", id, err)
	}
	return err
}

func isNotYetVisible(err error) bool {
	var fe *FaultError
	if errors.As(err, &fe) {
		return fe.Fault.Code == "EntityNotYetVisible"
	}
	return false
}

func observationFromInstance(inst substrate.Instance, intent cohort.EntityIntent) cohort.Observation {
	return cohort.Observation{
		ID:         intent.ID,
		Generation: intent.Generation,
		ProviderID: inst.ProviderID,
		State:      lifecycleFromEC2State(inst.State),
		Rung:       intent.Rung,
		Address:    inst.PrivateAddr,
		ObservedAt: time.Now(),
	}
}

// lifecycleFromEC2State maps EC2 instance state names to cohort.LifecycleState.
func lifecycleFromEC2State(state string) cohort.LifecycleState {
	switch ec2types.InstanceStateName(state) {
	case ec2types.InstanceStateNamePending:
		return cohort.StateLaunching
	case ec2types.InstanceStateNameRunning:
		return cohort.StateRunning
	case ec2types.InstanceStateNameStopped, ec2types.InstanceStateNameStopping:
		return cohort.StateStopped
	case ec2types.InstanceStateNameShuttingDown, ec2types.InstanceStateNameTerminated:
		return cohort.StateFailed
	default:
		return cohort.StateUnknown
	}
}
