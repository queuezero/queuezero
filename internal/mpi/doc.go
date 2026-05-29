// Package mpi is the MPI DOMAIN layer — cohort's first non-Slurm consumer and
// the co-proof that internal/cohort's domain seam is a real seam, not a
// Slurm-shaped hole (docs/ARCHITECTURE.md §15).
//
// It fills the domain seam (cohort.Enroller / cohort.Assembler) for collective
// MPI cohorts:
//
//   - Enroller:  the entity is running AND its readiness tag is confirmed.
//     "Running" is already proven by the reconciler's phase 2; this probe adds
//     the phase-3 readiness check (spored-written q0:ready / mount + EFA health).
//   - Assembler: the PMIx wire-up — build the peer manifest from the complete,
//     simultaneously-live cohort and publish it to S3 for members to pull. This
//     is the "identify the systems to each other" step. The cohort core has
//     already guaranteed `members` is complete and live (the barrier) — that
//     guarantee is the thing the old cloud-init shell-loop barrier never gave.
//
// THE BOUNDARY (docs/ARCHITECTURE.md §4): the peer manifest — ranks, addresses,
// the hostfile — is TOPOLOGY. cohort refuses topology; it lives HERE, in the
// domain. cohort handed this layer only the phase-slot (Assemble runs once over
// a complete cohort) and learns only pass/fail. If a peer graph or address list
// ever appears in internal/cohort, the boundary has leaked.
//
// PROVIDER-NEUTRAL: this package imports internal/cohort and stdlib ONLY — never
// internal/substrate, never aws-sdk-go-v2. It depends on the provider through
// two tiny local ports (ReadinessProbe, ManifestPublisher) that the AWS layer
// satisfies structurally. That keeps the domain testable with fakes and keeps
// the eventual git mv into spore-host mechanical.
//
// Why this is in queuezero and not spawn: spawn is a separate Go module that
// cannot import internal/cohort, and its launch path is on-node (IMDS). cohort
// graduates to the spore.host monorepo only once the MPI domain (here) and the
// Slurm domain (internal/slurm, Phase 2) both compile against an unmodified
// core. Until then this domain lives here as the first consumer (§15).
package mpi
