package spored

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/queuezero/queuezero/internal/tags"
)

// Identity is the node's view of itself: the provider instance ID to tag, plus
// the cluster/entity it belongs to (for logging and sanity checks).
type Identity struct {
	ProviderID string // e.g. EC2 instance ID — the resource Tag writes to
	Entity     string // q0:entity (the named cohort member, e.g. "gpu-042")
	Cluster    string // q0:cluster
}

// IdentitySource resolves this node's Identity. Production reads IMDS + tags
// (cmd/q0-spored); tests inject a fake.
type IdentitySource interface {
	Identify(ctx context.Context) (Identity, error)
}

// TagWriter writes readiness tags onto this node. Satisfied in production by
// substrate.Client.Tag (CreateTags); a fake in tests.
type TagWriter interface {
	Tag(ctx context.Context, providerID string, kv map[string]string) error
}

// Probe reports one aspect of node health. A nil error means healthy. What a
// probe checks is domain-defined (mount health, slurmd check-in, EFA, ...); the
// reporter only learns pass/fail and the message.
type Probe interface {
	Name() string
	Check(ctx context.Context) error
}

// Reporter resolves identity, runs probes, and writes the readiness tag set. It
// is the whole sensor: no decisions, only reporting.
type Reporter struct {
	id     IdentitySource
	tags   TagWriter
	probes []Probe
	clock  func() time.Time // nil => time.Now; for deterministic detail strings if needed
}

// NewReporter constructs a Reporter over its ports and probes.
func NewReporter(id IdentitySource, writer TagWriter, probes ...Probe) *Reporter {
	return &Reporter{id: id, tags: writer, probes: probes}
}

// ReportOnce resolves identity, runs every probe, and writes ONE readiness tag
// set reflecting the current truth:
//
//	all probes pass -> q0:phase=enrolled, q0:ready=true,  q0:detail="all probes ok"
//	any probe fails -> q0:phase=booting,  q0:ready=false, q0:detail="<probe>: <err>"
//
// ready reports whether the node is fully ready after this pass (so callers like
// Run can stop once it converges).
func (r *Reporter) ReportOnce(ctx context.Context) (ready bool, err error) {
	ident, err := r.id.Identify(ctx)
	if err != nil {
		return false, fmt.Errorf("spored: identify: %w", err)
	}

	phase, readyVal, detail := r.evaluate(ctx)

	if werr := r.tags.Tag(ctx, ident.ProviderID, map[string]string{
		tags.Phase:  phase,
		tags.Ready:  boolStr(readyVal),
		tags.Detail: detail,
	}); werr != nil {
		return readyVal, fmt.Errorf("spored: write readiness tags: %w", werr)
	}
	return readyVal, nil
}

// evaluate runs all probes and computes the (phase, ready, detail) triple. A
// node with no probes is trivially ready (the no-domain / minimal-AMI case).
func (r *Reporter) evaluate(ctx context.Context) (phase string, ready bool, detail string) {
	var failures []string
	for _, p := range r.probes {
		if err := p.Check(ctx); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", p.Name(), err))
		}
	}
	if len(failures) == 0 {
		return tags.PhaseEnrolled, true, "all probes ok"
	}
	return tags.PhaseBooting, false, strings.Join(failures, "; ")
}

// Run reports on an interval until the context is cancelled, or until the node
// has reported ready for stableCount consecutive passes (after which it backs
// off to a slower keepalive — still a sensor, never silent). It always reports
// once immediately. Returns ctx.Err() on cancellation.
func (r *Reporter) Run(ctx context.Context, interval time.Duration) error {
	const stableCount = 2
	stable := 0
	for {
		ready, err := r.ReportOnce(ctx)
		if err == nil && ready {
			stable++
		} else {
			stable = 0
		}

		wait := interval
		if stable >= stableCount {
			// Converged: slow to a keepalive so we still re-assert readiness
			// (and catch a mount that dies later) without hammering the tag API.
			wait = interval * 10
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
