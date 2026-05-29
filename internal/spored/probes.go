package spored

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// MountProbe confirms a filesystem path is a live, distinct mount point — the
// catch for the "running+idle with a dead Lustre mount" false positive
// (ARCHITECTURE §7). It compares the device of Path against the device of its
// parent: if they differ, Path is a real mount; if they match, nothing is
// mounted there. It also confirms the path is statable (a hung mount errors).
type MountProbe struct {
	Path string
}

func (p MountProbe) Name() string { return "mount:" + p.Path }

func (p MountProbe) Check(_ context.Context) error {
	fi, err := os.Stat(p.Path)
	if err != nil {
		return fmt.Errorf("stat %s: %w", p.Path, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", p.Path)
	}
	dev, err := deviceID(p.Path)
	if err != nil {
		return err
	}
	parentDev, err := deviceID(filepath.Dir(p.Path))
	if err != nil {
		return err
	}
	if dev == parentDev {
		return fmt.Errorf("%s is not a mount point (same device as parent)", p.Path)
	}
	return nil
}

// SlurmdProbe confirms slurmd is live on this node. It runs a liveness check via
// the provided command (default: `scontrol ping`-style is controller-side, so on
// a compute node we check the slurmd process/service). The Runner indirection
// keeps it fakeable in tests.
type SlurmdProbe struct {
	// Runner runs a liveness command and returns its error. Defaults to checking
	// `systemctl is-active slurmd` when nil.
	Runner func(ctx context.Context) error
}

func (p SlurmdProbe) Name() string { return "slurmd" }

func (p SlurmdProbe) Check(ctx context.Context) error {
	run := p.Runner
	if run == nil {
		run = defaultSlurmdCheck
	}
	if err := run(ctx); err != nil {
		return fmt.Errorf("slurmd not active: %w", err)
	}
	return nil
}

func defaultSlurmdCheck(ctx context.Context) error {
	// `systemctl is-active slurmd` exits 0 only when the unit is active.
	return exec.CommandContext(ctx, "systemctl", "is-active", "slurmd").Run()
}
