package slurm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/queuezero/queuezero/internal/cohort"
	"github.com/queuezero/queuezero/internal/substrate"
)

// ClusterDescriber enumerates every instance currently tagged for a cluster,
// regardless of generation. The sweeper needs the whole fleet because
// "superseded generation" is an inequality EC2 tag filters can't express; the
// generation comparison happens in-process. Satisfied in production by
// *aws.Observer.DescribeCluster; a fake in tests. slurm depends only on this
// interface and the substrate.Instance type, never the AWS SDK.
type ClusterDescriber interface {
	DescribeCluster(ctx context.Context, cluster string) ([]substrate.Instance, error)
}

// SweepOptions tunes a sweep.
type SweepOptions struct {
	// Grace is the minimum age before a stale-generation instance is reaped.
	// It tolerates the eventual consistency of tag-filtered Describe and gives a
	// just-launched (but not-yet-current-tagged) instance time to settle.
	Grace time.Duration
	// DryRun reports decisions without terminating anything.
	DryRun bool
	// Now overrides time.Now for deterministic tests. Nil uses time.Now.
	Now func() time.Time
}

// SweepDecision records what the sweeper did to one instance and why —
// legibility (ARCHITECTURE §10): a sweep never says "reaped 3", it says which
// and why, and which it spared and why.
type SweepDecision struct {
	Entity     cohort.EntityID
	ProviderID string
	Generation string
	Reason     string
}

// SweepResult is the full accounting of one sweep.
type SweepResult struct {
	Reaped []SweepDecision // terminated (or, under DryRun, would-be)
	Spared []SweepDecision // left alone, with reason
}

// Sweep reaps generation-orphaned instances — those left behind by a missed
// teardown (a crashed suspend, a superseded partitions.yaml apply). A missed
// Terminate is a silent cost leak, not a visible failure (ARCHITECTURE §12).
//
// It reaps exactly the instances where ALL of: a generation tag is present, that
// generation is NOT the current spec generation, the instance is older than the
// grace period, and an entity tag is present (so it can be terminated by named
// entity, never by a bulk/provider-id path — non-negotiable #2). Everything else
// is spared with a recorded reason. Current-generation instances — the live
// nodes — are structurally never reaped.
func (b *Bridge) Sweep(ctx context.Context, opts SweepOptions) (SweepResult, error) {
	if b.Describer == nil {
		return SweepResult{}, errors.New("slurm sweep: no ClusterDescriber configured")
	}
	if b.Actuator == nil {
		return SweepResult{}, errors.New("slurm sweep: no Actuator configured")
	}
	current := string(b.Cfg.Generation)
	if current == "" {
		// Without a current generation, every instance looks superseded — a sweep
		// would reap the whole cluster. Refuse loudly.
		return SweepResult{}, errors.New("slurm sweep: current generation is empty; refusing to sweep")
	}

	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}

	instances, err := b.Describer.DescribeCluster(ctx, b.Cfg.Cluster)
	if err != nil {
		// A describe failure means we can't decide safely — do nothing rather
		// than reap blind.
		return SweepResult{}, fmt.Errorf("slurm sweep: describe cluster %q: %w", b.Cfg.Cluster, err)
	}

	var res SweepResult
	for _, inst := range instances {
		dec := SweepDecision{
			Entity:     cohort.EntityID(inst.Entity),
			ProviderID: inst.ProviderID,
			Generation: inst.Generation,
		}

		switch {
		case isTerminating(inst.State):
			dec.Reason = "already terminating (state=" + inst.State + ")"
			res.Spared = append(res.Spared, dec)
		case inst.Generation == "":
			dec.Reason = "no generation tag (not q0-managed or mid-launch)"
			res.Spared = append(res.Spared, dec)
		case inst.Generation == current:
			dec.Reason = "current generation (live node)"
			res.Spared = append(res.Spared, dec)
		case withinGrace(inst.LaunchTime, opts.Grace, now()):
			dec.Reason = fmt.Sprintf("stale generation %s but within grace (age %s <= %s)",
				inst.Generation, age(inst.LaunchTime, now()), opts.Grace)
			res.Spared = append(res.Spared, dec)
		case inst.Entity == "":
			dec.Reason = "stale generation but no entity tag — cannot terminate by name"
			res.Spared = append(res.Spared, dec)
		default:
			dec.Reason = fmt.Sprintf("stale generation %s != current %s, age %s > grace %s",
				inst.Generation, current, age(inst.LaunchTime, now()), opts.Grace)
			if !opts.DryRun {
				if err := b.Actuator.Terminate(ctx, dec.Entity); err != nil {
					// Best-effort: record the failure in the reason but keep going.
					dec.Reason += fmt.Sprintf(" (terminate failed: %v)", err)
				}
			}
			res.Reaped = append(res.Reaped, dec)
		}
	}
	return res, nil
}

// withinGrace reports whether an instance is too young to reap. A zero
// LaunchTime (provider didn't report it) is treated as within grace —
// conservative: never reap something whose age we can't establish.
func withinGrace(launch time.Time, grace time.Duration, now time.Time) bool {
	if launch.IsZero() {
		return true
	}
	return now.Sub(launch) <= grace
}

func age(launch, now time.Time) time.Duration {
	if launch.IsZero() {
		return 0
	}
	return now.Sub(launch)
}

// isTerminating reports whether an EC2 state string means the instance is
// already going away. Matched as raw strings to keep slurm free of the EC2 SDK.
func isTerminating(state string) bool {
	return state == "shutting-down" || state == "terminated"
}
