# queuezero — Architecture

> **Status:** design, pre-implementation. This document is the conceptual contract for the build.
> Revision 2 — incorporates the spore.host suite (truffle/spawn/spored/lagotto) as the provider
> substrate, cohort's home and co-proof, and the runtime topology.

---

## 1. What queuezero is

queuezero is a **spend-governed, multi-account cloud cluster provisioner that ships with a
Slurm-compatible front end** — the replacement for AWS ParallelCluster (and PCS) for R1 /
academic university research computing in 2026 and beyond.

It is *not* "ParallelCluster, modernized." ParallelCluster's pain is not age — it is one
structural decision: **PC models an HPC cluster as a single CloudFormation stack.** Single-account,
brittle updates, opaque scaling, slow failure, AMI coupling — all of it falls out of that.

The brittleness has a recognizable *shape*, and it is not unique to PC. It recurs anywhere a tool
launches a set of cloud instances by **count** with **no error classification**: an
all-or-nothing launch, an opaque failure, a best-effort barrier. queuezero exists to retire that
shape — and the core it is built on retires it everywhere it appears, including inside the
spore.host tooling queuezero itself depends on (see §8, §15).

### The longer thesis

Slurm is the wrong long-term abstraction for an M/M/∞ cloud. Slurm exists to allocate a *fixed,
scarce* resource; its entire conceptual apparatus — partitions, fair-share, backfill, the queue
itself — is the mathematics of contention. The cloud's defining property is the absence of that
scarcity.

But the scarcity did not vanish — it **changed currency**. On-prem the constraint was nodes; in
the cloud the constraint is dollars per unit time. The queue did not disappear; it moved from
"waiting for a free node" to "waiting because the budget says not yet." That is a different
scheduling problem — admission control against a spend rate — and nobody's existing workflow is
built for it.

So queuezero lets people **keep the Slurm interface** — `sbatch`, partitions, muscle memory —
while the layer underneath becomes spend-rate admission control. This produces a layering with a
built-in exit ramp:

| Layer | Role | Survives past Slurm? |
|---|---|---|
| `partitions.yaml` & the Slurm front | Contention-era surface users already know | No — replaceable front |
| **ASBX** | The bridge: Slurm `ResumeProgram`/`SuspendProgram` ⇄ the cloud | It *is* the bridge |
| **ASBB** | The real control plane: spend-rate admission control | **Yes — the durable core** |
| **cohort** | The reconciliation core — converges named sets of entities | Yes — provider/domain-agnostic |
| **spore.host suite** | Fleet discovery, launch, on-node sensing, capacity watching | Yes |

ASBB is not a budget feature bolted on the side — it is the part of queuezero that outlives the
premise.

---

## 2. The substrate principle

**Infrastructure-as-code for the pets. Direct API for the cattle.** This is the founding decision.

ParallelCluster's original sin is putting the compute fleet *inside* the stack — `ComputeResources`
are CFN resources, so every fleet change is a stack update and a capacity hiccup becomes a
rollback. queuezero splits hard along this line:

**The static substrate** — VPC/subnets (or BYO), security groups, IAM roles, the controller,
shared storage, placement groups, and the partition *definitions* — changes rarely and genuinely
benefits from declarative IaC with drift detection. queuezero generates and applies **OpenTofu**
under the hood (state in S3 + DynamoDB lock), wrapped so the operator never sees HCL — they see
`cluster.yaml` / `partitions.yaml` / `stack.yaml`. **No CloudFormation. No CDK.** Reimplementing
the AWS provider's resource CRUD and drift logic in a hand-rolled reconciler is a year of work
badly spent.

**The elastic fleet** — the actual instances — **never touches IaC.** It is a cohort reconciled
at runtime (§4, §7).

This split is also where **composability** lives. Each spec file is a layer with its own content
hash and its own `q0 apply <layer>`: `partitions.yaml` references a `stack.yaml` hash; the
controller references an AMI hash. Roll the software stack without recycling the network; roll
partitions without touching identity.

The static layer is identity-light and rarely-changing, which is why declarative IaC fits it. The
elastic fleet is identity-heavy, which is why it must stay out of IaC **and** off any count-based
pool abstraction (§9). One principle, two exclusions.

Software stack delivery: pair this with **Strata-style attested squashfs layers** composed at
boot over a minimal, immutable AMI. A `stack.yaml` change becomes a layer swap, not an Image
Builder run. Bootstrap *scripts* follow the same pattern at a lighter grain — content-addressed,
hash-pinned artifacts in S3, pulled at boot, never inlined into userdata (§11).

---

## 3. Multi-account model

Multi-account is nearly free, because **Slurm's cloud-scheduling seam is already account-agnostic.**
The resume program just needs to launch instances *somewhere*; `slurmctld` does not know or care
which account they live in, as long as the network path and the resume/suspend programs work.

queuezero's model: a **management / control account** runs the queuezero control plane and
`slurmctld`; **execution accounts** — per-PI, per-project, or per-sensitivity-tier — host compute.
`partitions.yaml` maps **partition → account**; ASBX assumes the correct cross-account role at
resume time.

One logical cluster, *N* accounts of compute. This buys blast-radius isolation, per-account SCPs
and budgets, clean cost attribution, **escape from the per-account-per-region quota ceiling**, and
a natural home for compliance enclaves (a FASSE-style enclave is a partition mapped to an account
with stricter SCPs — wiring directly into the `attest` work). The `gauss-asbx` design is exactly
this shape; queuezero is its generalization.

---

## 4. The cohort core

The conceptual heart of queuezero is a piece called **cohort**.

### Definition

> A **cohort reconciler** converges named sets of identity-bearing entities against
> eventually-consistent infrastructure, where a set succeeds, fails, and fast-fails *as a unit*,
> and where set-completion is followed by a domain-defined assembly phase. The unit of
> reconciliation is the cohort. The single entity is the 1-cohort.

### Why this is the real product

The error taxonomy, the classifier, phased deadlines, fast-fail — those are table stakes. What
makes cohort worth having is that it correctly reconciles **sets of entities that must come up
together, learn about each other, and fail together** — and essentially nothing in the standard
cloud toolbox does this. The standard toolbox (ASG, managed node groups, Batch) is built on
**abstraction-by-erasure**: it works by throwing entity identity away. cohort assumes the
opposite. That assumption is the product.

### What "linked" precisely means

"Linked" decomposes into exactly two things; the core takes one and refuses the other.

- **Co-readiness** — members must be up *together*, the set fails *together*, and the set
  **fast-fails as a unit** when its readiness gate becomes unsatisfiable. Cardinality and timing,
  entangled with fast-fail. **In.**
- **Mutual identification** — members must learn *something about each other* before the set is
  useful (MPI's PMIx address exchange; Globus's collection mesh join). The core takes the
  *phase* — an assembly step that runs once, over a complete and simultaneously-live cohort,
  after the barrier. It does **not** take the *content*: topology, peer graph, address list.
  **Mechanism out, phase-slot in.**

The moment the core models topology it has stopped being a reconciler and become a workflow
orchestrator — a leakier abstraction with a worse track record. **Membership in, structure out.**

### The serial job is the proof, not the exception

A serial HPC job is the **degenerate cohort: cardinality one** — no barrier, no assembly, a
trivially satisfied gate. It is the same cohort logic with N=1 and a no-op assembler. A model
whose simple case is the general case with a parameter at its minimum is the right shape.

### The cohort lifecycle

```
  per-entity phases            cohort-scoped phases
  ─────────────────            ────────────────────
  Launch-acked ─┐
  Running ──────┤── each entity, independently
  Enrolled ─────┘
                 ▼
        ┌──────────────────┐   hold until ALL members individually Enrolled,
        │  Cohort barrier  │   OR fast-fail the whole cohort if the gate
        └──────────────────┘   becomes unsatisfiable
                 ▼
        ┌──────────────────┐   domain-supplied action runs ONCE over the
        │  Cohort assembly │   complete cohort. MPI: PMIx wire-up.
        └──────────────────┘   Globus: mesh join. Core learns only pass/fail.
                 ▼
            Cohort Ready
```

`Enrolled` is deliberately generic — "the entity has been accepted by whatever external authority
the domain cares about." queuezero supplies *slurmd checked in + mounts healthy*; a Globus domain
supplies *endpoint registered*. Same phase slot, different domain probe.

### Home and extraction discipline

cohort's eventual home is **the spore.host monorepo, as a top-level peer of the suite** —
`cohort/` alongside `truffle/`, `spawn/`, `lagotto/`, `spored/`. It is not a queuezero-private
package: spore.host's own tooling needs it as much as queuezero does (§8, §15).

But it is **built first inside queuezero at `internal/cohort`**, for thrash-room. `internal/`
makes it uncallable from outside, so its interfaces can be reworked freely until proven. The
enforced rule: **`internal/cohort` imports neither a cloud SDK nor anything scheduler-specific**
— it deals only in its `ports.go` interfaces (`Actuator`/`Observer`/`Classifier` for the
provider seam; `Enroller`/`Assembler` for the domain seam). `make guard-cohort` enforces this in
CI. This rule is what makes the eventual `git mv` into the monorepo mechanical rather than
archaeological.

The extraction trigger is **two domain implementations compiling against an unmodified core**.
That trigger is now concrete and controlled (§15): the **MPI domain** (spawn) and the **Slurm
domain** (queuezero). The Globus server is a third, later consumer — no longer the thing the
extraction waits on.

---

## 5. The error taxonomy — *the taxonomy is the product*

The hard problem is not failing fast; it is **classifying fast.** The AWS API returns answers
that are some mix of true, stale, incomplete, and lies-by-omission. The engineering problem is
deciding, in seconds and correctly, which of five things each answer means. PC barely classifies
at all — and so does spawn's current orchestrator (§8): every error is `fmt.Errorf("...: %w",
err)`, capacity and throttle and auth all indistinguishable.

Every provider error maps to exactly one class, in an **explicit table** — never ad-hoc string
matching.

| Class | Examples (AWS) | Policy |
|---|---|---|
| **RetryableConsistency** | IAM profile not yet propagated; `InvalidAMIID.NotFound` inside the window; SG/subnet not visible; Describe-miss on a fresh instance | Bounded retry, short backoff, **tight** ceiling. Lag, not failure. |
| **Throttle** | `RequestLimitExceeded`, `Rate exceeded` | Exponential backoff + jitter. The fix is slowing the whole client. |
| **CapacityExhausted** | `InsufficientInstanceCapacity` (ICE), spot unavailable | **Never retry in place.** Advance the fallback chain. Chain exhausted → fast-fail, or hand to a lagotto watch (§8). |
| **Terminal** | `UnauthorizedOperation`, quota exceeded, `InvalidParameterValue`, bad AMI past the window | Fail immediately, loud, verbatim code. |
| **Ambiguous** | timeout, connection reset, 5xx | Mutation status unknown. **Must not exist downstream** — idempotency tokens collapse it into RetryableConsistency. |

**ICE is purchase-model-independent.** Plain on-demand `RunInstances` for a constrained type ICEs
routinely. There is no safe baseline — only a chain of rungs with different ICE probabilities and
prices. ODCRs / capacity blocks are the one rung type genuinely reserved against ICE, and truffle
already manages them (§8).

---

## 6. The single substrate client

Nothing in queuezero touches the AWS SDK directly. Everything goes through one substrate client
that does exactly three things.

1. **Idempotency tokens on every mutation.** `RunInstances` carries a deterministic `ClientToken`
   derived from `(cluster, entity, generation)`. This kills the Ambiguous class — after a timeout,
   re-issue the *same* call. The token is also the **authority over eventually-consistent reads**:
   Describe is advisory; the token is ground truth. (spawn today has *no* `ClientToken` anywhere
   — see §8.)
2. **The classifier.** Applies the §5 table. Non-portable across clouds by nature (§14).
3. **An adaptive, account-shared rate limiter.** Throttling is a property of the *account*, not
   the call site. One client-side token bucket backs the whole client off on Throttle.

---

## 7. The phased reconciler

A resume operation is: declare intent (a cohort of named entities), then run a deadline-bounded
loop — observe tolerating consistency gaps, diff per-entity, correct, repeat until converged or
the budget runs out. Its unit is the **named entity**, never a count (§9).

Fast-fail precision comes from splitting the budget into **phases, each with its own deadline and
failure reason:**

| Phase | Done when | Blowing the deadline means |
|---|---|---|
| 1 — Launch-acked | `RunInstances`/`StartInstances` returns | Throttling or an API problem — **not** capacity |
| 2 — Running | `DescribeInstances` shows `running` | Capacity failure surfaces here or at phase 1 |
| 3 — Enrolled | entity checks in; readiness (incl. mount) confirmed | Bootstrap / network / storage problem |
| 4 — Cohort barrier | *all* members reach phase 3, or gate unsatisfiable | Partial-cohort failure — fast-fail the set |
| 5 — Cohort assembly | domain assembler succeeds | Wire-up / registration failure |

A node that dies in phase 1 and one that dies in phase 3 get **different reasons, and queuezero
names which.**

**Capacity fallback chains.** The chain is an ordered list of **`(instance type, AZ, capacity
model, account)` rungs** in `partitions.yaml`. ASBA/truffle may *populate* it; the operator
*approves* it by committing the file; queuezero **never substitutes outside it**. A warm-start of
a stopped/hibernated entity is an *optimization* rung — it can ICE too. The terminal branch of an
exhausted chain is either fast-fail or, for patient workloads, a lagotto watch (§8).

Phase 3 always includes a **mount-health probe** — a node can be `running` in EC2 and `idle` in
Slurm with a dead Lustre mount. In queuezero this probe is reported by **spored**, on the node
itself (§11).

---

## 8. Fleet states & the spore.host suite

A queuezero-managed instance is in one of four lifecycle states — each named entity independently,
never "the fleet, currently 38, wants 40": **Running**, **Stopped** (warm — EBS persists),
**Hibernated** (RAM frozen to EBS — mounts/processes survive), **Absent**. spawn supports
hibernation natively, so `Stopped`/`Hibernated` map straight onto it — there is no warm-pool gap.

**The spore.host suite is queuezero's provider substrate.** It is a multi-module monorepo at
`github.com/spore-host/spore-host` (each tool — e.g. `spawn` — is its own Go module); queuezero
links the relevant modules as libraries, **not** by shelling out to their CLIs. Linked-as-library
means a classified error arrives as a Go value, not parsed stderr.

| Tool | What it is | Role for queuezero / cohort |
|---|---|---|
| **truffle** | Instance discovery — spot prices across regions, quotas, capacity search, **ODCRs** | Populates fallback-chain rungs; backs `q0 preflight`; the `CapacityReserved` rung |
| **spawn** | Launch & lifecycle — single instances, sweeps, **MPI clusters**, hibernation | Fills cohort's **`Actuator`** (provider seam) and supplies the **MPI domain** (§15) |
| **spored** | On-node daemon — a **smart sensor** (§11) | Reports phase-3 readiness via a bounded set of EC2 tags |
| **lagotto** | Capacity watcher — polls scarce capacity across regions, serverless (Lambda), fires launch/notify/webhook when it appears | The **patient rung**: the legible terminal branch of an exhausted fallback chain |

### lagotto: the patient rung

cohort's `CapacityExhausted` policy is "advance the chain; chain exhausted → fast-fail." But
fast-fail is only correct for *deadline / interactive* work. For a *patient* batch job — "64
H100s, tonight or tomorrow, don't care" — hard-failing is wrong. The right terminal branch is a
**lagotto watch**: cohort fast-fails the immediate attempt *and* registers a lagotto poll for the
rung. Choosing fast-fail vs lagotto-watch is itself a spend-rate policy **ASBB owns** — urgent
dollars fail fast, patient dollars wait.

### A note on spawn's current state — and why it is the first transplant

spawn already *reaches for* the collective case (it launches MPI clusters and sweeps) but does it
with the ParallelCluster shape, in miniature. Read of the code as of v0.33:

- `orchestrator.scaleUp(count)` issues `RunInstances` with `MinCount = MaxCount = count` — a
  count-based, all-or-nothing launch. The ASG pattern, hand-rolled.
- No error classification. Every failure is `fmt.Errorf("failed to run instances: %w", err)`.
- No `ClientToken` anywhere in `spawn/pkg/` — the Ambiguous class is fully unhandled.
- MPI is a **cloud-init userdata template** (`pkg/userdata/mpi.go`): each node installs OpenMPI
  and improvises a hostfile on boot. The collective barrier is best-effort, on-box, unenforced.

This is the same brittleness queuezero exists to retire — same root cause (count abstraction,
absent taxonomy), smaller scale. So spawn is not a peer of cohort and not a thing to harvest from;
spawn's `orchestrator`/`sweep`/MPI paths are the **naive version cohort supersedes**. Porting them
onto cohort is §15.

(`spawn/pkg/slurm` is *only* an sbatch-file parser — a convenience for importing existing batch
scripts into a spawn config. spawn does not speak Slurm at runtime. Its `pkg/queue` is a
job-dependency DAG, not a Slurm queue. The Slurm runtime domain is queuezero's, never spawn's.)

---

## 9. No ASG — the named entity is the unit

**Stated non-goal:** queuezero does not use Auto Scaling Groups, managed node groups, AWS Batch,
or any count-based pool abstraction. The unit of management is the named entity.

ASG is built for the web-scale problem: fungible, anonymous, stateless instances behind a load
balancer where the only state variable is *count*. An HPC node is the categorical opposite:
**named** (`slurmd` registered as `gpu-042`), **placed** (partition, placement group, AZ),
**a participant** (rank 7 of a 64-rank job — "kill it, a replacement appears" takes down the
other 63), and **stateful** (a hibernated node *is* its restored RAM).

This is why ASBX's correctness criterion is **partial-failure**: a resume for 40 entities where
31 launch must mark *exactly these 9* down, with the real error attached. ASG cannot express
"these 9" — only "down 9." A count-based controller structurally cannot do queuezero's job. (It
is also exactly what spawn's `MinCount=MaxCount` launch cannot do — §8.)

The controller itself: `slurmctld` is the most pet-like thing in the system. It is an explicitly
named, stateful singleton with a named standby (`SlurmctldHost`); durability comes from where its
**state** lives — save-state dir on durable shared storage, accounting in RDS — not from
instance fungibility. No ASG, no LB, no fiction.

**The principle:** reach for primitives that *preserve* identity, not ones that abstract it away.

---

## 10. Legibility

Legibility is a deep requirement, not polish. queuezero must **just work, and be crystal clear
about why when it doesn't, or fall back in a legible and approved manner.**

Every entity queuezero drains carries a **structured `Record`**: error class, verbatim provider
code, the phase it died in, every fallback rung attempted. A coded form goes into the `scontrol`
reason field so `sinfo -R` is meaningful; the full trace goes behind **`q0 explain <entity>`**.

"It didn't work" is never a thing a queuezero operator says. It is always: *"hit ICE on
`p5.48xlarge` in `us-east-1a`, walked the chain, `1b` also ICE, chain exhausted at 14:32:07,
entity drained"* — or *"chain exhausted, registered a lagotto watch, will resume when capacity
appears."*

This is also the spend-rate payoff: when a job waits, queuezero says *"not Slurm priority — your
project's burn rate hit its ceiling, here is the number."*

---

## 11. Runtime topology — the resume/suspend programs, spored, and the tag channel

Slurm's contract is immovable: elastic cloud nodes require slurmctld to be configured with a
`ResumeProgram` and a `SuspendProgram`, which it forks — short-lived, per call, on the controller,
with a hostlist as argv. Those two programs **stay**, and they **are ASBX**.

**The resume/suspend programs are Go binaries that link cohort.** The resume program:
`ResumeProgram gpu-[001-064]` → parse hostlist into a `cohort.Cohort` → `Reconcile()`. cohort's
provider seam is filled by a **spawn-backed `Actuator`** and a **truffle-backed rung source**;
the domain seam is filled by queuezero's Slurm `Enroller`/`Assembler`. Suspend is the same shape
inverted, plus the generation-tagged orphan sweeper. Everything is linked as a library — no CLI
shell-outs — so a classified `CapacityExhausted` arrives as a Go value, not parsed stderr.

**spored is a smart sensor, not an autonomous agent.** It lands on every provisioned compute
node. There are two kinds of "brain" and they must not merge:

- The **reconciliation brain** — what the cohort should do, fast-fail, advance rungs — lives in
  cohort, in ASBX, **off-node**. A node never decides cohort policy.
- The **self-report brain** — lives in spored, **on-node**. spored knows what only the node can
  truly know: is the Lustre mount actually up, did bootstrap finish, is `slurmd` really live, are
  Kerberos tickets fresh after a hibernate-resume. It writes that truth into a **bounded set of
  the node's own EC2 tags** (`q0:phase`, `q0:ready`, `q0:detail`). It reports; it does not
  orchestrate.

This makes cohort's `Observer` **hybrid**: `DescribeInstances` for lifecycle state (phases 1–2 —
spored isn't running yet during early boot), and spored-written tags for readiness (phase 3).
spored-written status is advisory like all Describe data; the idempotency token still owns "did
it launch." It also defeats the "a hibernated node lies convincingly" problem: on
resume-from-hibernate spored holds `q0:ready=false` until mounts re-verify, so the reconciler is
not fooled by an instant `slurmd` check-in.

**Tags are the small-signal control channel — no SSH, no message bus.** At launch ASBX
*writes config tags* (generation, TTL backstop, "cohort owns suspend — do not idle-self-
terminate", and the S3 location of the bootstrap script-set); spored *reads* config and *writes
status*; the reconciler reads status. spawn already does config-via-tags, so half of this exists.
Tags are eventually consistent and rate-limited, so spored writes a bounded few, not a log stream
— tags carry *signals*, not *payloads*.

**Bootstrap and evolving state — the S3 payload channel.** Userdata is the wrong delivery
mechanism for anything beyond a trivial shim. It is a ~16 KB blob, base64-baked into the instance
at launch: opaque (you cannot see what ran without SSH), size-capped, **immutable for the life of
the instance**, and invisible to the control plane. spawn's `pkg/userdata/mpi.go` is the
cautionary case — the MPI setup inlined as a templated script — and it is part of why §8's
barrier is best-effort and unenforced: you cannot enforce a barrier on a script you cannot
observe.

queuezero delivers bootstrap as **content-addressed scripts in S3** — `s3://.../scripts/<sha256>`,
hash-pinned, read via an IAM-instance-profile-scoped path. A bootstrap script is then a layer with
a content hash, exactly like `stack.yaml` (§2); `partitions.yaml` pins it by hash; `q0 explain`
names the exact script hash an entity ran, so legibility (§10) extends to *what code executed on
the box*, not just which API call failed. Userdata shrinks to a minimal IAM-scoped
**fetch-and-exec shim** carrying zero application logic — the same minimal-immutable discipline as
the AMI (§2).

The decisive property, beyond size and audit, is **mutability**. Userdata is frozen at launch; an
S3 object is not. This matters because a cohort's relevant state *evolves during reconciliation*:
members come up one at a time, so the MPI peer manifest is not known when node 1 launches — it is
complete only after the barrier. Userdata baked node 1 with whatever was known then; an S3 object
the node re-fetches carries membership *as it converges*. So the channel is two-part: **tags
carry small, bounded control signals**; **S3 carries larger, evolving payloads** — bootstrap
scripts, the converging peer manifest, revised config. Both are mutable, re-fetchable, and
observable; userdata is none of those. The cohort assembly phase (§4) writes its output — the
completed peer manifest — to S3 for members to pull, and the barrier is what guarantees that
manifest is complete before assembly publishes it.

One irreducible seam: the node must still be told *where* the S3 scripts are and have an instance
profile that can read them. That minimal pointer rides in userdata (and/or a launch-time tag).
The discipline is that userdata's only job is the fetch shim — never application logic, never
evolving state.

**spored's optional self-termination brain is dialed down in cohort mode.** That brain is optional
to begin with; in cohort mode the dangerous part — idle-kill — must be off for a collective cohort
(do not kill rank 7 mid-collective). spored keeps a TTL backstop and keeps **spot-interruption
handling** (only the node can catch the 2-minute warning fast enough). cohort owns the
authoritative suspend.

---

## 12. ASBX / ASBA / ASBB — the bridge and the real scheduler

queuezero does not build a scheduler. It builds everything *around* Slurm's power-save plugin,
and **ASBX is that plugin** — concretely, the resume/suspend Go programs of §11, linking cohort.

**Completing ASBX** means: fast-fail at resume (mark capacity-failed nodes `down`/`drain` via
`scontrol` immediately so Slurm requeues, rather than letting them sit in `CF` until
`ResumeTimeout`); partial-failure correctness (§9); multi-account role plumbing (§3); EFA /
placement-group wiring; a clean `SuspendProgram` with the generation-tagged sweeper.

**ASBA / truffle** populate capacity fallback chains — propose rungs; never the final authority.
Note the overlap: `truffle spot`/`capacity`/`quotas` *is* a capacity advisor, which is ASBA's job
description. Whether ASBA wraps truffle or truffle absorbs the advisory role is an open
consolidation question.

**ASBB** is the durable core — **the actual scheduler**, wearing fair-share's clothes:
spend-rate admission control, decision input dollars/hour per project. "Done" for ASBB is not
"budget guardrails work" — it is "the spend-rate admission function is the real thing deciding
when a job runs." ASBB also owns the warm-pool size and the fast-fail-vs-lagotto-watch policy
(§8).

**The suspend side is where money leaks.** A missed `TerminateInstances` is a silent cost leak,
not a visible failure. Suspend reconciles: a sweeper diffs EC2 reality (instances tagged with this
cluster + generation) against Slurm reality and reaps orphans. Describe-by-tag-filter is itself
eventually consistent, so the reaper needs a launch-time **grace period**; the **generation tag**
makes superseded-spec instances unambiguously reapable while protecting current ones.

---

## 13. Capture & import — one backend, three modes

The migration tooling and the bursting stretch goal are the *same thing*. One **introspect → spec**
backend:

- **`q0 import parallelcluster`** — parse a PC config file; `HeadNode`→controller,
  `SlurmQueues`→`partitions.yaml`, `SharedStorage`→storage. Flag what does not map.
- **`q0 capture`** — introspect a live on-prem cluster: `scontrol show config`, `sinfo`, users
  from `sacctmgr`/LDAP, the software stack from Lmod/Spack/EasyBuild manifests.

PC import is the special case where the "live cluster" is a file. Job-level `#SBATCH` parsing has
**shared lineage with `spawn/pkg/slurm`** — that parser already exists and is tested; reuse it
rather than re-implementing.

Three run modes, one architecture with two parameters (*where `slurmctld` lives*, *what
pre-exists*): **greenfield**, **replicate** (capture on-prem, mirror in AWS), **burst**
(controller stays on-prem; deploy only ASBX + cloud partitions into the existing `slurm.conf` via
`Include`). Burst is greenfield minus the controller layer.

**`q0 preflight`** — check Service Quotas, simulate IAM, verify instance-type offerings per AZ via
`DescribeInstanceTypeOfferings`, confirm AMI/subnet/EFA compatibility — *before any mutation*.
Largely a truffle-backed command. Failing in 10 seconds instead of 20 minutes is most of the felt
difference from PC.

---

## 14. Multi-cloud posture

The single substrate client plus OpenTofu makes multi-cloud *eventually* tractable — but name
where it is **not** free. **Portable:** static-substrate generation, the cohort core, the
fleet-state model, the phased reconciler. **Not portable:** the **error taxonomy / classifier** —
the most provider-specific artifact in the system — and capacity-rung semantics. The contract is
portable; the table is per-cloud. The thing that makes queuezero *correct* is the thing that must
be rebuilt per cloud.

---

## 15. The cohort extraction discipline & spawn as co-proof

cohort is built first at `internal/cohort` and graduates to the spore.host monorepo when **two
domain implementations compile against an unmodified core** (§4). Those two are now concrete and
both are code Scott controls:

- **The MPI domain — in spawn.** spawn's `orchestrator`/`sweep`/MPI paths are retired onto
  cohort: `scaleUp(count)` becomes a cohort reconcile over named entities; the opaque
  `fmt.Errorf` becomes the classifier; the cloud-init MPI barrier becomes cohort's real barrier
  plus an MPI `Assembler` (installing OpenMPI runs from an S3-delivered bootstrap script, not
  inlined userdata — §11; the *collective readiness gate*
  moves off the box). spawn supplies the AWS provider seam *and* the MPI domain. This transplant
  is worth doing for spawn's own sake — spawn gains idempotency, classification, fast-fail,
  partial-failure — independent of queuezero.
- **The Slurm domain — in queuezero / ASBX.** `slurmd` enrollment, `scontrol` fast-fail, the
  resume/suspend programs (§11). Its MPI assembler may *delegate to* spawn's, since the wire-up
  is shared and only Slurm-registration is queuezero's.

These two are **orthogonal on the domain axis** — spawn is entirely Slurm-free; queuezero is
Slurm-heavy. If `internal/cohort` compiles unmodified against both, the domain seam is genuinely
a seam and not a Slurm-shaped hole. **That is the milestone that earns the extraction** — not
"queuezero launches a cluster," but "the same cohort core, untouched, drives spawn's MPI path and
queuezero's Slurm path." spawn is therefore not a dependency queuezero borrows from — it is
cohort's **co-proof**.

Sequencing: build cohort in `internal/cohort`; the **spawn transplant is the first consumer** (no
Slurm domain to confuse the picture, pure provider-seam-plus-MPI-domain, and spawn's current
brittleness is a measurable before/after baseline on a shipping tool); queuezero's Slurm domain
lands second, on a core already hardened by a real consumer; graduate cohort to
`spore-host/cohort`; the Globus server is a third, later domain consumer.

---

## 16. Layering summary

```
┌──────────────────────────────────────────────────────────────────────┐
│  q0 CLI            apply · import · capture · preflight · explain      │
├──────────────────────────────────────────────────────────────────────┤
│  spec/             cluster.yaml · stack.yaml · partitions.yaml ·       │
│                    users.yaml   (content-hashed, composable layers)    │
├───────────────────────────────┬──────────────────────────────────────┤
│  STATIC SUBSTRATE              │  ELASTIC FLEET                        │
│  tofu/  → OpenTofu             │  ASBX = the ResumeProgram /            │
│  VPC · IAM · controller ·      │         SuspendProgram Go binaries     │
│  storage · partition defs      │     │   (Slurm's immovable seam)       │
│  (drift-detected, declarative) │     ▼                                 │
│  NO CloudFormation / CDK       │  Slurm domain  (Enroller: slurmd +     │
│                                │     mount; Assembler: PMIx wire-up)   │
│                                │     │                                 │
│                                │     ▼                                 │
│                                │  cohort   ← the real product          │
│                                │     · named-entity state machine      │
│                                │     · cohort barrier + assembly       │
│                                │     · phased deadlines, fast-fail     │
│                                │     · NO cloud-SDK, NO scheduler imports│
│                                │     │  built at internal/cohort,      │
│                                │     │  graduates to spore-host/cohort │
│                                │     ▼  ports: Actuator/Observer/       │
│                                │            Classifier/Enroller/       │
│                                │            Assembler                  │
│                                │  substrate/  single cloud chokepoint  │
│                                │     · idempotency tokens              │
│                                │     · classifier (per-cloud)          │
│                                │     · account-shared rate limiter     │
├────────────────────────────────┴──────────────────────────────────────┤
│  spore.host suite — github.com/spore-host/spore-host                   │
│    truffle  rung discovery, ODCRs, quotas      → fallback chain         │
│    spawn    launch / hibernation / MPI domain  → cohort Actuator        │
│    spored   on-node smart sensor               → readiness via tags     │
│    lagotto  capacity watcher (serverless)      → the patient rung       │
└──────────────────────────────────────────────────────────────────────┘
        ▲ same cohort core also powers spawn's own MPI/sweep paths (§15)
```

---

## Appendix — non-goals (state these explicitly in reviews)

- **No CloudFormation, no CDK.** OpenTofu for the static substrate; direct API for the fleet.
- **No ASG / managed node groups / Batch.** Count-based pool abstractions; the named entity is
  the unit (§9). This includes retiring spawn's `MinCount=MaxCount` orchestrator path.
- **The core does not model topology.** Membership and co-readiness, yes; peer graphs and
  dependency DAGs, no (§4).
- **cohort imports no cloud SDK and no scheduler.** Provider via `Actuator`/`Classifier`, domain
  via `Enroller`/`Assembler`. `make guard-cohort` enforces it.
- **spore.host tools are linked as libraries, not shelled out to.** Classified errors must arrive
  as Go values.
- **spored does not orchestrate.** It is a sensor: it reports node-truth via tags; cohort decides.
- **No application logic in userdata.** Bootstrap is content-addressed scripts in S3, hash-pinned
  and IAM-scoped; userdata carries only a minimal fetch-and-exec shim (§11). Tags carry signals,
  S3 carries payloads, userdata carries neither.
- **Data gravity is orthogonal.** queuezero touches it only at the phase-3 mount-health probe.
- **cohort is not extracted on day one.** `internal/cohort` first; graduates to the spore.host
  monorepo when the MPI and Slurm domains both compile against an unmodified core (§15).
