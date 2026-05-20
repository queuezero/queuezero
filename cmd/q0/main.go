// Command q0 is the queuezero CLI.
//
// queuezero is a spend-governed, multi-account cloud cluster provisioner with
// a Slurm-compatible front end — the replacement for AWS ParallelCluster/PCS.
// See docs/ARCHITECTURE.md.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var version = "0.0.0-dev"

func main() {
	root := &cobra.Command{
		Use:   "q0",
		Short: "queuezero — spend-governed multi-account cloud cluster provisioner",
		Long: "queuezero (q0) provisions and operates HPC clusters across multiple\n" +
			"cloud accounts. Static substrate via OpenTofu; elastic fleet via direct\n" +
			"API with fast-fail classification. No CloudFormation, no CDK, no ASG.",
	}
	root.Version = version

	root.AddCommand(
		cmdApply(),
		cmdPreflight(),
		cmdImport(),
		cmdCapture(),
		cmdExplain(),
	)

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "q0:", err)
		os.Exit(1)
	}
}

// cmdApply applies one or more composable spec layers (cluster.yaml,
// stack.yaml, partitions.yaml, users.yaml), each independently content-hashed.
func cmdApply() *cobra.Command {
	return &cobra.Command{
		Use:   "apply [layer]",
		Short: "apply a composable spec layer (cluster|stack|partitions|users)",
		RunE: func(*cobra.Command, []string) error {
			return fmt.Errorf("not yet implemented — see docs/ARCHITECTURE.md §2")
		},
	}
}

// cmdPreflight checks quotas, IAM (SimulatePrincipalPolicy), instance-type
// offerings per AZ, and AMI/subnet/EFA compatibility BEFORE any mutation.
// Failing in 10 seconds instead of 20 minutes is most of the felt difference
// from ParallelCluster.
func cmdPreflight() *cobra.Command {
	return &cobra.Command{
		Use:   "preflight",
		Short: "verify quotas, IAM, capacity offerings and compatibility — no mutation",
		RunE: func(*cobra.Command, []string) error {
			return fmt.Errorf("not yet implemented — see docs/ARCHITECTURE.md §12")
		},
	}
}

func cmdImport() *cobra.Command {
	c := &cobra.Command{
		Use:   "import",
		Short: "recast an existing cluster as queuezero spec files",
	}
	c.AddCommand(&cobra.Command{
		Use:   "parallelcluster [config.yaml]",
		Short: "convert a ParallelCluster config into queuezero spec layers",
		RunE: func(*cobra.Command, []string) error {
			return fmt.Errorf("not yet implemented — see internal/capture")
		},
	})
	return c
}

func cmdCapture() *cobra.Command {
	return &cobra.Command{
		Use:   "capture",
		Short: "introspect a live on-prem cluster and emit replicating spec files",
		RunE: func(*cobra.Command, []string) error {
			return fmt.Errorf("not yet implemented — see internal/capture")
		},
	}
}

// cmdExplain renders the full structured failure trace for an entity: fault
// class, verbatim provider code, the phase it died in, every rung attempted.
func cmdExplain() *cobra.Command {
	return &cobra.Command{
		Use:   "explain [entity]",
		Short: "show the structured reconciliation trace for an entity",
		RunE: func(*cobra.Command, []string) error {
			return fmt.Errorf("not yet implemented — see internal/cohort/explain.go")
		},
	}
}
