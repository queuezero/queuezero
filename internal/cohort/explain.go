package cohort

import (
	"fmt"
	"time"
)

// Record is the structured legibility artifact every reconciled entity
// carries. Legibility is a deep requirement, not polish: queuezero must be
// crystal clear about WHY when something fails, or fall back in a legible and
// approved manner. See docs/ARCHITECTURE.md §10.
//
// A coded form of this goes into the Slurm `scontrol` reason field so
// `sinfo -R` is meaningful; the full Record is what `q0 explain <entity>`
// renders.
type Record struct {
	Entity     EntityID
	Generation Generation
	Cohort     CohortID

	// ReachedPhase is the furthest phase the entity got to. Combined with
	// Terminal or CohortCancelled, this answers "how far did it get and why did it stop".
	ReachedPhase Phase

	// Attempts is every rung tried, in order, with the fault that ended it.
	// This is what makes "walked the chain, 1b also ICE, chain exhausted" a
	// statement queuezero can actually make.
	Attempts []Attempt

	// Terminal, if set, is the fault that ended reconciliation for this
	// entity. Nil on success and nil on cohort-cancellation (see CohortCancelled).
	Terminal *Fault

	// CohortCancelled is set when this entity was healthy and in-flight but the
	// cohort fast-failed around it. It is DISTINCT from a failure: the entity
	// did not hit ICE, did not blow a deadline — the cohort died because of
	// another member. Reading CohortCancelled as a failure (e.g. by checking
	// only Terminal!=nil) would misattribute the cause to the wrong entity.
	//
	// q0 explain on a 64-node cohort must distinguish the ONE entity that
	// caused the fast-fail from the 63 cancelled because of it.
	CohortCancelled *CohortCancelInfo

	StartedAt  time.Time
	FinishedAt time.Time
}

// CohortCancelInfo describes why a healthy entity was cancelled by a cohort fast-fail.
type CohortCancelInfo struct {
	// CulpritID is the entity whose fault made the gate unsatisfiable.
	CulpritID EntityID
	// CulpritFault is the fault the culprit hit.
	CulpritFault Fault
	// CulpritPhase is the phase the culprit was in when it failed.
	CulpritPhase Phase
	// At is when the fast-fail was triggered.
	At time.Time
	// SurvivorPhase is the phase THIS entity had reached when cancelled.
	// Distinct per survivor: one may be still launching; another already enrolled.
	SurvivorPhase Phase
}

// Attempt records one rung tried for one entity.
type Attempt struct {
	Rung    Rung
	Phase   Phase  // phase reached on this rung before the fault (or PhaseReady)
	Fault   *Fault // nil if this attempt succeeded
	At      time.Time
}

// Succeeded reports whether the entity reached PhaseReady.
func (r Record) Succeeded() bool {
	return r.Terminal == nil && r.CohortCancelled == nil && r.ReachedPhase == PhaseReady
}

// WasCohortCancelled reports whether this entity was cancelled by a cohort fast-fail.
// Programmatic check: use this to distinguish survivors from the culprit.
func (r Record) WasCohortCancelled() bool { return r.CohortCancelled != nil }

// Summary is the one-line, scontrol-reason-shaped form.
func (r Record) Summary() string {
	if r.Succeeded() {
		return fmt.Sprintf("ready (%d attempt(s))", len(r.Attempts))
	}
	if r.CohortCancelled != nil {
		cc := r.CohortCancelled
		return fmt.Sprintf("cohort-cancelled at phase=%s — culprit=%s %s/%s at phase=%s",
			cc.SurvivorPhase, cc.CulpritID, cc.CulpritFault.Class, cc.CulpritFault.Code, cc.CulpritPhase)
	}
	if r.Terminal != nil {
		return fmt.Sprintf("%s/%s at phase=%s after %d rung(s)",
			r.Terminal.Class, r.Terminal.Code, r.ReachedPhase, len(r.Attempts))
	}
	return fmt.Sprintf("incomplete at phase=%s", r.ReachedPhase)
}

// Explain renders the full multi-line trace for `q0 explain`.
func (r Record) Explain() string {
	out := fmt.Sprintf("entity %s  cohort=%s  generation=%s\n", r.Entity, r.Cohort, r.Generation)
	out += fmt.Sprintf("  outcome: %s\n", r.Summary())
	if r.CohortCancelled != nil {
		cc := r.CohortCancelled
		out += fmt.Sprintf("  cohort fast-failed at %s\n", cc.At.Format(time.RFC3339))
		out += fmt.Sprintf("  culprit: entity=%s fault=%s/%s phase=%s\n",
			cc.CulpritID, cc.CulpritFault.Class, cc.CulpritFault.Code, cc.CulpritPhase)
		out += fmt.Sprintf("  this entity was at phase=%s when cancelled\n", cc.SurvivorPhase)
		return out
	}
	for i, a := range r.Attempts {
		rung := fmt.Sprintf("%s/%s/%v", a.Rung.InstanceType, a.Rung.AvailZone, a.Rung.CapacityModel)
		if a.Fault != nil {
			out += fmt.Sprintf("  [%d] %s  reached=%s  -> %s/%s  (%s)\n",
				i+1, rung, a.Phase, a.Fault.Class, a.Fault.Code, a.At.Format(time.RFC3339))
		} else {
			out += fmt.Sprintf("  [%d] %s  reached=%s  -> ok  (%s)\n",
				i+1, rung, a.Phase, a.At.Format(time.RFC3339))
		}
	}
	return out
}
