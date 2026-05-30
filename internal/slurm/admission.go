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
	EstimatedCost   float64 // the budget service's estimate ($/hr for a fleet hold)
	BudgetRemaining float64
	// TransactionID is the hold the budget service placed for this fleet. It is
	// empty when the gate places no hold (e.g. a graceful-degradation allow). The
	// resume path persists it so the matching suspend can reconcile the hold
	// against actuals (the other half of the spend-rate loop).
	TransactionID string
}

// Reconciler closes a resume-time hold against actual cost at teardown. It is an
// OPTIONAL capability: Suspend uses it only when the configured Admitter also
// implements it (type assertion), so a plain Admitter is unaffected. Like the
// admission request it is fleet-shaped — actual cost is rate × runtime, computed
// from the persisted hold and the node's run duration.
type Reconciler interface {
	Reconcile(ctx context.Context, req ReconcileRequest) error
}

// ReconcileRequest closes one hold. ActualCost is the realized charge
// (rate × runtime hours); the budget service converts the hold to a charge and
// refunds the variance.
type ReconcileRequest struct {
	TransactionID string
	Account       string
	JobID         string  // a synthetic id for the fleet teardown (entity + generation)
	ActualCost    float64
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
