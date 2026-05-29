// Command q0-suspend is the binary slurmctld forks as its SuspendProgram
// (slurm.conf: SuspendProgram=/usr/bin/q0-suspend). Slurm invokes it with a
// hostlist as argv; it stops or terminates each named entity per the partition's
// warm-pool intent. This is ASBX (ARCHITECTURE §11/§12).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/queuezero/queuezero/internal/asbx"
)

func main() {
	partition := flag.String("partition", "", "Slurm partition being suspended (else Q0_PARTITION / node-name match)")
	flag.Parse()

	args := flag.Args()
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "q0-suspend: usage: q0-suspend [--partition NAME] <hostlist>")
		os.Exit(2)
	}
	hostlist := args[0]

	ctx := context.Background()
	settings := asbx.SettingsFromEnv(*partition)
	bridge, err := asbx.BuildBridge(ctx, settings)
	if err != nil {
		fmt.Fprintln(os.Stderr, "q0-suspend:", err)
		os.Exit(1)
	}

	if err := bridge.Suspend(ctx, settings.Partition, hostlist); err != nil {
		// Suspend errors are a cost-leak risk, so surface them — but the orphan
		// sweeper (phase 2b) is the durable backstop.
		fmt.Fprintln(os.Stderr, "q0-suspend:", err)
		os.Exit(1)
	}
}
