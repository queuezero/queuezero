# KICKOFF — queuezero Phase 1

Paste the block below into Claude Code from the repo root to start Phase 1.
It assumes the Phase 0 scaffold (this repo) is in place. Revision 2.

---

```
You are working on queuezero, a spend-governed multi-account cloud cluster provisioner replacing
AWS ParallelCluster. Before writing any code, read docs/ARCHITECTURE.md (the conceptual contract)
and CLAUDE.md (conventions and non-negotiables). Treat both as binding.

Phase 0 is done: package boundaries, the cohort interfaces, the AWS fault-class table skeleton,
and the CLI command tree all exist and compile. Implementations are `panic("not yet implemented")`
stubs with written CONTRACT comments.

Phase 1 goal: a working cohort core, proven by its FIRST consumer — the spawn transplant. By the
end, the same unmodified internal/cohort must drive a collective set of real EC2 instances with
correct classification, fast-fail, and a structured explanation. Slurm is NOT in Phase 1 — it is
deliberately held back so the cohort boundary is shaken out by a Slurm-free consumer first.

Build Phase 1 in this order, one reviewable PR per step. After each step run `make check` (vet +
guard-cohort + test); guard-cohort must stay green — if it ever fails you have leaked a
provider/scheduler import into internal/cohort and must fix the boundary before continuing.

STEP 1 — substrate.Limiter (internal/substrate/ratelimit.go)
  Implement the account-shared adaptive token bucket. Acquire blocks for a permit; Backoff
  shrinks the effective rate on a Throttle and recovers it gradually on sustained success. One
  Limiter is shared by all cohorts reconciling against one account. Table-driven unit tests, no
  AWS.

STEP 2 — AWS Classifier (internal/substrate/aws/classifier.go)
  Implement Classify: unwrap to smithy.APIError, look up awsFaultTable, default unmapped codes to
  FaultTerminal, detect transport-level timeout/reset/5xx as FaultAmbiguous. Add aws-sdk-go-v2 to
  go.mod here. Unit-test every class with synthetic errors. This table is the product — make it
  exhaustive.

STEP 3 — substrate.Client over EC2 (internal/substrate/client.go + aws)
  Implement the AWS Client: RunInstance/StartInstance/StopInstance/TerminateInstance/
  DescribeByTag. Every mutation carries the deterministic idempotency token (substrate.Token) and
  passes through the Limiter. On a classified FaultAmbiguous, re-issue the SAME tokened call
  rather than surfacing the ambiguity — the reconciler must never see FaultAmbiguous.
  DescribeByTag results are advisory: a miss is StateUnknown, not StateAbsent.

STEP 4 — aws.Actuator + Observer (internal/substrate/aws/actuator.go)
  Implement cohort.Actuator and cohort.Observer over substrate.Client. Every call names exactly
  one entity. Back the implementation with the spawn library where spawn already does the work
  (github.com/spore-host/spore-host/spawn) — launch, hibernation start/stop — linked as a
  library, not shelled out. The Observer is HYBRID: DescribeInstances for lifecycle state
  (phases 1-2), and entity-written readiness tags (q0:phase/q0:ready/q0:detail) for phase 3.

STEP 5 — cohort.Reconciler (internal/cohort/reconcile.go)
  Implement Reconcile to the CONTRACT comment already in the file. Per-entity phases 1-3
  concurrent; table-driven fault handling; CapacityExhausted advances the approved chain via
  advanceRung and never substitutes outside it; phase budgets enforced individually so failures
  name their phase; the cohort barrier fast-fails the whole set the instant the gate goes
  unsatisfiable; assembly runs once over a complete cohort. Every entity ends with a populated
  Record. Test the ENTIRE reconciler with fake ports — no AWS, no Slurm, no MPI. Include the
  1-cohort (serial) case and a collective cohort with an injected mid-launch capacity failure
  that must fast-fail the set.

STEP 6 — the spawn transplant (the MPI domain — cohort's first consumer)
  This is the milestone. Retire spawn's naive collective path onto cohort:
  - Replace orchestrator.scaleUp(count)'s MinCount=MaxCount RunInstances with a cohort.Cohort of
    named entities reconciled by cohort.Reconciler.
  - The opaque `fmt.Errorf("failed to run instances: %w", err)` is replaced by the classifier.
  - Implement an MPI Assembler (cohort.Assembler): the collective readiness gate moves OFF the
    box into cohort's barrier. Installing OpenMPI runs from an S3-delivered, hash-pinned bootstrap
    script (NOT inlined userdata); "don't start mpirun until all ranks are up" is now cohort's
    barrier, and the Assembler does the PMIx wire-up, publishing the converged peer manifest to S3.
  - Implement an MPI Enroller: instance running + readiness tag confirmed. No Slurm anywhere.
  - Bootstrap delivery: userdata carries only a fetch-and-exec shim pointing at an S3 script-set;
    no application logic in userdata (see ARCHITECTURE §11).
  Capture a concrete before/after: spawn's current path losing instances to an unclassified ICE,
  vs the cohort version fast-failing with a per-entity Record. That before/after IS the proof.

STEP 7 — q0 explain (cmd/q0)
  Wire `q0 explain <entity>` to render cohort.Record.Explain() from the event log. Verify the
  output names the fault class, verbatim code, phase of death, and every rung tried.

Definition of done for Phase 1: internal/cohort, UNMODIFIED, drives spawn's MPI path (Step 6) end
to end — a collective cohort that fast-fails as a unit in seconds when capacity is injected-short,
with a per-entity explanation, not a 20-minute hang. The cohort boundary is now proven against a
real Slurm-free consumer.

Phase 2 lands the Slurm domain: ASBX as the ResumeProgram/SuspendProgram Go binaries linking the
same unmodified cohort core; the spored sensor and the tag control channel; ASBB spend-rate
admission; the OpenTofu static-substrate path (q0 apply cluster); q0 preflight. When the Slurm
domain compiles against the unmodified core alongside the MPI domain, cohort has earned its
graduation to spore-host/cohort. Phase 3: q0 import/capture and the bursting mode.

Constraints, repeated because they matter: no CFN/CDK; no ASG or count-based pools; internal/
cohort stays provider/scheduler-agnostic; spore.host tools linked as libraries not shelled;
idempotency tokens on every mutation; the fault taxonomy stays table-driven; every drained entity
gets a Record; spored is a sensor, not an orchestrator. When unsure, re-read docs/ARCHITECTURE.md
rather than guessing — the design was reasoned to deliberately.
```

---

## Why this order

Bottom-up, and **spawn-first by design**. Steps 1-2 (Limiter, Classifier) are pure and testable
with no AWS — they de-risk the fault table, which the whole system's correctness rests on. Steps
3-4 add the SDK and the spawn-backed provider seam. Step 5 lands the reconciler, still tested
entirely against fakes — the proof the cohort boundary holds.

Step 6 — the spawn transplant — is the deliberate first consumer rather than queuezero's own
Slurm path, for three reasons: it has no Slurm domain to confuse the picture (pure provider-seam +
MPI-domain), it exercises a real shipping tool, and spawn's current brittleness gives a concrete
before/after baseline. queuezero's Slurm domain then lands in Phase 2 on a core already hardened
by a real consumer — and the two domains compiling against one unmodified core is what earns the
extraction to `spore-host/cohort`.

Phase 2: the Slurm domain (ASBX resume/suspend programs, spored, the tag channel), ASBB
spend-rate admission, the OpenTofu static-substrate path, `q0 preflight`. Phase 3: `q0 import` /
`q0 capture` and the bursting mode. The Globus server — the third domain consumer — is a separate
track.
