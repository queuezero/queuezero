package cohort

import "time"

// CohortID names a set of entities reconciled as a unit.
type CohortID string

// Cohort is a named set of identity-bearing entities that succeed, fail, and
// fast-fail together. A serial HPC job is the 1-cohort: cardinality one, a
// trivially-satisfied barrier, and a no-op Assembler. It is the SAME logic,
// not a special case — which is the evidence the model is the right shape.
//
// Construct with NewCohort or NewMPICohort rather than a struct literal when
// MinViable semantics matter; see the zero-value note on MinViable.
type Cohort struct {
	ID      CohortID
	Members []EntityIntent

	// Budget bounds each phase with its own deadline, so a failure names the
	// phase it died in.
	Budget PhaseBudget

	// MinViable is the minimum number of enrolled entities required to satisfy
	// the cohort barrier.
	//
	// ZERO-VALUE CONTRACT: MinViable==0 means "full membership required" —
	// len(Members) is used. It does NOT mean "no quorum required" or "always
	// satisfied." This is the correct default for MPI (all-or-nothing). For an
	// embarrassingly-parallel set where partial success is acceptable, set
	// MinViable explicitly. Use NewCohort or NewMPICohort to avoid the trap.
	MinViable int
}

// NewCohort constructs a Cohort with explicit MinViable. Use this for
// embarrassingly-parallel sets where partial membership is acceptable.
func NewCohort(id CohortID, members []EntityIntent, budget PhaseBudget, minViable int) Cohort {
	return Cohort{ID: id, Members: members, Budget: budget, MinViable: minViable}
}

// NewMPICohort constructs an all-or-nothing cohort (MinViable = len(members)).
// Use this for MPI and any other domain where partial membership is not viable.
func NewMPICohort(id CohortID, members []EntityIntent, budget PhaseBudget) Cohort {
	return Cohort{ID: id, Members: members, Budget: budget, MinViable: len(members)}
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
