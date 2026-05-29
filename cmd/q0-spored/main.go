// Command q0-spored is queuezero's on-node readiness reporter — the WRITER half
// of the hybrid Observer's tag control channel (ARCHITECTURE §11). It runs on
// every provisioned compute node (installed and started by the deferred §11
// userdata fetch-shim), resolves its own identity via IMDS, runs mount + slurmd
// health probes, and writes q0:phase/q0:ready/q0:detail to its own EC2 tags. The
// off-node Observer reads those for phase-3 enrollment.
//
// It is a SENSOR, NOT AN ORCHESTRATOR (non-negotiable #8): no idle-kill, no
// self-termination. See internal/spored for the testable core.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	awssdkconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
	"github.com/aws/aws-sdk-go-v2/service/ec2"

	"github.com/queuezero/queuezero/internal/spored"
	"github.com/queuezero/queuezero/internal/substrate"
	awssub "github.com/queuezero/queuezero/internal/substrate/aws"
)

func main() {
	interval := flag.Duration("interval", 15*time.Second, "how often to re-check health and write readiness tags")
	once := flag.Bool("once", false, "report once and exit (for testing/debugging)")
	flag.Parse()

	region := os.Getenv("Q0_REGION")
	if region == "" {
		fmt.Fprintln(os.Stderr, "q0-spored: Q0_REGION is required")
		os.Exit(1)
	}

	// SIGTERM/SIGINT -> graceful stop (systemd stop, node shutdown).
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	awsCfg, err := awssdkconfig.LoadDefaultConfig(ctx, awssdkconfig.WithRegion(region))
	if err != nil {
		fmt.Fprintln(os.Stderr, "q0-spored: load AWS config:", err)
		os.Exit(1)
	}
	ec2c := ec2.NewFromConfig(awsCfg)
	lim := substrate.NewLimiter(substrate.LimiterConfig{}, nil)
	client := awssub.NewClient(ec2c, lim)
	imdsClient := imds.NewFromConfig(awsCfg)

	identity := newIMDSIdentity(imdsClient, client)
	reporter := spored.NewReporter(identity, client, probesFromEnv()...)

	if *once {
		if _, err := reporter.ReportOnce(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "q0-spored:", err)
			os.Exit(1)
		}
		return
	}

	if err := reporter.Run(ctx, *interval); err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, "q0-spored:", err)
		os.Exit(1)
	}
}

// probesFromEnv builds the health probes from configuration:
//   - Q0_MOUNT_PATHS: comma-separated mount points to verify (the dead-Lustre
//     catch). Empty => no mount probe.
//   - Q0_CHECK_SLURMD: "true" (default) adds the slurmd liveness probe.
func probesFromEnv() []spored.Probe {
	var probes []spored.Probe
	for _, p := range strings.Split(os.Getenv("Q0_MOUNT_PATHS"), ",") {
		if p = strings.TrimSpace(p); p != "" {
			probes = append(probes, spored.MountProbe{Path: p})
		}
	}
	if os.Getenv("Q0_CHECK_SLURMD") != "false" {
		probes = append(probes, spored.SlurmdProbe{})
	}
	return probes
}
