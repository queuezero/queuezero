package slurm

import (
	"context"

	"github.com/queuezero/queuezero/internal/cohort"
)

// Enroller implements cohort.Enroller for the Slurm domain.
type Enroller struct{}

// IsEnrolled reports slurmd check-in AND mount health. A node can be running
// in EC2 and idle in Slurm with a dead Lustre mount — and a hibernated node
// lies convincingly, checking in instantly with stale mounts. So the probe
// verifies both, and reports what it verified.
func (Enroller) IsEnrolled(ctx context.Context, id cohort.EntityID) (cohort.Readiness, error) {
	// TODO(phase-1): query slurmctld for node state; run/confirm a mount-health
	// probe on the node. Return Readiness with both fields populated.
	panic("slurm.Enroller.IsEnrolled: not yet implemented")
}

// Assembler implements cohort.Assembler for MPI cohorts. For a 1-cohort
// (serial job) this is a no-op; for a collective cohort it performs the PMIx
// address exchange so the ranks can find each other.
type Assembler struct{}

func (Assembler) Assemble(ctx context.Context, members []cohort.Observation) error {
	// TODO(phase-1): publish the hostlist / drive the PMIx exchange. The cohort
	// core has already guaranteed `members` is complete and simultaneously
	// live — that guarantee is the thing Slurm never gave MPI.
	panic("slurm.Assembler.Assemble: not yet implemented")
}

// ResumeProgram is the entry point Slurm invokes (the ASBX bridge). It
// translates a Slurm hostlist into a cohort.Cohort, reconciles it, and writes
// the Outcome back to scontrol — failed nodes marked down/drain IMMEDIATELY.
func ResumeProgram(ctx context.Context, hostlist string) error {
	// TODO(phase-1): parse hostlist -> []cohort.EntityIntent (rungs/budget from
	// partitions.yaml); build cohort.Cohort (Collective => MinViable = len);
	// run cohort.Reconciler.Reconcile; on failure mark nodes down via scontrol
	// with Record.Summary() as the reason.
	panic("slurm.ResumeProgram: not yet implemented")
}

// SuspendProgram is the teardown entry point. Beyond stopping the named nodes,
// queuezero runs a generation-tagged sweeper to reap orphaned instances —
// a missed Terminate is a silent cost leak, not a visible failure.
func SuspendProgram(ctx context.Context, hostlist string) error {
	// TODO(phase-1): Stop/Terminate named entities; honor warm-pool intent.
	panic("slurm.SuspendProgram: not yet implemented")
}
