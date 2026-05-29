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
