package slurm

import (
	"context"

	"github.com/queuezero/queuezero/internal/cohort"
	"github.com/queuezero/queuezero/internal/mpi"
)

// Enroller implements cohort.Enroller for the Slurm domain.
//
// The domain probe for phase 3: slurmd has checked in AND the node is
// operational (mounts healthy, and — after a hibernate-resume — not lying with
// stale mounts). In queuezero this truth is reported by spored on the node into
// the q0:ready / q0:phase / q0:detail tags and surfaced by the hybrid AWS
// Observer's ReadReadiness. The Slurm Enroller therefore adds nothing on top of
// the MPI Enroller's readiness-tag probe — slurmd-check-in and mount-health are
// exactly what spored already confirms before it writes q0:phase=enrolled.
//
// It delegates to mpi.Enroller (ARCHITECTURE §15: the readiness probe is shared;
// only Slurm registration — the scontrol writeback in ResumeProgram — is
// queuezero's). The probe is satisfied in production by *aws.Observer.
type Enroller struct{ inner *mpi.Enroller }

// NewEnroller constructs a Slurm Enroller over a readiness probe (the AWS
// Observer in production; a fake in tests).
func NewEnroller(probe mpi.ReadinessProbe) *Enroller {
	return &Enroller{inner: mpi.NewEnroller(probe)}
}

// IsEnrolled reports slurmd check-in + mount health via the readiness probe.
func (e *Enroller) IsEnrolled(ctx context.Context, id cohort.EntityID) (cohort.Readiness, error) {
	return e.inner.IsEnrolled(ctx, id)
}

// Assembler implements cohort.Assembler for collective (MPI-over-Slurm) cohorts.
//
// The cohort core has already guaranteed `members` is complete and
// simultaneously live — the barrier — which is the thing Slurm never gave MPI.
// The PMIx wire-up itself is shared with the pure-MPI domain (ARCHITECTURE §15:
// "Its MPI assembler may delegate to spawn's, since the wire-up is shared and
// only Slurm-registration is queuezero's"), so this delegates to mpi.Assembler.
// The Slurm-specific registration is the scontrol node-state writeback that
// ResumeProgram does after Reconcile, not an assembly-phase concern.
//
// It is invoked ONLY for collective cohorts (the reconciler gates assembly on
// IsCollective() && Assembler != nil); serial and partial cohorts pass a nil
// Assembler.
type Assembler struct{ inner *mpi.Assembler }

// NewAssembler constructs a Slurm Assembler over a manifest publisher (S3 in
// production; a fake in tests). The publisher is the payload channel that
// carries the converged peer manifest (ARCHITECTURE §11).
func NewAssembler(pub mpi.ManifestPublisher) *Assembler {
	return &Assembler{inner: mpi.NewAssembler(pub)}
}

// Assemble drives the PMIx wire-up over the complete, live cohort.
func (a *Assembler) Assemble(ctx context.Context, members []cohort.Observation) error {
	return a.inner.Assemble(ctx, members)
}
