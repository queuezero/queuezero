package cohort

import "time"

// EntityID is the stable, identity-preserving name of a single managed entity.
// In the Slurm domain this is the node name (e.g. "gpu-042"). It is NEVER a
// count, an index into an anonymous pool, or anything ASG-shaped.
type EntityID string

// Generation tags every entity with the spec revision that created it.
// Instances from a superseded partitions.yaml apply are unambiguously
// reapable; current-generation instances are protected from the suspend
// sweeper. See docs/ARCHITECTURE.md §11.
type Generation string

// EntityIntent is the desired specification for one entity. The reconciler's
// intent for a cohort is a set of these — a set of NAMED slots, never "N".
type EntityIntent struct {
	ID         EntityID
	Generation Generation
	Cohort     CohortID

	// Rung is the (instance type, AZ, capacity model) currently selected from
	// the approved fallback chain. The reconciler advances this on a
	// FaultCapacityExhausted; it never substitutes outside the chain.
	Rung Rung

	// FallbackChain is the ordered list of approved rungs from partitions.yaml.
	// The reconciler advances through this chain on FaultCapacityExhausted;
	// it NEVER substitutes a rung outside it. Empty means single-rung (no fallback).
	FallbackChain []Rung

	// IdempotencyToken is deterministic in (cluster, ID, Generation). It
	// collapses FaultAmbiguous into FaultRetryableConsistency and is the
	// authority over eventually-consistent reads.
	IdempotencyToken string
}

// Rung is one option in a capacity fallback chain. There is no "safe
// baseline" — on-demand and spot are both rungs that can fault to capacity;
// they differ only in ICE probability and price. ODCR/capacity-block rungs
// are the one kind genuinely reserved against ICE.
type Rung struct {
	InstanceType  string
	AvailZone     string
	CapacityModel CapacityModel
	AccountID     string // execution account for this rung (multi-account, §3)

	// WarmStart means resume a Stopped/Hibernated entity rather than cold-launch.
	// It is a RUNG property, not a pre-check: if warm-start ICEs, advanceRung
	// moves to the next rung in the chain exactly like any other ICE.
	WarmStart bool
}

type CapacityModel int

const (
	CapacityOnDemand CapacityModel = iota
	CapacitySpot
	CapacityReserved // ODCR / capacity block — should not ICE
)

// Observation is one entity's infrastructure-truth state as seen by an
// Observer. It is advisory: a StateUnknown is lag, and the idempotency token
// is consulted for ground truth.
type Observation struct {
	ID            EntityID
	Generation    Generation
	State         LifecycleState
	ProviderID    string // e.g. EC2 instance ID, once known
	Rung          Rung
	Address       string // private address, once Running — domain may need it
	ObservedAt    time.Time
}

// Readiness is the result of a domain Enroller probe. It is deliberately
// richer than a bool: a hibernated entity lies convincingly (checks in
// instantly with stale mounts/tickets), so the probe reports WHAT it verified.
type Readiness struct {
	Enrolled     bool
	MountHealthy bool   // a node can be running+idle with a dead Lustre mount
	Detail       string // human-readable, surfaced by q0 explain
}

func (r Readiness) OK() bool { return r.Enrolled && r.MountHealthy }
