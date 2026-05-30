package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseMountPathsLine(t *testing.T) {
	content := "Q0_MOUNT_SPEC='fs-0:/shared,fs-1:/scratch'\nQ0_MOUNT_PATHS='/shared,/scratch'\n"
	if got := parseMountPathsLine(content); got != "/shared,/scratch" {
		t.Errorf("parseMountPathsLine = %q, want /shared,/scratch", got)
	}
	if got := parseMountPathsLine("nothing here\n"); got != "" {
		t.Errorf("absent line => empty, got %q", got)
	}
}

func TestMountPaths_EnvWins(t *testing.T) {
	t.Setenv("Q0_MOUNT_PATHS", "/from-env")
	if got := mountPaths(); got != "/from-env" {
		t.Errorf("env should win, got %q", got)
	}
}

func TestMountPaths_FileFallback(t *testing.T) {
	t.Setenv("Q0_MOUNT_PATHS", "") // unset
	dir := t.TempDir()
	f := filepath.Join(dir, "mounts")
	if err := os.WriteFile(f, []byte("Q0_MOUNT_PATHS='/shared,/scratch'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("Q0_MOUNTS_FILE", f)
	if got := mountPaths(); got != "/shared,/scratch" {
		t.Errorf("file fallback = %q, want /shared,/scratch", got)
	}
}

func TestMountPaths_NeitherPresent(t *testing.T) {
	t.Setenv("Q0_MOUNT_PATHS", "")
	t.Setenv("Q0_MOUNTS_FILE", filepath.Join(t.TempDir(), "nonexistent"))
	if got := mountPaths(); got != "" {
		t.Errorf("neither env nor file => empty, got %q", got)
	}
}

func TestProbesFromEnv_FromFile(t *testing.T) {
	t.Setenv("Q0_MOUNT_PATHS", "")
	t.Setenv("Q0_CHECK_SLURMD", "false") // isolate mount probes
	dir := t.TempDir()
	f := filepath.Join(dir, "mounts")
	_ = os.WriteFile(f, []byte("Q0_MOUNT_PATHS='/shared,/scratch'\n"), 0o644)
	t.Setenv("Q0_MOUNTS_FILE", f)
	if n := len(probesFromEnv()); n != 2 {
		t.Errorf("want 2 mount probes from file, got %d", n)
	}
}
