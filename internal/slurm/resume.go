// Package slurm is queuezero's Slurm domain — ASBX. See doc.go for the boundary
// discipline. resume.go and suspend.go are the bodies of the ResumeProgram /
// SuspendProgram that slurmctld forks (ARCHITECTURE §11); they parse a hostlist
// into a cohort.Cohort, reconcile on the UNMODIFIED cohort core, and write the
// per-entity Outcome back to Slurm via scontrol.
package slurm

import (
	"context"
	"errors"
	"fmt"

	"github.com/queuezero/queuezero/internal/cohort"
	"github.com/queuezero/queuezero/internal/recordstore"
	"github.com/queuezero/queuezero/internal/spec"
)

// Config carries the runtime configuration a resume/suspend invocation needs
// beyond the hostlist argv. The cmd wrapper fills it from env/flags; tests fill
// it directly.
type Config struct {
	Cluster          string            // cluster name: tag scope + idempotency-token derivation
	Region           string            // AWS region (informational here; the Actuator owns the SDK)
	Generation       cohort.Generation // current spec generation, stamped on every entity
	Partitions       *spec.Partitions  // loaded partitions.yaml
	DefaultPartition string            // used when the invocation did not name a partition
	BootstrapS3      string            // S3 location of the hash-pinned bootstrap script-set

	// FailMode governs what an Admitter error means: FailGraceful (default —
	// allow + warn, so a budget-service outage does not block the cluster) or
	// FailStrict (refuse). Empty => graceful.
	FailMode string
}

// Bridge holds the constructed ports the resume/suspend logic drives. The cmd
// wrapper builds one with real AWS-backed ports; tests build one with fakes.
//
// Reconciler is a closure rather than a value because the Assembler varies per
// invocation: collective cohorts get a real Assembler, serial/partial cohorts
// get nil (the reconciler must not run assembly for them).
type Bridge struct {
	Reconciler func(asm cohort.Assembler) *cohort.Reconciler
	Actuator   cohort.Actuator   // used by Suspend to stop/terminate named entities
	Assembler  cohort.Assembler  // non-nil only when collective resume is supported (S3 publisher)
	Scontrol   Scontrol
	Records    recordstore.Store
	Describer  ClusterDescriber  // used by Sweep to enumerate cluster instances; nil disables sweep
	Admitter   Admitter          // spend-rate gate at resume; nil disables admission (no gate)
	Holds      HoldStore         // persists resume-time holds for suspend-time reconcile; nil disables
	Cfg        Config
}

// Resume is the body of the Slurm ResumeProgram. partition may be empty, in
// which case the partition is resolved from the node names (or Cfg.DefaultPartition).
//
// It returns a non-nil error ONLY for failures that prevented any reconcile
// (bad config, hostlist expansion failure). Per-entity launch failures are NOT
// errors here — they are written back to Slurm as node state (down/drain), which
// is how a resume program signals failure; a non-zero exit would make slurmctld
// treat the whole batch as failed.
func (b *Bridge) Resume(ctx context.Context, partition, hostlist string) error {
	nodes, err := b.Scontrol.ShowHostnames(ctx, hostlist)
	if err != nil {
		return fmt.Errorf("slurm resume: expand hostlist: %w", err)
	}
	if len(nodes) == 0 {
		return fmt.Errorf("slurm resume: hostlist %q expanded to no nodes", hostlist)
	}

	part, err := b.resolvePartition(partition, nodes[0])
	if err != nil {
		return err
	}

	chain, err := part.CohortFallbackChain()
	if err != nil {
		return fmt.Errorf("slurm resume: partition %q: %w", part.Name, err)
	}
	budget := part.CohortBudget()
	cohortID := cohort.CohortID(part.Name + ":" + hostlist)

	intents := make([]cohort.EntityIntent, 0, len(nodes))
	for _, node := range nodes {
		intent, err := cohort.NewEntityIntent(
			b.Cfg.Cluster, cohort.EntityID(node), b.Cfg.Generation, cohortID,
			chain[0], chain, "", // Rung = chain[0] so advanceRung matches by value; empty token => deterministic
		)
		if err != nil {
			return fmt.Errorf("slurm resume: build intent for %s: %w", node, err)
		}
		intents = append(intents, intent)
	}

	c, asm, err := b.buildCohort(part, cohortID, intents, budget)
	if err != nil {
		return err
	}

	// Spend-rate admission (ARCHITECTURE §1, §12): before launching anything, ask
	// whether the project's budget admits this fleet. A refusal short-circuits the
	// reconcile and marks the nodes down with a budget reason — the same legible
	// path a capacity failure takes. Absent Admitter => no gate (feature off).
	if b.Admitter != nil {
		refused, reason, res := b.checkAdmission(ctx, part, chain[0], len(nodes))
		if refused {
			b.finish(ctx, recordRefusal(intents, cohortID, reason))
			return nil
		}
		// Admitted with a hold: persist it per node so the matching suspend can
		// reconcile it against actuals. Best-effort — a persistence failure must
		// not block the launch; it only means that hold won't be auto-reconciled
		// (the orphan path / ASBB recovery is the backstop).
		b.persistHolds(nodes, part, res)
	}

	out, _ := b.Reconciler(asm).Reconcile(ctx, c)
	b.finish(ctx, out)
	return nil
}

// finish persists the Outcome (so `q0 explain <node>` works after this process
// exits) and writes node state back to Slurm. Shared by the normal reconcile
// path and the admission-refusal path.
func (b *Bridge) finish(ctx context.Context, out cohort.Outcome) {
	if b.Records != nil {
		if err := b.Records.PutOutcome(out); err != nil {
			// Non-fatal: legibility loss, not a launch failure. Surface it but
			// still do the scontrol writeback below.
			fmt.Printf("slurm resume: persist outcome: %v\n", err)
		}
	}
	// Immediate fast-fail writeback: mark failed nodes down/drain NOW rather than
	// letting them sit in CF until ResumeTimeout (ARCHITECTURE §12).
	b.writeback(ctx, out)
}

// checkAdmission consults the spend-rate gate. It returns (refused, reason,
// result) — result carries the placed hold's TransactionID and estimated rate on
// the allow path. An Admitter error is resolved by the configured fail mode:
// FailStrict refuses (fail closed); FailGraceful (the default) allows with a
// warning — a budget service outage must not block the whole cluster.
func (b *Bridge) checkAdmission(ctx context.Context, part spec.Partition, rung cohort.Rung, count int) (bool, string, AdmissionResult) {
	res, err := b.Admitter.Admit(ctx, AdmissionRequest{
		Cluster:       b.Cfg.Cluster,
		Partition:     part.Name,
		Account:       part.ExecutionAccount,
		InstanceType:  rung.InstanceType,
		CapacityModel: capacityModelString(rung.CapacityModel),
		Count:         count,
		Region:        b.Cfg.Region,
	})
	if err != nil {
		if b.Cfg.FailMode == FailStrict {
			return true, fmt.Sprintf("admission check failed (strict): %v", err), AdmissionResult{}
		}
		fmt.Printf("slurm resume: admission check failed, allowing (graceful): %v\n", err)
		return false, "", AdmissionResult{}
	}
	if !res.Allowed {
		reason := res.Reason
		if reason == "" {
			reason = "budget exhausted"
		}
		return true, reason, res
	}
	return false, "", res
}

// persistHolds records one Hold per node from an admission result so the
// matching suspend can reconcile it. It is best-effort: with no HoldStore, no
// TransactionID (e.g. a graceful-degradation allow placed no hold), or a write
// error, it simply skips — an unreconciled hold is a budget-drift risk handled
// by ASBB recovery, not a reason to fail the launch.
func (b *Bridge) persistHolds(nodes []string, part spec.Partition, res AdmissionResult) {
	if b.Holds == nil || res.TransactionID == "" {
		return
	}
	// res.EstimatedCost is the whole-fleet $/hr; attribute it per node.
	perNodeRate := 0.0
	if len(nodes) > 0 {
		perNodeRate = res.EstimatedCost / float64(len(nodes))
	}
	now := nowFunc()
	for _, node := range nodes {
		h := Hold{
			Entity:        cohort.EntityID(node),
			TransactionID: res.TransactionID,
			Account:       part.ExecutionAccount,
			Partition:     part.Name,
			HourlyRate:    perNodeRate,
			StartedAt:     now,
		}
		if err := b.Holds.Put(h); err != nil {
			fmt.Printf("slurm resume: persist hold for %s: %v\n", node, err)
		}
	}
}

// capacityModelString renders a cohort.CapacityModel for the admission request.
func capacityModelString(m cohort.CapacityModel) string {
	switch m {
	case cohort.CapacitySpot:
		return "spot"
	case cohort.CapacityReserved:
		return "reserved"
	default:
		return "ondemand"
	}
}

// resolvePartition picks the Partition for this invocation: the explicit
// partition arg, else Cfg.DefaultPartition, else prefix-match on the node name.
func (b *Bridge) resolvePartition(partition, sampleNode string) (spec.Partition, error) {
	if b.Cfg.Partitions == nil {
		return spec.Partition{}, errors.New("slurm resume: no partitions loaded")
	}
	idx := b.Cfg.Partitions.Index()
	name := partition
	if name == "" {
		name = b.Cfg.DefaultPartition
	}
	if name != "" {
		part, ok := idx[name]
		if !ok {
			return spec.Partition{}, fmt.Errorf("slurm resume: unknown partition %q", name)
		}
		return part, nil
	}
	part, ok := idx.ResolveForNode(sampleNode)
	if !ok {
		return spec.Partition{}, fmt.Errorf("slurm resume: cannot resolve partition for node %q (pass --partition)", sampleNode)
	}
	return part, nil
}

// buildCohort selects the cohort shape from the partition semantics and returns
// the cohort plus the Assembler to reconcile with (nil for serial/partial).
func (b *Bridge) buildCohort(part spec.Partition, id cohort.CohortID, intents []cohort.EntityIntent, budget cohort.PhaseBudget) (cohort.Cohort, cohort.Assembler, error) {
	switch {
	case len(intents) == 1:
		c, err := cohort.NewSerialCohort(id, intents[0], budget)
		return c, nil, err

	case part.Collective:
		// Collective resume needs a real Assembler (S3 manifest publisher) to be
		// meaningful. The S3 publisher is phase 2b; until Bridge.Assembler is
		// wired, refuse rather than launch a collective we cannot wire up.
		if b.Assembler == nil {
			return cohort.Cohort{}, nil, fmt.Errorf(
				"slurm resume: partition %q is collective but no Assembler is configured "+
					"(collective resume requires the S3 ManifestPublisher — phase 2b)", part.Name)
		}
		c, err := cohort.NewMPICohort(id, intents, budget)
		return c, b.Assembler, err

	default:
		// >1 node, non-collective: an embarrassingly-parallel set. 2a treats it
		// as all-must-succeed (MinViable = len); a partial-success knob
		// (Partition.MinNodes) is a later refinement. Partial cohorts prohibit
		// assembly, so the Assembler is nil.
		c, err := cohort.NewPartialCohort(id, intents, budget, len(intents), nil)
		return c, nil, err
	}
}

// writeback marks each non-succeeded entity's Slurm node state. Successful nodes
// are left untouched — slurmctld auto-detects slurmd check-in and moves them to
// IDLE itself; writing state=resume would only race the controller.
func (b *Bridge) writeback(ctx context.Context, out cohort.Outcome) {
	for _, rec := range out.Records {
		if rec.Succeeded() {
			continue
		}
		state := "drain"
		if rec.Terminal != nil {
			// A real failure for THIS node (capacity exhausted, terminal fault):
			// down. Survivors cancelled around someone else's failure: drain.
			state = "down"
		}
		_ = b.Scontrol.UpdateNode(ctx, string(rec.Entity), state, rec.Summary())
	}
}
