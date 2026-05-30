package bootstrap

import (
	"strings"
	"testing"
)

func validParams() Params {
	return Params{
		S3URI:  "s3://gauss-q0-scripts/scripts/abc123def456.tar.gz",
		SHA256: "abc123def456",
		Region: "us-east-1",
	}
}

func TestShim_NoMounts_NoMountFile(t *testing.T) {
	out, _ := Shim(validParams())
	if strings.Contains(out, MountsFile) || strings.Contains(out, "Q0_MOUNT_SPEC") {
		t.Error("no mounts => shim should not write the mounts file")
	}
}

func TestShim_WithMounts_WritesAndSourcesMountsFile(t *testing.T) {
	p := validParams()
	p.Mounts = []Mount{
		{DNS: "fs-0.efs.us-east-1.amazonaws.com", Path: "/shared"},
		{DNS: "fs-1.efs.us-east-1.amazonaws.com", Path: "/scratch"},
	}
	out, err := Shim(p)
	if err != nil {
		t.Fatalf("Shim: %v", err)
	}
	for _, w := range []string{
		"cat > /etc/q0/mounts",
		"Q0_MOUNT_SPEC='fs-0.efs.us-east-1.amazonaws.com:/shared,fs-1.efs.us-east-1.amazonaws.com:/scratch'",
		"Q0_MOUNT_PATHS='/shared,/scratch'",
		". /etc/q0/mounts",
		"exec /opt/q0/bootstrap/bootstrap.sh", // still execs the operator entrypoint after
	} {
		if !strings.Contains(out, w) {
			t.Errorf("shim with mounts missing %q\n---\n%s", w, out)
		}
	}
	// The mounts file must be written BEFORE the exec (delivered to the operator script).
	if strings.Index(out, "/etc/q0/mounts") > strings.Index(out, "exec /opt/q0/bootstrap/bootstrap.sh") {
		t.Error("mounts file must be written before exec-ing bootstrap.sh")
	}
}

func TestShim_WithControllerHost_WritesAndSourcesNodeFile(t *testing.T) {
	p := validParams()
	p.ControllerHost = "10.0.1.42"
	out, err := Shim(p)
	if err != nil {
		t.Fatalf("Shim: %v", err)
	}
	for _, w := range []string{
		"cat > /etc/q0/mounts",
		"Q0_CONTROLLER_HOST='10.0.1.42'",
		". /etc/q0/mounts",
		"exec /opt/q0/bootstrap/bootstrap.sh",
	} {
		if !strings.Contains(out, w) {
			t.Errorf("shim with controller host missing %q\n---\n%s", w, out)
		}
	}
	// Controller host alone => no mount lines.
	if strings.Contains(out, "Q0_MOUNT_SPEC") {
		t.Error("controller host without mounts should not emit Q0_MOUNT_SPEC")
	}
	// The node-config file must be written BEFORE the exec.
	if strings.Index(out, "/etc/q0/mounts") > strings.Index(out, "exec /opt/q0/bootstrap/bootstrap.sh") {
		t.Error("node-config file must be written before exec-ing bootstrap.sh")
	}
}

func TestShim_MountsAndControllerHost_BothDelivered(t *testing.T) {
	p := validParams()
	p.Mounts = []Mount{{DNS: "fs-0.efs.us-east-1.amazonaws.com", Path: "/shared"}}
	p.ControllerHost = "10.0.1.42"
	out, err := Shim(p)
	if err != nil {
		t.Fatalf("Shim: %v", err)
	}
	for _, w := range []string{
		"Q0_MOUNT_SPEC='fs-0.efs.us-east-1.amazonaws.com:/shared'",
		"Q0_MOUNT_PATHS='/shared'",
		"Q0_CONTROLLER_HOST='10.0.1.42'",
	} {
		if !strings.Contains(out, w) {
			t.Errorf("shim missing %q\n---\n%s", w, out)
		}
	}
}

func TestShim_NoMountsNoController_NoNodeFile(t *testing.T) {
	out, _ := Shim(validParams())
	if strings.Contains(out, MountsFile) || strings.Contains(out, "Q0_CONTROLLER_HOST") {
		t.Error("neither mounts nor controller host => shim should not write the node-config file")
	}
}

func TestShim_RendersFetchVerifyExec(t *testing.T) {
	out, err := Shim(validParams())
	if err != nil {
		t.Fatalf("Shim: %v", err)
	}
	wants := []string{
		"set -euo pipefail",                                  // fail closed
		"s3://gauss-q0-scripts/scripts/abc123def456.tar.gz",  // the baked URI
		"sha256sum -c -",                                     // digest verification
		"abc123def456",                                       // expected hash
		"--region us-east-1",                                 // region for the fetch
		"bootstrap.done",                                     // idempotency sentinel
		"exec /opt/q0/bootstrap/bootstrap.sh",                // exec the entrypoint
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("rendered shim missing %q\n---\n%s", w, out)
		}
	}
}

func TestShim_DefaultLogPath(t *testing.T) {
	out, _ := Shim(validParams())
	if !strings.Contains(out, defaultLogPath) {
		t.Errorf("expected default log path %q in shim", defaultLogPath)
	}
}

func TestShim_RequiredFields(t *testing.T) {
	cases := map[string]Params{
		"no uri":    {SHA256: "h", Region: "r"},
		"no sha256": {S3URI: "s3://b/scripts/h.tar.gz", Region: "r"},
		"no region": {S3URI: "s3://b/scripts/h.tar.gz", SHA256: "h"},
	}
	for name, p := range cases {
		if _, err := Shim(p); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

func TestShim_EnforcesSizeCap(t *testing.T) {
	p := validParams()
	p.LogPath = "/" + strings.Repeat("x", MaxUserDataBytes) // blow the cap
	if _, err := Shim(p); err == nil {
		t.Error("expected size-cap error for an oversized shim")
	}
}

func TestShim_WellUnderCap(t *testing.T) {
	out, _ := Shim(validParams())
	if len(out) > 2048 {
		t.Errorf("shim is %d bytes — should be tiny (fetch-exec only), well under %d",
			len(out), MaxUserDataBytes)
	}
}
