package bootstrap

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// writeScriptSet creates a minimal script-set dir with bootstrap.sh + extras.
func writeScriptSet(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bootstrap.sh"), []byte("#!/bin/bash\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "install-spored.sh"), []byte("echo install\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestPack_ProducesContentAddressedDigest(t *testing.T) {
	dir := writeScriptSet(t)
	var buf bytes.Buffer
	digest, err := Pack(dir, &buf)
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}
	if len(digest) != 64 || !isHexLower(digest) {
		t.Fatalf("digest %q is not 64-char lowercase hex", digest)
	}
	// The key must be exactly what the consumer's parser accepts.
	key := ScriptKey(digest)
	if key != "scripts/"+digest+".tar.gz" {
		t.Errorf("ScriptKey=%q", key)
	}
}

func TestPack_Deterministic(t *testing.T) {
	dir := writeScriptSet(t)
	var a, b bytes.Buffer
	d1, err := Pack(dir, &a)
	if err != nil {
		t.Fatal(err)
	}
	d2, err := Pack(dir, &b)
	if err != nil {
		t.Fatal(err)
	}
	if d1 != d2 {
		t.Errorf("same tree gave different digests: %s vs %s", d1, d2)
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Error("same tree gave different bytes (non-deterministic pack)")
	}
}

func TestPack_RoundTripContainsEntrypointExecutable(t *testing.T) {
	dir := writeScriptSet(t)
	var buf bytes.Buffer
	if _, err := Pack(dir, &buf); err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(&buf)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	foundExec := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Name == Entrypoint {
			foundExec = hdr.Mode&0o100 != 0
		}
	}
	if !foundExec {
		t.Error("bootstrap.sh missing or not executable in the tarball")
	}
}

// TestPack_ReferenceScriptSet packs the real scripts/bootstrap reference set so
// it can never silently rot: it must stay packable (contains an executable
// bootstrap.sh entrypoint) and deterministic. This wires the shell artifact into
// `make check`.
func TestPack_ReferenceScriptSet(t *testing.T) {
	dir := filepath.Join("..", "..", "scripts", "bootstrap")
	if _, err := os.Stat(filepath.Join(dir, Entrypoint)); err != nil {
		t.Fatalf("reference script-set missing %s: %v", Entrypoint, err)
	}

	var a, b bytes.Buffer
	d1, err := Pack(dir, &a)
	if err != nil {
		t.Fatalf("Pack reference set: %v", err)
	}
	d2, err := Pack(dir, &b)
	if err != nil {
		t.Fatal(err)
	}
	if d1 != d2 {
		t.Errorf("reference set digest is non-deterministic: %s vs %s", d1, d2)
	}

	// bootstrap.sh must be present and executable in the tarball.
	gz, err := gzip.NewReader(&a)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	foundExec := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Name == Entrypoint {
			foundExec = hdr.Mode&0o100 != 0
		}
	}
	if !foundExec {
		t.Error("reference scripts/bootstrap/bootstrap.sh missing or not executable in the tarball")
	}
}

func TestPack_MissingEntrypoint(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "other.sh"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Pack(dir, &bytes.Buffer{}); err == nil {
		t.Error("expected error when bootstrap.sh entrypoint is missing")
	}
}

func isHexLower(s string) bool {
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}
