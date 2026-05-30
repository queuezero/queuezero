package slurm

import (
	"context"
	"errors"
	"fmt"

	"github.com/queuezero/queuezero/internal/cohort"
	"github.com/queuezero/queuezero/internal/spec"
)

// Suspend is the body of the Slurm SuspendProgram. It stops or terminates each
// named entity in the hostlist. Every call names exactly one entity — no
// count-based teardown (non-negotiable #2), same as launch.
//
// Warm-pool intent (a spend-rate knob owned by ASBB, surfaced in
// partitions.yaml) decides stop-vs-terminate: a partition with a hibernated or
// stopped warm pool keeps instances warm so a later resume is a fast Start; a
// partition with no warm pool terminates.
//
// It is best-effort: a stuck node is logged and skipped so it cannot block
// reaping the rest. It returns an aggregate error if ANY node failed, for the
// caller's logging — but the suspend program's exit code is not load-bearing to
// Slurm the way resume's node-state writeback is.
func (b *Bridge) Suspend(ctx context.Context, partition, hostlist string) error {
	nodes, err := b.Scontrol.ShowHostnames(ctx, hostlist)
	if err != nil {
		return fmt.Errorf("slurm suspend: expand hostlist: %w", err)
	}
	if b.Actuator == nil {
		return errors.New("slurm suspend: no Actuator configured")
	}

	part, err := b.resolvePartition(partition, firstOr(nodes, ""))
	if err != nil {
		// Partition only governs warm-pool intent; if it cannot be resolved,
		// fall back to plain Terminate rather than failing the whole suspend.
		part = spec.Partition{}
	}
	mode, terminate := suspendAction(part)

	var errs []error
	for _, node := range nodes {
		id := cohort.EntityID(node)
		var aerr error
		if terminate {
			aerr = b.Actuator.Terminate(ctx, id)
		} else {
			aerr = b.Actuator.Stop(ctx, id, mode)
		}
		if aerr != nil {
			errs = append(errs, fmt.Errorf("%s: %w", node, aerr))
			continue
		}
		// Teardown succeeded: close this node's resume-time budget hold against
		// actuals. Best-effort and never blocks teardown (a reconcile failure is a
		// budget-drift risk handled by ASBB recovery, not a suspend failure).
		b.reconcileHold(ctx, id)
	}

	// The generation-tagged orphan sweeper — the backstop for a missed Terminate
	// (a silent cost leak, ARCHITECTURE §12) — is Bridge.Sweep (sweep.go),
	// invoked separately via `q0 sweep` (cron), NOT from the per-call Suspend
	// path. Suspend stays focused on its hostlist; the cluster-wide reap is an
	// explicit maintenance pass so every suspend doesn't trigger a cluster-wide
	// DescribeCluster.

	if len(errs) > 0 {
		return fmt.Errorf("slurm suspend: %d node(s) failed: %w", len(errs), errors.Join(errs...))
	}
	return nil
}

// reconcileHold closes the resume-time hold for one torn-down node. It looks up
// the persisted hold, computes actual cost as rate × runtime (the rate-
// reservation semantics the gate held against), asks the budget service to
// reconcile, and removes the hold. Every step is best-effort: a missing hold,
// no reconcile-capable Admitter, or a service error is logged and swallowed so
// teardown is never blocked.
func (b *Bridge) reconcileHold(ctx context.Context, id cohort.EntityID) {
	if b.Holds == nil {
		return
	}
	h, err := b.Holds.Get(id)
	if err != nil {
		if !errors.Is(err, ErrNoHold) {
			fmt.Printf("slurm suspend: read hold for %s: %v\n", id, err)
		}
		return // no hold => nothing to reconcile
	}

	// Reconcile is an optional Admitter capability; without it we can still drop
	// the local hold (the service-side hold stays until ASBB recovery sweeps it).
	rec, ok := b.Admitter.(Reconciler)
	if !ok {
		if err := b.Holds.Delete(id); err != nil {
			fmt.Printf("slurm suspend: delete hold for %s: %v\n", id, err)
		}
		return
	}

	runtimeHours := nowFunc().Sub(h.StartedAt).Hours()
	if runtimeHours < 0 {
		runtimeHours = 0
	}
	actualCost := h.HourlyRate * runtimeHours

	req := ReconcileRequest{
		TransactionID: h.TransactionID,
		Account:       h.Account,
		JobID:         fmt.Sprintf("%s/%s", b.Cfg.Cluster, id),
		ActualCost:    actualCost,
	}
	if err := rec.Reconcile(ctx, req); err != nil {
		// Leave the local hold in place so a later sweep/retry can try again.
		fmt.Printf("slurm suspend: reconcile hold %s for %s: %v\n", h.TransactionID, id, err)
		return
	}
	if err := b.Holds.Delete(id); err != nil {
		fmt.Printf("slurm suspend: delete hold for %s: %v\n", id, err)
	}
}

// suspendAction maps a partition's warm-pool spec to a stop mode (or terminate).
// Hibernated takes precedence over Stopped; no warm pool => terminate.
func suspendAction(part spec.Partition) (mode cohort.StopMode, terminate bool) {
	switch {
	case part.WarmPool.Hibernated > 0:
		return cohort.StopHibernate, false
	case part.WarmPool.Stopped > 0:
		return cohort.StopWarm, false
	default:
		return cohort.StopWarm, true
	}
}

func firstOr(s []string, fallback string) string {
	if len(s) > 0 {
		return s[0]
	}
	return fallback
}
