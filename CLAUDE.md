# CLAUDE.md — queuezero

Handoff context for Claude Code. Read `docs/ARCHITECTURE.md` first; it is the conceptual contract.
This file is conventions and guardrails. Revision 2.

## What queuezero is, in one paragraph

A spend-governed, multi-account cloud cluster provisioner with a Slurm-compatible front end — the
replacement for AWS ParallelCluster/PCS for R1/academic research computing. PC's defining flaw is
modelling a cluster as a single CloudFormation stack; queuezero splits the static substrate
(declarative OpenTofu, drift-detected) from the elastic fleet (direct API, runtime-reconciled).
The long thesis: Slurm is the wrong abstraction for an M/M/∞ cloud where the scarce resource is
dollars/hour, not nodes. queuezero keeps the Slurm interface users know while the layer underneath
becomes spend-rate admission control. ASBB is the part that outlives Slurm.

## Non-negotiables

1. **No CloudFormation, no CDK.** OpenTofu only, for the static substrate.
2. **No ASG, no managed node groups, no AWS Batch.** Any count-based pool abstraction is
   forbidden — the unit of management is the named entity. Every fleet call names exactly one
   entity. (This is also the spawn anti-pattern being retired — see below.)
3. **`internal/cohort` imports nothing provider- or scheduler-specific.** No `aws-sdk-go-v2`, no
   Slurm packages, no `internal/substrate`, no `internal/slurm`. It deals only in its `ports.go`
   interfaces. `make guard-cohort` enforces this; CI must run it.
4. **The error taxonomy is table-driven.** Provider errors are classified via the explicit map in
   `internal/substrate/aws/classifier.go` — never ad-hoc `strings.Contains` in the resume path.
5. **Idempotency tokens on every mutation.** Deterministic in `(cluster, entity, generation)`.
   They collapse the `Ambiguous` fault class; it must never reach the cohort reconciler.
6. **Legibility.** Every reconciled entity ends with a populated `cohort.Record`. "It didn't
   work" is never an acceptable outcome string.
7. **spore.host tools are linked as Go libraries, never shelled out to.** A classified error must
   arrive as a Go value, not parsed stderr.
8. **spored is a sensor, not an orchestrator.** It reports node-truth via a bounded set of EC2
   tags; cohort (off-node) decides. Never push reconciliation policy onto the node.
9. **No application logic in userdata.** Bootstrap is content-addressed scripts in S3 —
   hash-pinned, IAM-scoped, referenced by launch-time tag. Userdata carries only a fetch-and-exec
   shim. Userdata is immutable, opaque, and size-capped; S3 objects are mutable and re-fetchable,
   which is required for evolving state (e.g. a cohort's peer manifest as it converges). Tags
   carry signals, S3 carries payloads.

## The cohort core — the most important package

`internal/cohort` is the conceptual product. It reconciles named SETS of identity-bearing
entities — cohorts — that succeed, fail, and fast-fail as a unit. A serial job is the 1-cohort.
An MPI job is a collective cohort with an all-or-nothing barrier and an assembly phase.

The boundary, precisely: the core takes **membership and co-readiness** (the set, the collective
gate, fast-fail-as-a-unit) and the **assembly phase-slot**. It refuses **topology** (peer graphs,
who-talks-to-whom). If you find yourself adding a dependency graph or address list to `cohort`,
stop — that belongs in a domain layer.

Two seams, both interface-only in `ports.go`:
- **Provider seam** — `Actuator` / `Observer` / `Classifier`. Filled per cloud. AWS impl lives in
  `internal/substrate/aws` and is backed by the spore.host suite (below).
- **Domain seam** — `Enroller` / `Assembler`. Filled per domain. queuezero supplies the **Slurm**
  domain (`internal/slurm`); spawn supplies the **MPI** domain (§15 of ARCHITECTURE).

Write everything in `cohort` in domain-neutral vocabulary: *entity*, *cohort*, *enrolled*,
*assembly* — never *node*, *slurmd*, *drained*. Test the boundary by mentally substituting
"Globus" for "MPI"; if the code still reads correctly, the boundary holds.

`cohort` is **not** extracted into its own repo yet. Built at `internal/cohort`; graduates to the
spore.host monorepo (`cohort/`, a peer of `truffle/ spawn/ lagotto/ spored/`) when the MPI domain
and the Slurm domain both compile against an unmodified core. Do not propose extracting earlier.

## The spore.host suite — queuezero's provider substrate

Monorepo at `github.com/spore-host/spore-host` — multi-module (e.g. `spawn` has its own
`go.mod`; import path `github.com/spore-host/spore-host/spawn`). Linked as libraries.

| Tool | Role for queuezero | Notes |
|---|---|---|
| **truffle** | Rung discovery — spot prices, quotas, capacity, **ODCRs**; backs `q0 preflight` | Overlaps ASBA's advisor role — consolidation TBD |
| **spawn** | Fills cohort's `Actuator`; **also supplies the MPI domain** | Its `orchestrator`/`sweep`/MPI code is the naive count-based pattern cohort retires |
| **spored** | On-node smart sensor — writes phase-3 readiness to EC2 tags (`q0:phase`/`q0:ready`/`q0:detail`) | Self-termination brain is optional and off for collective cohorts |
| **lagotto** | Capacity watcher (serverless) — the **patient rung**: terminal branch of an exhausted fallback chain | fast-fail vs lagotto-watch is an ASBB policy |

`spawn/pkg/slurm` is only an sbatch-file *parser* (import convenience) — spawn does not speak
Slurm at runtime. That parser has shared lineage with `q0 import` job-level parsing; reuse it.

## ASBX / ASBA / ASBB

| Name | Role | Module path |
|---|---|---|
| **ASBX** | The Slurm `ResumeProgram`/`SuspendProgram` Go binaries — link cohort directly. Slurm domain (`Enroller`/`Assembler`). | _TODO: pin_ |
| **ASBA** | Proposes fallback-chain rungs; overlaps truffle. | _TODO: pin / consolidate_ |
| **ASBB** | Spend-rate admission control — the real scheduler. Owns warm-pool size + fast-fail-vs-lagotto policy. | _TODO: pin_ |

Integration intent: `internal/slurm` *is* ASBX — the resume/suspend programs parse a Slurm
hostlist into a `cohort.Cohort`, reconcile, and write the Outcome back via `scontrol`
(capacity-failed nodes marked `down`/`drain` immediately). ASBB is checked at resume time —
refuse the launch if budget is exhausted.

## Runtime topology (see ARCHITECTURE §11)

Slurm forks `ResumeProgram`/`SuspendProgram` per call on the controller — those binaries are
ASBX, and they link cohort. cohort's `Observer` is **hybrid**: `DescribeInstances` for lifecycle
state (phases 1–2), spored-written tags for readiness (phase 3). The control channel is two-part:
**tags carry small signals** (ASBX writes config at launch, spored reads config + writes status),
**S3 carries payloads** (bootstrap scripts, the converging cohort peer manifest, revised config).
No SSH, no message bus, no application logic in userdata.

## Conventions

- **Go 1.26.** Module `github.com/queuezero/queuezero`. Binary `q0`.
- **Spec files:** `cluster.yaml`, `stack.yaml`, `partitions.yaml`, `users.yaml` — each a
  composable, content-hashed layer with its own `q0 apply <layer>`.
- **Terminology:** ASBX/ASBA/ASBB (not ABSX). CLI is `q0`. Control account runs `slurmctld`;
  compute lives in execution accounts.
- **Errors:** wrap with `%w`; preserve verbatim provider codes — never paraphrase a provider
  error code.
- **Tests:** the cohort core is tested with fake `Actuator`/`Observer`/`Classifier`/`Enroller`/
  `Assembler` — no AWS, no Slurm. If a cohort test needs a real provider, the boundary has leaked.
- **The controller is a pet.** `slurmctld` is a named, stateful singleton with a named standby
  (`SlurmctldHost`). Durability is in the state dir + RDS. Never wrap it in an ASG.

## Build & check

```
make build         # bin/q0
make vet
make guard-cohort  # enforces non-negotiable #3 (go-list based; checks real imports)
make test
make check         # vet + guard + test — run before every commit
```

## Status

Phase 0: scaffold complete — package boundaries, cohort interfaces, the AWS fault-class table
skeleton, the CLI command tree. Everything compiles; the reconciler and provider calls are
`panic("not yet implemented")` stubs with written CONTRACT comments.

Phase 1: see `KICKOFF.md`. Note the build order leads with the **spawn transplant** as cohort's
first consumer (pure provider-seam + MPI-domain, no Slurm to confuse the picture), then lands the
Slurm domain on a core already hardened by a real consumer.
