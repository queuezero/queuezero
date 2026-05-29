// Package spored is queuezero's on-node readiness reporter — the WRITER half of
// the hybrid Observer's tag control channel (ARCHITECTURE §11). It runs on every
// provisioned compute node, resolves its own identity, runs health probes
// (mount health, slurmd check-in), and writes the result into its own EC2
// readiness tags (q0:phase / q0:ready / q0:detail). The off-node Observer reads
// those tags for phase-3 enrollment.
//
// It is a SENSOR, NOT AN ORCHESTRATOR (non-negotiable #8): it reports node-truth;
// cohort (off-node) decides what to do. There is deliberately no idle-kill or
// self-termination brain here — that is the dangerous part the architecture
// keeps off-node for collective cohorts.
//
// This package is provider-agnostic and SDK-free: identity, tag-writing, and
// each health probe are small interfaces (ports), so the reporter is unit-tested
// with fakes — no IMDS, no AWS, no real filesystem. The production wiring (IMDS
// identity, substrate.Client-backed tag writer, real mount/slurmd probes) lives
// in cmd/q0-spored.
//
// It is named for the spore.host `spored` agent whose role it mirrors, but it is
// independent code: the real spored (in the spore-host monorepo) writes spawn:*
// tags and has no q0: readiness concept. Folding this logic into that agent is a
// later cross-repo task.
package spored
