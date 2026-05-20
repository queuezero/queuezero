package cohort

import "time"

// CohortID names a set of entities reconciled as a unit.
type CohortID string

// Cohort is a named set of identity-bearing entities that succeed, fail, and
// fast-fail together. A serial HPC job is the 1-cohort: cardinality one, a
// trivially-satisfied barrier, and a no-op Assembler. It is the SAME logic,
// not a special case — which is the evidence the model is the right shape.
type Cohort struct {
	ID      CohortID
	Members []EntityIntent

	// Budget bounds each phase with its own deadline, so a failure names the
	// phase it died in.
	Budget PhaseBudget

	// MinViable allows a cohort to be declared ready below full membership
	// (e.g. an embarrassingly-parallel set). For an MPI cohort this MUST equal
	// len(Members): the barrier is genuinely all-or-nothing. Defaults to full
	// membership when zero.
	MinViable int
}

// IsCollective reports whether this cohort has a real barrier. A 1-cohort is
// not collective; an MPI cohort is.
func (c Cohort) IsCollective() bool { return len(c.Members) > 1 }

// PhaseBudget is the deadline for each phase of reconciliation. Phase 1 is
// deliberately tight — blowing it means throttling or an API problem, never a
// capacity problem, and that distinction is load-bearing for legibility.
type PhaseBudget struct {
	LaunchAcked    time.Duration // ~10-15s
	Running        time.Duration // ~minutes; capacity faults surface here
	Enrolled       time.Duration // bootstrap + authority check-in + mount probe
	CohortBarrier  time.Duration // how long to wait for stragglers before fast-failing the set
	CohortAssembly time.Duration // domain wire-up
}

// DefaultBudget is a starting point; partitions.yaml may override per partition.
func DefaultBudget() PhaseBudget {
	return PhaseBudget{
		LaunchAcked:    15 * time.Second,
		Running:        5 * time.Minute,
		Enrolled:       3 * time.Minute,
		CohortBarrier:  90 * time.Second,
		CohortAssembly: 60 * time.Second,
	}
}

// Outcome is the result of reconciling one cohort. Whether success or failure,
// every member carries a Record (see explain.go) — "it didn't work" is never
// an acceptable answer; "ICE on p5.48xlarge in us-east-1a, chain exhausted at
// 14:32:07" is.
type Outcome struct {
	Cohort  CohortID
	Ready   bool
	Records map[EntityID]Record
}
