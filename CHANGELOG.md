# Changelog

All notable changes to queuezero are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Progress and planning are tracked in [GitHub issues, milestones, and the project
board](https://github.com/queuezero/queuezero/issues) — this file records only
shipped, user-facing changes.

## [Unreleased]

queuezero is pre-release (`0.0.0-dev`); no versioned release has been tagged yet.
The entries below accumulate toward the first tagged release.

### Added
- **cohort reconciler core** (`internal/cohort`): provider-/scheduler-/domain-agnostic
  reconciliation of named entity cohorts with all-or-nothing collective gating, fast-fail,
  and a populated `Record` per entity. Tested entirely against fakes.
- **Slurm domain (ASBX)** (`internal/slurm`): `q0-resume`/`q0-suspend` parse a Slurm hostlist
  into a cohort, reconcile, and write outcomes back via `scontrol`; collective (MPI) resume via
  an S3 peer manifest; orphan sweeper (`q0 sweep`).
- **AWS substrate** (`internal/substrate/aws`): single-entity `Actuator`/`Observer`/`Classifier`
  with idempotency tokens, a table-driven error classifier, and spored-tag readiness.
- **Spend-rate admission (ASBB)**: resume-time `/budget/admit` gate + suspend-time
  `/budget/reconcile`, with a persisted per-node hold store. `q0 sweep` now reconciles a reaped
  orphan's hold (charges rate × runtime).
- **`q0 apply cluster`** (OpenTofu static substrate): generated/BYO VPC, slurmctld controller pet,
  IAM/buckets foundation, shared storage — **EFS** and **FSx-Lustre** (with optional S3 data
  repository association). Backend bootstrap, `--dry-run`, and `--show-env` to pin the `Q0_*` env.
- **Node bootstrap**: content-addressed S3 script-sets, a fetch-verify-exec userdata shim,
  shared-storage mount-spec delivery, and `Q0_CONTROLLER_HOST` delivery to launched nodes.
- **`q0 preflight`**: read-only EC2 offering/AMI/subnet/AZ checks plus truffle-backed Service-Quota
  checks (`--no-quota` to skip).
- **`q0 bootstrap push`**, **`q0 explain`** (structured per-entity reconciliation traces).

[Unreleased]: https://github.com/queuezero/queuezero/commits/main
