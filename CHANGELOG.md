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
- **Reference node bootstrap script-set** (`scripts/bootstrap/`): the `bootstrap.sh` entrypoint the
  userdata shim execs — sources `/etc/q0/mounts`, mounts EFS shared storage, configures slurmd's
  `SlurmctldHost` (primary + standby) and starts `slurmd` + `q0-spored` — plus a `q0-spored.service`
  systemd unit and a README documenting the contract. Packable via `q0 bootstrap push`. (#7)
- **Controller named standby** (`q0 apply cluster`): when `controller.standbyHost` is set, a second
  identical slurmctld pet is provisioned (the Slurm backup `SlurmctldHost`) sharing the controller SG,
  subnet, and instance profile; its private IP is emitted as a tofu output and pinned to
  `Q0_CONTROLLER_STANDBY_HOST`. Failover is Slurm's runtime behavior over the shared state dir — never
  an ASG (§9). (#6)
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

### Changed
- **`q0-spored`** now discovers its AWS region from IMDS when `Q0_REGION` is unset (previously it
  exited). A node no longer needs the region delivered to it. (#7)

[Unreleased]: https://github.com/queuezero/queuezero/commits/main
