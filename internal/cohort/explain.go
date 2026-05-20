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
	// Terminal, this answers "how far did it get and why did it stop".
	ReachedPhase Phase

	// Attempts is every rung tried, in order, with the fault that ended it.
	// This is what makes "walked the chain, 1b also ICE, chain exhausted" a
	// statement queuezero can actually make.
	Attempts []Attempt

	// Terminal, if set, is the fault that ended reconciliation for this
	// entity. Nil on success.
	Terminal *Fault

	StartedAt  time.Time
	FinishedAt time.Time
}

// Attempt records one rung tried for one entity.
type Attempt struct {
	Rung    Rung
	Phase   Phase  // phase reached on this rung before the fault (or PhaseReady)
	Fault   *Fault // nil if this attempt succeeded
	At      time.Time
}

// Succeeded reports whether the entity reached PhaseReady.
func (r Record) Succeeded() bool { return r.Terminal == nil && r.ReachedPhase == PhaseReady }

// Summary is the one-line, scontrol-reason-shaped form.
func (r Record) Summary() string {
	if r.Succeeded() {
		return fmt.Sprintf("ready (%d attempt(s))", len(r.Attempts))
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
