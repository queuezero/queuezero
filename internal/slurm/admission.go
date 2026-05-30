package slurm

import (
	"context"
	"time"

	"github.com/queuezero/queuezero/internal/cohort"
)

// Admitter is the spend-rate admission gate consulted at resume time, BEFORE any
// instance is launched (ARCHITECTURE §1, §12: the real scarce resource is
// dollars/hour, so the real scheduler is spend-rate admission, not Slurm's
// queue). It is satisfied in production by an ASBB-backed HTTP client
// (internal/asbb); a fake in tests. The slurm domain depends only on this
// interface — it never imports the budget service.
//
// The request is FLEET-SHAPED, not job-shaped: a Slurm ResumeProgram fires with
// only a hostlist, so resume knows the partition/account, the first rung's
// instance type, and the node count — not a job's walltime. The budget service
// estimates cost from that shape.
type Admitter interface {
	Admit(ctx context.Context, req AdmissionRequest) (AdmissionResult, error)
}

// AdmissionRequest is what the resume path knows at gate time.
type AdmissionRequest struct {
	Cluster       string
	Partition     string
	Account       string // partition.ExecutionAccount
	InstanceType  string // chain[0].InstanceType — the first rung to be attempted
	CapacityModel string // ondemand | spot | reserved
	Count         int    // number of nodes being resumed
	Region        string
}

// AdmissionResult is the gate's verdict. Allowed=false means refuse the launch.
type AdmissionResult struct {
	Allowed         bool
	Reason          string  // human-readable, surfaced via q0 explain on refusal
	EstimatedCost   float64 // what the budget service estimated for this fleet
	BudgetRemaining float64
}

// Fail modes for an Admitter error (the gate could not reach a verdict).
const (
	FailGraceful = "graceful" // allow + warn (default): a budget-service outage must not block the cluster
	FailStrict   = "strict"   // refuse: fail closed on an unreachable gate
)

// recordRefusal synthesizes an Outcome in which every member is terminally
// refused for budget reasons, at PhaseLaunchAcked (nothing was launched). This
// reuses the exact fast-fail legibility path: PutOutcome + writeback mark the
// nodes down with the reason, and q0 explain shows terminal/BudgetExhausted.
func recordRefusal(intents []cohort.EntityIntent, cohortID cohort.CohortID, reason string) cohort.Outcome {
	now := time.Now()
	f := cohort.Fault{Class: cohort.FaultTerminal, Code: "BudgetExhausted", Message: reason}
	records := make(map[cohort.EntityID]cohort.Record, len(intents))
	for _, in := range intents {
		fc := f
		records[in.ID] = cohort.Record{
			Entity:       in.ID,
			Generation:   in.Generation,
			Cohort:       cohortID,
			ReachedPhase: cohort.PhaseLaunchAcked,
			Terminal:     &fc,
			StartedAt:    now,
			FinishedAt:   now,
		}
	}
	return cohort.Outcome{Cohort: cohortID, Ready: false, Records: records}
}
