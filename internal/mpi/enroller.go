package mpi

import (
	"context"

	"github.com/queuezero/queuezero/internal/cohort"
)

// ReadinessProbe is the provider port the Enroller reads phase-3 readiness
// through. It is deliberately minimal: one method, no provider types. The AWS
// Observer satisfies it structurally via its ReadReadiness method
// (internal/substrate/aws.Observer.ReadReadiness) — the MPI domain never names
// the AWS package, so the boundary in package doc holds.
//
// A Globus or local-test domain supplies its own ReadinessProbe; the Enroller
// is identical regardless.
type ReadinessProbe interface {
	ReadReadiness(ctx context.Context, id cohort.EntityID) (cohort.Readiness, error)
}

// Enroller implements cohort.Enroller for MPI cohorts.
//
// The domain probe for the late per-entity phase: "the entity has been accepted
// by whatever external authority the domain cares about" (cohort/ports.go). For
// MPI that authority is operational readiness — spored has confirmed the node is
// up, mounts are healthy, and (where applicable) EFA is live — surfaced through
// the readiness tags the provider's hybrid Observer reads.
//
// It does NOT install OpenMPI or run mpirun: per ARCHITECTURE §11 the collective
// readiness gate moves OFF the box into cohort's barrier, and OpenMPI install is
// an S3-delivered bootstrap script, not inline userdata. The Enroller only
// reports whether the entity is ready to participate.
type Enroller struct {
	probe ReadinessProbe
}

// NewEnroller constructs an MPI Enroller over a ReadinessProbe (the AWS Observer
// in production; a fake in tests).
func NewEnroller(probe ReadinessProbe) *Enroller {
	return &Enroller{probe: probe}
}

// IsEnrolled reports the entity's readiness for the collective. It delegates to
// the provider readiness probe; a not-yet-ready entity returns a non-OK
// Readiness (not an error), so the reconciler keeps polling within the phase-3
// budget rather than fast-failing on transient not-readiness.
//
// A nil probe trivially enrolls (the no-domain / unit-test convenience), matching
// cohort's contract that a nil Enroller trivially enrolls every entity.
func (e *Enroller) IsEnrolled(ctx context.Context, id cohort.EntityID) (cohort.Readiness, error) {
	if e.probe == nil {
		return cohort.Readiness{Enrolled: true, Operational: true, Detail: "no readiness probe configured"}, nil
	}
	return e.probe.ReadReadiness(ctx, id)
}
