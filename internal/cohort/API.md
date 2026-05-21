# internal/cohort — API Surface Review

**Status:** provisional, v0.x pre-extraction. All interfaces are still
movable in coordinated multi-repo commits. v1.0 is earned by the co-proof
(§7), not declared here.

---

## 1. Exported vs internal — keep/unexport verdicts

All names below are in `package cohort`. "External consumer" means spawn's
MPI transplant or queuezero's Slurm domain, once cohort is its own module.

### KEEP EXPORTED — consumer must name it

| Name | File | Reasoning |
|---|---|---|
| `Reconciler` | reconcile.go | Entry point; spawn and ASBX both construct one |
| `NewReconciler` | reconcile.go | Constructor added this commit (see §5) |
| `Reconciler.Reconcile` | reconcile.go | The single public method consumers call |
| `Reconciler.Drain` | reconcile.go | Called by ASBX suspend-sweeper and by emergency teardown paths outside the normal reconcile loop |
| `RateLimiter` | reconcile.go | Filed for account-shared throttle; substrate passes *Limiter through this |
| `Actuator` | ports.go | Provider seam — implemented per cloud |
| `Observer` | ports.go | Provider seam — implemented per cloud |
| `Classifier` | ports.go | Provider seam — implemented per cloud |
| `Enroller` | ports.go | Domain seam — implemented per domain |
| `Assembler` | ports.go | Domain seam — implemented per domain |
| `Cohort` | cohort.go | Caller-constructed input to Reconcile |
| `NewCohort` / `NewMPICohort` | cohort.go | Constructors added this commit (see §3) |
| `CohortID` | cohort.go | Used in Cohort and in Record; consumers match cohorts by ID |
| `PhaseBudget` / `DefaultBudget` | cohort.go | Caller-set per partition |
| `Outcome` | cohort.go | Returned by Reconcile |
| `EntityID` | entity.go | The identity-preserving name — passed everywhere |
| `Generation` | entity.go | Used in EntityIntent and in Record for reap-safety |
| `EntityIntent` | entity.go | Caller-constructed; one slot in Cohort.Members |
| `Rung` / `CapacityModel` / `CapacityOnDemand/Spot/Reserved` | entity.go | Fallback-chain building block |
| `Observation` | entity.go | Returned from Actuator/Observer; passed to Assembler |
| `Readiness` | entity.go | Returned from Enroller |
| `LifecycleState` / `State*` consts | state.go | Observer and test code must pattern-match state |
| `StopMode` / `StopWarm` / `StopHibernate` | state.go | Actuator.Stop argument |
| `FaultClass` / `Fault*` consts | state.go | Classifier returns Fault; consumers match class |
| `Fault` | state.go | Returned by Classifier; stored in Record.Terminal |
| `Phase` / `Phase*` consts | state.go | Record carries ReachedPhase; Slurm domain encodes it in scontrol reason |
| `Record` | explain.go | Read-only output; `q0 explain` renders it |
| `Attempt` | explain.go | Embedded in Record.Attempts |
| `CohortCancelInfo` / `ParentCancelInfo` | explain.go | Read from Record; consumers distinguish culprit from survivor |
| `BackoffPolicy` / `DefaultBackoffPolicy` | backoff.go | Provider-agnostic; substrate constructs its own instance for throttle path |

### ALREADY UNEXPORTED — correct as-is

| Name | File | Reasoning |
|---|---|---|
| `entityTracker` and all its methods | reconcile.go | Pure reconciler-internal bookkeeping; zero reason for a consumer to touch |
| `culpritInfo` | reconcile.go | Internal coordination struct between goroutines |
| `sleep` | reconcile.go | Utility; not an API concept |
| `maxConsistencyRetries` / `pollInterval` | reconcile.go | Tuning knobs, but internal ones — tuning is via PhaseBudget and BackoffPolicy |

### OPEN QUESTION — debatable, not changed

| Name | Issue |
|---|---|
| `Reconciler.Clock` | Exported field used only in tests. Idiomatic Go test injection uses an unexported field set via a test-scoped constructor (e.g. `newReconcilerWithClock`). Leaving exported for now because it is a non-breaking removal later and the test ergonomics are fine. Revisit before v1.0. |
| `Reconciler.Drain` | Could be unexported (it is called internally and is also a seam for external teardown). Keep exported until Phase 2 suspend-sweeper is wired; reassess then. |
| `Fault.Retryable` | Convenience bool; a consumer can derive it from `Class`. Not harmful to keep, but redundant. Flag for v1.0 cleanup. |

---

## 2. The five port interfaces — vocabulary check

The substitution test: read each signature with "Globus" mentally swapped for
"MPI" / "Slurm." If it only makes sense for one domain, the seam leaks.

### Actuator
```go
Launch(ctx, EntityIntent) (Observation, error)
Start(ctx, EntityID) (Observation, error)
Stop(ctx, EntityID, StopMode) error
Terminate(ctx, EntityID) error
```
**PASS.** All vocabulary is domain-neutral: *launch*, *start*, *stop*,
*terminate*, *EntityID*, *StopMode* (warm/hibernate). A Globus-domain Actuator
that creates and destroys DTN VMs fits this signature without any rename. Note
that `StopMode` (warm vs hibernate) is cloud-flavoured — a non-cloud domain
would always pass `StopWarm` — but this is not a leak: the interface is callable
from any domain, even if only one mode is meaningful.

### Observer
```go
Observe(ctx, []EntityID) ([]Observation, error)
```
**PASS.** `Observation` carries only cohort vocabulary: `ID`, `State`
(LifecycleState), `ProviderID`, `Rung`, `Address`, `ObservedAt`. No cloud type
leaks through. `ProviderID` is a plain string — an EC2 instance ID or a VM ID
or a Globus endpoint ID all fit.

### Classifier
```go
Classify(err error) Fault
```
**PASS.** Takes a raw error; returns a Fault with a `FaultClass` enum and a
verbatim string code. The doc notes it is the most provider-specific artifact and
explicitly NOT portable — the interface itself is clean, but every cloud needs its
own implementation. This is the correct design.

**One flag:** `FaultAmbiguous` is documented as "must not escape `substrate.Client`."
This is a protocol, not a type constraint. The interface permits `FaultAmbiguous`
to be returned by any Classifier; only the `Reconciler` enforces it via the loud
BUG path. Consider adding a doc comment on `Classifier` explicitly: "implementors
of this interface for use with `Reconciler` MUST NOT return `FaultAmbiguous`; use
idempotency tokens to resolve ambiguity before classification reaches here."
**Action: add the doc comment (in this commit).**

### Enroller
```go
IsEnrolled(ctx, EntityID) (Readiness, error)
```
**ALMOST PASS.** `Readiness.MountHealthy` is the one vocabulary smell. "Mount" is
HPC/filesystem language — a Globus DTN domain has no concept of a "mount" and
would always return `MountHealthy: true`. The field's purpose is really "the
entity is fully operational, not just process-alive." A more neutral name would be
`Operational` or `FullyReady` — "healthy" is generic enough.
**Action: record as open question; not renamed in this commit (breaking change).**

### Assembler
```go
Assemble(ctx, []Observation) error
```
**PASS.** "Assemble" is domain-neutral: MPI PMIx wire-up, Globus mesh join, Slurm
hostfile publication — all read as assembly over a complete, simultaneously-live
set. `[]Observation` is cohort vocabulary. The Assembler receives only what it
needs and returns only pass/fail; the reconciler never inspects topology.

---

## 3. Struct fields as contract

### EntityIntent — caller-constructed

| Field | Zero-value trap? | Notes |
|---|---|---|
| `ID` | Empty string silently creates an entity with no name. **Flag.** | Validate non-empty in `NewReconciler` or document that empty ID is caller's responsibility. Open question. |
| `Generation` | Empty string is valid (e.g. bootstrap generation). Fine. | |
| `Cohort` | Must match the parent Cohort.ID. Not validated. | Low risk; mismatch surfaces in Record, not silently wrong. |
| `Rung` | Zero-value Rung (empty strings) would pass an empty InstanceType to Launch. **Flag.** | Open question: validate in Reconcile? |
| `FallbackChain` | Nil/empty = no fallback. This IS the documented contract. Fine. | |
| `IdempotencyToken` | Empty token means no idempotency. **Flag.** | Substrate generates tokens; callers should not leave this empty. Document prominently or make it package-generated. Open question. |

### Rung — caller-constructed, embedded in EntityIntent

All fields are plain strings or ints. The notable item: `AccountID` is an
execution-account ID for multi-account routing (ARCHITECTURE §3). Empty AccountID
means single-account mode. This is the correct default and should stay documented.
`WarmStart bool` has safe zero (false = cold launch). Fine.

### Cohort — caller-constructed

| Field | Zero-value trap? | Notes |
|---|---|---|
| `ID` | Empty string allowed (1-entity workloads may not need a name). Low risk. | |
| `Members` | Nil/empty = reconcile succeeds immediately with no work. Surprising but not harmful. | |
| `Budget` | Zero PhaseBudget = all timeouts fire instantly. **Critical trap.** Always use `DefaultBudget()` or explicit values. | NewMPICohort and NewCohort do NOT set a default budget; callers must provide one. Consider a `NewMPICohortWithBudget` variant. Open question. |
| `MinViable` | Zero = full membership. **Documented on field and via constructors this commit.** | This is the most important zero-value decision in the package. |

### Record — returned to caller, never constructed externally

All fields are read-only from the caller's perspective. The inspection path via
`Succeeded()`, `WasCohortCancelled()`, `WasParentCancelled()`, `Summary()`,
`Explain()` is complete (see §4). The exported struct fields are accessible for
consumers who want to walk `Attempts` or format their own rendering.

---

## 4. Error / outcome inspection

Can a consumer answer every outcome question without reaching into unexported fields?

| Question | Method | Complete? |
|---|---|---|
| Did this entity succeed? | `rec.Succeeded()` | Yes |
| Did the cohort fast-fail around it? | `rec.WasCohortCancelled()` | Yes |
| Who was the culprit and why? | `rec.CohortCancelled.CulpritID`, `.CulpritFault.Code`, `.CulpritPhase` | Yes — all exported |
| Was it a parent-context cancel? | `rec.WasParentCancelled()` | Yes |
| What phase did it reach? | `rec.ReachedPhase` (Phase, exported) | Yes |
| What was the verbatim fault? | `rec.Terminal.Code`, `.Class` | Yes |
| What rungs were tried? | `rec.Attempts` ([]Attempt, all exported fields) | Yes |
| One-line reason for scontrol? | `rec.Summary()` | Yes |
| Full trace for q0 explain? | `rec.Explain()` | Yes |

**No inspection requires reaching into unexported fields.** The surface is complete.

One mild gap: `Outcome.Ready` is a bool. A consumer who wants to know *why* it is
not ready must iterate `Records`. This is by design (each entity has its own
reason) and is not a gap.

---

## 5. Construction — decision and implementation

**Problem:** `Reconciler` was a bare exported struct. Every field is permanently
API. A new required port (e.g. a `PolicyAdvisor` for ASBB) would be a silent
zero-value trap.

**Decision: `NewReconciler` constructor added in this commit.** Signature:

```go
func NewReconciler(act Actuator, obs Observer, clf Classifier,
    enr Enroller, asm Assembler, lim RateLimiter) *Reconciler
```

`Enroller`, `Assembler`, and `Limiter` may be nil (documented semantics).
`Clock` remains an exported field for test injection; it is explicitly documented
`TEST-ONLY`. Functional-options is not implemented now — the parameter count is
manageable (6) and the option set is stable. Revisit if it grows beyond 8.

**`NewCohort` and `NewMPICohort` added in this commit** to close the `MinViable`
zero-value trap. `NewMPICohort` explicitly sets `MinViable = len(members)`, making
the all-or-nothing contract visible at the call site.

**`PhaseBudget` has no constructor yet.** `DefaultBudget()` exists. Consider a
`NewMPICohortWithBudget` convenience in a later step. Open question.

---

## 6. BackoffPolicy placement

`BackoffPolicy` belongs in `package cohort`. It is:
- Provider-agnostic: computes durations, imports nothing scheduler/cloud specific.
- Used by the reconciler internally for `RetryableConsistency` retries.
- Exported so substrate can construct a SEPARATE instance with a longer cap for
  its throttle retry path.

`substrate/aws/client.go` constructs `throttleBackoff` as a package-level
`cohort.BackoffPolicy` literal (500ms base, 60s cap, 20% jitter) — does NOT use
`DefaultBackoffPolicy()`. This is correct: the throttle path needs a longer cap
than the consistency-lag path. The separation is working as designed.

**No change needed.**

---

## 7. Module path and versioning

**Intended import path (once extracted):** `github.com/spore-host/cohort`

Rationale: cohort is not a spawn sub-tool — it supersedes spawn's collective
path and is the conceptual heart of the spore.host suite. It should sit at the
same level as `spawn`, `truffle`, `lagotto`, `spored` — a top-level peer.

**Version:** starts at `v0.x`, explicitly pre-1.0. Interfaces are still movable
in coordinated commits across `spore-host/cohort`, `spore-host/spore-host/spawn`,
and `queuezero/queuezero`.

**v1.0 is earned by the co-proof (ARCHITECTURE §15):** the same unmodified cohort
core must compile against both:
1. The MPI domain (spawn transplant — Step 6 of the Phase 1 build plan)
2. The Slurm domain (queuezero ASBX — Phase 2)

When both compile against an unmodified core AND all tests pass, the seams are
proven. That earns v1.0 — not a calendar date.

**Current home:** `internal/cohort` within `github.com/queuezero/queuezero`. The
`internal/` keyword makes the package uncallable from outside the module, keeping
the interfaces movable until the co-proof milestone.

---

## 8. The guard travels with cohort

`make guard-cohort` (scripts/guard-cohort.sh) enforces the import discipline:
`internal/cohort` must import no cloud SDK and no scheduler. Currently this
check lives in queuezero's Makefile.

**When cohort becomes its own repo, `guard-cohort` MUST move into cohort's own
CI.** It cannot depend on a consumer's build to enforce the rule that makes cohort
reusable. Concretely:

- `spore-host/cohort` CI must run `go list ./...` and assert no import from
  `github.com/aws/aws-sdk-go-v2/...`, `azure-sdk`, `cloud.google.com`, or any
  scheduler package.
- The check is not optional: it is the invariant that makes the extraction a
  `git mv` rather than an archaeology project, and it is what future domain
  consumers depend on.

---

## Open questions (not changed in this commit)

| # | Item | Risk | When to resolve |
|---|---|---|---|
| OQ-1 | `Readiness.MountHealthy` naming — HPC-specific; should be `Operational` or `FullyReady` | Low: only an Enroller implementation concern | Before v1.0 |
| OQ-2 | `EntityIntent.ID` empty string not validated | Medium: silent wrong behavior | Before Step 6 integration test |
| OQ-3 | `EntityIntent.IdempotencyToken` empty string silently disables idempotency | High: correctness bug in production if left empty | Before Step 6; consider package-generated tokens |
| OQ-4 | `Rung` zero-value (empty InstanceType) not validated | Medium: would pass garbage to Actuator | Before Step 6 |
| OQ-5 | `PhaseBudget` zero-value trap (all deadlines fire instantly) | High if a caller constructs a bare Cohort without setting Budget | Add `NewMPICohortWithBudget` convenience constructor |
| OQ-6 | `Reconciler.Clock` exported — test-only field on a production struct | Low | Before v1.0; change to unexported + test setter |
| OQ-7 | `Reconciler.Drain` — keep exported? | Low | Reassess after Phase 2 suspend-sweeper is wired |
| OQ-8 | `Fault.Retryable` redundant (derivable from Class) | Low | v1.0 cleanup |
| OQ-9 | `Classifier` interface doc should explicitly state "MUST NOT return FaultAmbiguous" | Low | Add doc comment (one-line — not in this commit for scope) |

---

*This document is the v0.x API surface review. Revisit after the co-proof
milestone (Step 6 MPI transplant + Phase 2 Slurm domain) for the v1.0 surface
decision.*
