#!/usr/bin/env bash
# queuezero reference node bootstrap (the script-set entrypoint, ARCHITECTURE §11).
#
# The userdata fetch-shim has already: fetched + sha256-verified this script-set
# from S3, unpacked it to /opt/q0/bootstrap, written /etc/q0/mounts, sourced it,
# and exec'd this file. This script carries the APPLICATION logic the shim must
# not (§11/#9): mount shared storage, point slurmd at the controller, start the
# node services.
#
# It is a REFERENCE adapted per-site — the slurm.conf specifics and the q0-spored
# binary path are the parts an operator customizes. It is idempotent (safe to
# re-run) and fails closed.
set -euo pipefail

log() { echo "q0-bootstrap.sh: $*"; }

# --- Inputs --------------------------------------------------------------------
# /etc/q0/mounts is the node-config file the shim wrote and sourced. Re-source it
# so this script also works when run standalone (debugging, re-runs). Tolerate
# its absence (a node with no shared storage and no controller is still valid).
MOUNTS_FILE="${Q0_MOUNTS_FILE:-/etc/q0/mounts}"
if [ -f "$MOUNTS_FILE" ]; then
  # shellcheck disable=SC1090
  . "$MOUNTS_FILE"
fi

# --- 1. Mount shared storage ---------------------------------------------------
# Q0_MOUNT_SPEC is "dns:path,dns:path" (see internal/bootstrap/mount.go). Today
# the wire format is EFS/NFS-shaped only; FSx-Lustre delivery is queuezero issue
# #4 (it needs a mount-type extension to the spec). Mount each entry as NFSv4.
mount_shared_storage() {
  local spec="${Q0_MOUNT_SPEC:-}"
  [ -n "$spec" ] || { log "no Q0_MOUNT_SPEC; skipping mounts"; return 0; }

  local IFS=','
  for entry in $spec; do
    local dns="${entry%%:*}"
    local path="${entry#*:}"
    if [ -z "$dns" ] || [ -z "$path" ] || [ "$dns" = "$entry" ]; then
      log "skipping malformed mount entry '$entry'"
      continue
    fi
    if mountpoint -q "$path"; then
      log "already mounted: $path"
      continue
    fi
    log "mounting $dns:/ at $path (nfs4)"
    mkdir -p "$path"
    # EFS over NFSv4.1. A site using FSx-Lustre adapts this once issue #4 lands.
    mount -t nfs4 -o nfsvers=4.1,rsize=1048576,wsize=1048576,hard,timeo=600,retrans=2 \
      "${dns}:/" "$path"
  done
}

# --- 2. Configure slurmd to reach the controller ------------------------------
# Q0_CONTROLLER_HOST is the primary SlurmctldHost; Q0_CONTROLLER_STANDBY_HOST,
# when present, is the backup. The exact slurm.conf layout is site-specific —
# this writes a minimal drop-in a site can replace. slurmd is typically run with
# `--conf-server` pointing at the controller(s) so config is fetched centrally.
configure_slurmd() {
  local primary="${Q0_CONTROLLER_HOST:-}"
  [ -n "$primary" ] || { log "no Q0_CONTROLLER_HOST; skipping slurmd config"; return 0; }

  local conf_servers="$primary"
  if [ -n "${Q0_CONTROLLER_STANDBY_HOST:-}" ]; then
    conf_servers="${primary},${Q0_CONTROLLER_STANDBY_HOST}"
  fi

  mkdir -p /etc/q0
  # Reference: record the conf-server list where the slurmd unit can read it.
  # An operator's slurmd.service uses: ExecStart=/usr/sbin/slurmd --conf-server $Q0_SLURMD_CONF_SERVERS
  cat > /etc/q0/slurmd.env <<EOF
Q0_SLURMD_CONF_SERVERS='${conf_servers}'
EOF
  log "slurmd conf-server(s): ${conf_servers}"
}

# --- 3. Start node services ----------------------------------------------------
start_services() {
  # q0-spored: the on-node readiness reporter (writes q0:phase/ready/detail tags).
  # Region is discovered from IMDS when Q0_REGION is unset, so no region needed here.
  systemctl enable --now q0-spored.service

  # slurmd: only when a controller was configured.
  if [ -n "${Q0_CONTROLLER_HOST:-}" ]; then
    systemctl enable --now slurmd.service
  fi
}

main() {
  log "starting (mounts_file=$MOUNTS_FILE)"
  mount_shared_storage
  configure_slurmd
  start_services
  log "complete"
}

main "$@"
