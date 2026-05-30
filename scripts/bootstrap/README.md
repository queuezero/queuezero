# Reference node bootstrap script-set

This directory is a **reference** queuezero node bootstrap script-set: the payload
that `q0 bootstrap push` packs, uploads content-addressed to S3, and that a
launched node fetches, verifies, and runs (ARCHITECTURE §11). Adapt it per-site —
the slurm.conf specifics and the `q0-spored` install path are yours to set.

```
q0 bootstrap push scripts/bootstrap          # tar + sha256 + upload, prints the s3:// URI
export Q0_BOOTSTRAP_S3=s3://<bucket>/scripts/<sha256>.tar.gz   # pin it for resume
```

## The runtime contract

The userdata fetch-shim (`internal/bootstrap`) runs **before** this script and has
already, by the time `bootstrap.sh` is exec'd:

1. fetched this script-set from S3 and verified its sha256 (fail-closed),
2. unpacked it to `/opt/q0/bootstrap`,
3. written and sourced **`/etc/q0/mounts`**, which exports:
   - `Q0_MOUNT_SPEC` — shared storage as `dns:path,dns:path` (EFS today),
   - `Q0_MOUNT_PATHS` — the mount points (also read by `q0-spored`),
   - `Q0_CONTROLLER_HOST` — the primary slurmctld (`SlurmctldHost`),
   - `Q0_CONTROLLER_STANDBY_HOST` — the backup slurmctld, when a standby is declared.

The shim carries **zero** application logic (§11/#9) — it only delivers config to a
known path. Mounting, slurmd config, and starting services is this script's job.

## What `bootstrap.sh` does

1. **Mount shared storage** — each `Q0_MOUNT_SPEC` entry as NFSv4 (EFS). Idempotent
   (skips already-mounted paths). FSx-Lustre mounting is queuezero **issue #4** — the
   wire format only carries EFS-shaped `dns:path` today, so non-NFS entries are noted
   and skipped until that lands.
2. **Configure slurmd** — writes the `conf-server` list (primary + standby) to
   `/etc/q0/slurmd.env` for the slurmd unit to consume via `--conf-server`. This is a
   minimal reference stanza; replace with your site's slurm.conf delivery.
3. **Start services** — `systemctl enable --now q0-spored.service` (always) and
   `slurmd.service` (when a controller is configured).

## What you customize

- **`q0-spored.service`** — the `ExecStart` binary path (`/usr/local/bin/q0-spored`)
  to wherever your AMI installs it. Region is auto-discovered from IMDS.
- **slurmd config/unit** — the real slurm.conf, the slurmd systemd unit, and how it
  consumes `/etc/q0/slurmd.env`. Shipping the slurmd/munge packages is an AMI concern.
- Anything site-specific (NFS mount options, additional mounts, monitoring agents).

Phase-3 readiness (slurmd checked in + mounts healthy) is what `q0-spored` reports
back to the off-node Observer once this script has done its job.
