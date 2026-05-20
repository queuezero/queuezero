// Package slurm is queuezero's Slurm/MPI DOMAIN layer. It is one of the two
// planned consumers of internal/cohort (the other being a future Globus
// server). It supplies the domain seam:
//
//   - Enroller:  "slurmd has checked in" + the mount-health probe.
//   - Assembler: MPI PMIx wire-up — publish the hostlist, exchange addresses.
//     This is the "identify the systems to each other" step. The cohort core
//     invokes it and learns only pass/fail; the topology lives entirely here.
//
// It also owns the ASBX surface: translating Slurm ResumeProgram/SuspendProgram
// invocations into cohort.Cohort intents, and translating cohort Outcomes back
// into `scontrol` state (fast-fail: mark failed nodes down/drain immediately
// so Slurm requeues, rather than letting them sit in CF until ResumeTimeout).
//
// DISCIPLINE: this package may import Slurm-facing code freely. internal/cohort
// may NOT import this package. If a cohort change is ever needed to wire this
// layer up, that is a real boundary leak — fix the core before extraction.
package slurm
