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

	"github.com/queuezero/queuezero/internal/cohort"
	"github.com/queuezero/queuezero/internal/recordstore"
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
	// A runtime error (e.g. "no record for entity") is not a usage mistake;
	// don't dump the usage block on it. Cobra still prints usage for genuine
	// flag/arg errors via Args validators.
	root.SilenceUsage = true
	root.SilenceErrors = true

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
// It reads the persisted cohort.Record from the record store under the cluster
// state dir — the reconciler process is long gone (ResumeProgram is per-call),
// so the Record must be read from durable storage, not held in memory.
//
// With no entity argument it lists every entity that has a stored record.
func cmdExplain() *cobra.Command {
	var dir string
	c := &cobra.Command{
		Use:   "explain [entity]",
		Short: "show the structured reconciliation trace for an entity",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			store, err := recordstore.NewFileStore(dir)
			if err != nil {
				return err
			}

			// No entity: list what records exist.
			if len(args) == 0 {
				ids, err := store.List()
				if err != nil {
					return err
				}
				if len(ids) == 0 {
					return fmt.Errorf("no reconciliation records found under %s", dir)
				}
				fmt.Printf("records under %s:\n", dir)
				for _, id := range ids {
					rec, err := store.Get(id)
					if err != nil {
						fmt.Printf("  %-24s (unreadable: %v)\n", id, err)
						continue
					}
					fmt.Printf("  %-24s %s\n", id, rec.Summary())
				}
				return nil
			}

			rec, err := store.Get(cohort.EntityID(args[0]))
			if err != nil {
				return err
			}
			fmt.Print(rec.Explain())
			return nil
		},
	}
	c.Flags().StringVar(&dir, "records-dir", defaultRecordsDir(), "directory holding persisted reconciliation records")
	return c
}

// defaultRecordsDir is where records live absent an explicit flag. It mirrors
// the controller state-dir convention; the real path comes from cluster.yaml's
// ControllerSpec.StateDir once `q0 apply` wires it through.
func defaultRecordsDir() string {
	if d := os.Getenv("Q0_RECORDS_DIR"); d != "" {
		return d
	}
	return "/var/lib/queuezero/records"
}
