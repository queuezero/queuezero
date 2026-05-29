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
		}
	}

	// TODO(phase-2b): generation-tagged orphan sweeper. A missed Terminate is a
	// silent cost leak, not a visible failure (ARCHITECTURE §12). The sweeper
	// diffs Observer.DescribeByTag{cluster, generation<current} against Slurm
	// reality and reaps orphans, with a launch-time grace period to tolerate the
	// eventual consistency of tag-filtered Describe. tagGeneration already exists
	// on launched instances to support this.

	if len(errs) > 0 {
		return fmt.Errorf("slurm suspend: %d node(s) failed: %w", len(errs), errors.Join(errs...))
	}
	return nil
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
