# queuezero

**A spend-governed, multi-account cloud cluster provisioner with a
Slurm-compatible front end — the replacement for AWS ParallelCluster / PCS.**

ParallelCluster's pain is not age. It is one structural decision: PC models an
HPC cluster as a single CloudFormation stack. Single-account, brittle updates,
opaque scaling, slow failure, AMI coupling — all of it falls out of that.
queuezero picks a different substrate decision and lets better properties fall
out of *that*.

```
q0 apply       apply composable, content-hashed spec layers
q0 preflight   verify quotas / IAM / capacity / compatibility before any mutation
q0 import      recast a ParallelCluster config as queuezero spec
q0 capture     introspect a live on-prem cluster and emit replicating spec
q0 explain     show the structured reconciliation trace for an entity
```

## Principles

- **IaC for the pets, direct API for the cattle.** OpenTofu for the static
  substrate (VPC, IAM, controller, storage); direct EC2 API for the elastic
  fleet. **No CloudFormation. No CDK.**
- **Multi-account is native.** A control account runs `slurmctld`; execution
  accounts host compute. `partitions.yaml` maps partition → account.
- **The named entity is the unit.** **No ASG**, no managed node groups, no
  count-based pool abstraction — those erase the identity an HPC node depends
  on.
- **Classify fast, not just fail fast.** Every provider error maps to one of
  five fault classes via an explicit table. The taxonomy is the product.
- **Legibility is a requirement.** Every drained entity carries a structured
  record: fault class, verbatim code, phase of death, every rung attempted.

## Layout

| Path | Role |
|---|---|
| `cmd/q0` | the CLI |
| `internal/cohort` | the reconciliation core — named-entity state machine, cohort barrier, phased fast-fail. **Provider- and scheduler-agnostic.** |
| `internal/substrate` | single cloud-API chokepoint: idempotency, classification, account-shared rate limiting |
| `internal/substrate/aws` | the AWS provider seam (the non-portable fault-class table) |
| `internal/spec` | `cluster.yaml` / `stack.yaml` / `partitions.yaml` / `users.yaml` |
| `internal/tofu` | OpenTofu generation + apply for the static substrate |
| `internal/capture` | the introspect→spec backend behind `import` and `capture` |
| `internal/slurm` | the Slurm/MPI domain layer + the ASBX resume/suspend bridge |

See [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) for the full design.

## Build

```
make build     # -> bin/q0
make check     # vet + cohort import guard + tests
```

## Tracking & releases

Progress is tracked entirely in GitHub — [issues](https://github.com/queuezero/queuezero/issues),
[milestones](https://github.com/queuezero/queuezero/milestones), and the project board — not in
in-repo status files. Contributions follow an issues/PRs-only workflow (no direct pushes to `main`).

Releases follow [Semantic Versioning 2.0.0](https://semver.org/spec/v2.0.0.html); user-facing
changes are recorded in [`CHANGELOG.md`](CHANGELOG.md) per
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
