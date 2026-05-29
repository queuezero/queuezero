// Command q0-resume is the binary slurmctld forks as its ResumeProgram
// (slurm.conf: ResumeProgram=/usr/bin/q0-resume). Slurm invokes it with a
// hostlist as argv; everything else comes from the Q0_* environment of the
// controller. It parses the hostlist into a cohort.Cohort, reconciles it on the
// queuezero cohort core, and marks failed nodes down/drain via scontrol.
//
// This is ASBX (ARCHITECTURE §11). See internal/asbx for the wiring and
// internal/slurm for the resume logic.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/queuezero/queuezero/internal/asbx"
)

func main() {
	partition := flag.String("partition", "", "Slurm partition being resumed (else Q0_PARTITION / SLURM_RESUME_PARTITION / node-name match)")
	flag.Parse()

	args := flag.Args()
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "q0-resume: usage: q0-resume [--partition NAME] <hostlist>")
		os.Exit(2)
	}
	hostlist := args[0]

	ctx := context.Background()
	settings := asbx.SettingsFromEnv(*partition)
	bridge, err := asbx.BuildBridge(ctx, settings)
	if err != nil {
		// A wiring failure prevented any reconcile — exit non-zero so the
		// operator sees it (Slurm logs ResumeProgram stderr).
		fmt.Fprintln(os.Stderr, "q0-resume:", err)
		os.Exit(1)
	}

	if err := bridge.Resume(ctx, settings.Partition, hostlist); err != nil {
		fmt.Fprintln(os.Stderr, "q0-resume:", err)
		os.Exit(1)
	}
	// Per-node launch failures are reported via scontrol node state, not exit
	// code: a non-zero exit would make slurmctld fail the whole batch.
}
