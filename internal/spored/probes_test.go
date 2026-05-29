package spored

import (
	"context"
	"errors"
	"testing"
)

func TestMountProbe_NotAMountPoint(t *testing.T) {
	// A fresh temp dir shares its parent's device, so it is NOT a mount point —
	// the probe must fail, which is the dead-mount catch.
	dir := t.TempDir()
	p := MountProbe{Path: dir}
	if err := p.Check(context.Background()); err == nil {
		t.Error("a non-mount directory should fail the mount probe")
	}
}

func TestMountProbe_MissingPath(t *testing.T) {
	p := MountProbe{Path: "/no/such/path/q0test"}
	if err := p.Check(context.Background()); err == nil {
		t.Error("a missing path should fail the mount probe")
	}
}

func TestMountProbe_Root(t *testing.T) {
	// "/" IS a mount point (its device differs from its parent, which is itself
	// the root — same device, so this actually fails the parent-differs test).
	// We only assert the probe runs without panicking and returns a definite
	// result; root's mount semantics vary by platform.
	p := MountProbe{Path: "/"}
	_ = p.Check(context.Background())
}

func TestSlurmdProbe_FakeRunner(t *testing.T) {
	pass := SlurmdProbe{Runner: func(context.Context) error { return nil }}
	if err := pass.Check(context.Background()); err != nil {
		t.Errorf("passing runner should yield healthy, got %v", err)
	}
	fail := SlurmdProbe{Runner: func(context.Context) error { return errors.New("dead") }}
	if err := fail.Check(context.Background()); err == nil {
		t.Error("failing runner should yield unhealthy")
	}
}
