// Package bootstrap renders the minimal userdata fetch-and-exec shim that
// delivers queuezero's node bootstrap (ARCHITECTURE §11). Userdata is the wrong
// place for application logic: it is opaque, size-capped (~16 KB), and immutable
// for the life of the instance. So the shim carries ZERO application logic
// (non-negotiable #9) — it only fetches a hash-pinned script-set from S3,
// verifies its digest, unpacks it, and execs its entrypoint. The mutable,
// re-fetchable payload lives in S3; userdata carries only "where" and the
// expected hash.
//
// This package is pure (no AWS SDK, no I/O): it renders a string, so it is
// trivially unit-tested and reusable. The aws layer base64-encodes the result
// onto the instance.
package bootstrap

import (
	"fmt"
	"strings"
	"text/template"
)

// MaxUserDataBytes is the EC2 userdata size cap. Shim refuses to render a script
// larger than this — a shim approaching the cap means application logic has
// crept in, which violates §11.
const MaxUserDataBytes = 16384

// Params is everything the shim needs baked in at launch. The peer set and any
// evolving state are NOT here — those are re-fetched from S3 by the bootstrap
// script the shim execs, never frozen into immutable userdata.
type Params struct {
	// S3URI is the content-addressed location of the script-set tarball,
	// e.g. s3://bucket/scripts/<sha256>.tar.gz. The hash is part of the key.
	S3URI string
	// SHA256 is the expected hex digest of the tarball (the same hash that
	// appears in the key). The shim verifies the download against it and fails
	// closed on mismatch.
	SHA256 string
	// Region is the AWS region, passed to `aws s3 cp` for a correct endpoint.
	Region string
	// LogPath is where the shim tees its output. Defaults to
	// /var/log/q0-bootstrap.log when empty.
	LogPath string
	// Mounts is the shared-storage spec delivered to the node. When non-empty the
	// shim writes /etc/q0/mounts (Q0_MOUNT_SPEC + Q0_MOUNT_PATHS) and sources it
	// before exec-ing bootstrap.sh, so the operator's bootstrap.sh (and q0-spored)
	// can mount/verify. The shim itself performs NO mount (still §11/#9 — it only
	// delivers the spec to a known path).
	Mounts []Mount
}

// MountsFile is the well-known path the shim writes the mount spec to and that
// q0-spored reads as a fallback when Q0_MOUNT_PATHS is not in its env.
const MountsFile = "/etc/q0/mounts"

const defaultLogPath = "/var/log/q0-bootstrap.log"

// shimData is what the template renders against: the launch params plus the
// derived mount-spec strings (empty when no mounts, which elides the block).
type shimData struct {
	S3URI, SHA256, Region, LogPath string
	MountsFile, MountSpec, MountPaths string
}

// shimTemplate is the rendered userdata. It is deliberately tiny and contains no
// application logic: fetch, verify, unpack, exec. It is idempotent across
// reboots via a sentinel file, and fails closed on any step (set -euo pipefail +
// sha256sum -c).
var shimTemplate = template.Must(template.New("shim").Parse(`#!/usr/bin/env bash
# queuezero bootstrap shim — fetch-and-exec only, NO application logic (§11).
set -euo pipefail
exec > >(tee -a {{.LogPath}}) 2>&1

SENTINEL=/var/lib/q0/bootstrap.done
if [ -f "$SENTINEL" ]; then
  echo "q0-bootstrap: already completed; skipping"
  exit 0
fi
mkdir -p /var/lib/q0 /opt/q0/bootstrap

echo "q0-bootstrap: fetching {{.S3URI}}"
aws --region {{.Region}} s3 cp "{{.S3URI}}" /tmp/q0-bootstrap.tar.gz

echo "{{.SHA256}}  /tmp/q0-bootstrap.tar.gz" | sha256sum -c -

tar -xzf /tmp/q0-bootstrap.tar.gz -C /opt/q0/bootstrap
touch "$SENTINEL"
{{if .MountSpec}}
# Deliver the shared-storage spec for bootstrap.sh + q0-spored to consume. The
# shim does NOT mount — it only writes the known location (§11/#9).
mkdir -p /etc/q0
cat > {{.MountsFile}} <<'Q0_MOUNTS_EOF'
Q0_MOUNT_SPEC='{{.MountSpec}}'
Q0_MOUNT_PATHS='{{.MountPaths}}'
Q0_MOUNTS_EOF
. {{.MountsFile}}
{{end}}
echo "q0-bootstrap: executing entrypoint"
exec /opt/q0/bootstrap/bootstrap.sh
`))

// Shim renders the userdata shim for the given params. It errors if a required
// field is missing or the rendered script would exceed MaxUserDataBytes.
func Shim(p Params) (string, error) {
	if p.S3URI == "" {
		return "", fmt.Errorf("bootstrap: S3URI is required")
	}
	if p.SHA256 == "" {
		return "", fmt.Errorf("bootstrap: SHA256 is required")
	}
	if p.Region == "" {
		return "", fmt.Errorf("bootstrap: Region is required")
	}
	if p.LogPath == "" {
		p.LogPath = defaultLogPath
	}

	data := shimData{
		S3URI:      p.S3URI,
		SHA256:     p.SHA256,
		Region:     p.Region,
		LogPath:    p.LogPath,
		MountsFile: MountsFile,
		MountSpec:  FormatMountSpec(p.Mounts),
		MountPaths: MountPaths(p.Mounts),
	}

	var b strings.Builder
	if err := shimTemplate.Execute(&b, data); err != nil {
		return "", fmt.Errorf("bootstrap: render shim: %w", err)
	}
	out := b.String()
	if len(out) > MaxUserDataBytes {
		return "", fmt.Errorf("bootstrap: shim is %d bytes, exceeds the %d-byte userdata cap "+
			"(application logic belongs in the S3 script-set, not userdata)", len(out), MaxUserDataBytes)
	}
	return out, nil
}
