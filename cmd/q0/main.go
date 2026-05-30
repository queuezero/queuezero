// Command q0 is the queuezero CLI.
//
// queuezero is a spend-governed, multi-account cloud cluster provisioner with
// a Slurm-compatible front end — the replacement for AWS ParallelCluster/PCS.
// See docs/ARCHITECTURE.md.
package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"time"

	awssdkconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/spf13/cobra"

	"github.com/queuezero/queuezero/internal/asbx"
	"github.com/queuezero/queuezero/internal/bootstrap"
	"github.com/queuezero/queuezero/internal/cohort"
	"github.com/queuezero/queuezero/internal/recordstore"
	"github.com/queuezero/queuezero/internal/slurm"
	"github.com/queuezero/queuezero/internal/spec"
	awssub "github.com/queuezero/queuezero/internal/substrate/aws"
	"github.com/queuezero/queuezero/internal/tofu"
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
		cmdSweep(),
		cmdBootstrap(),
	)

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "q0:", err)
		os.Exit(1)
	}
}

// cmdApply applies one or more composable spec layers (cluster.yaml,
// stack.yaml, partitions.yaml, users.yaml), each independently content-hashed.
func cmdApply() *cobra.Command {
	var region, clusterYAML, workdir string
	var scriptsBucket, manifestBucket, stateBucket, lockTable string
	var adminCIDR string
	var azCount int
	var approve, dryRun bool
	c := &cobra.Command{
		Use:   "apply <layer>",
		Short: "apply a composable spec layer (cluster only in this phase)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			layer := args[0]
			if layer != "cluster" {
				return fmt.Errorf("layer %q not yet implemented (this phase: cluster — the IAM/buckets foundation)", layer)
			}
			if clusterYAML == "" {
				clusterYAML = "cluster.yaml"
			}
			cl, err := spec.LoadCluster(clusterYAML)
			if err != nil {
				return err
			}
			if region == "" {
				region = firstNonEmpty(cl.Region, os.Getenv(asbx.EnvRegion))
			}
			if scriptsBucket == "" {
				scriptsBucket = firstNonEmpty(os.Getenv(asbx.EnvScriptsBucket), cl.Name+"-q0-scripts")
			}
			if manifestBucket == "" {
				manifestBucket = os.Getenv(asbx.EnvManifestBucket)
			}
			if stateBucket == "" {
				stateBucket = firstNonEmpty(os.Getenv(asbx.EnvStateBucket), cl.Name+"-q0-state")
			}
			if lockTable == "" {
				lockTable = firstNonEmpty(os.Getenv(asbx.EnvLockTable), cl.Name+"-q0-lock")
			}
			if workdir == "" {
				workdir = filepath.Join(".q0", "tofu", layer)
			}

			if adminCIDR == "0.0.0.0/0" {
				fmt.Println("warning: --admin-cidr is 0.0.0.0/0 (controller SSH open to the world); set it to your admin range")
			}
			files, err := tofu.GenerateClusterFoundation(cl, tofu.FoundationOpts{
				ScriptsBucket:  scriptsBucket,
				ManifestBucket: manifestBucket,
				AZCount:        azCount,
				AdminCIDR:      adminCIDR,
			})
			if err != nil {
				return err
			}
			if err := tofu.WriteFiles(workdir, files); err != nil {
				return err
			}
			hash, _ := cl.ContentHash()

			netDesc := fmt.Sprintf("generated VPC %s across %d AZ(s)", cl.Network.CIDR, azCount)
			if cl.Network.BYO {
				netDesc = fmt.Sprintf("BYO VPC %s", cl.Network.VPCID)
			}
			ctlDesc := "no controller"
			if cl.Controller.InstanceType != "" {
				ctlDesc = fmt.Sprintf("controller %s (ami %s)", cl.Controller.InstanceType, cl.Controller.AMIHash)
			}

			if dryRun {
				fmt.Printf("layer=cluster hash=%s region=%s workdir=%s\n", hash, region, workdir)
				fmt.Printf("would provision: %s; %s; scripts bucket %q, manifest bucket %q, IAM profile q0-node, state backend %q/%q\n",
					netDesc, ctlDesc, scriptsBucket, manifestBucket, stateBucket, lockTable)
				fmt.Println("rendered HCL written; no AWS touched (--dry-run)")
				return nil
			}
			if region == "" {
				return fmt.Errorf("no region: set cluster.yaml region, --region, or %s", asbx.EnvRegion)
			}

			backend := tofu.BackendConfig{Bucket: stateBucket, LockTable: lockTable, Region: region, Key: layer + "/terraform.tfstate"}
			awsCfg, err := awssdkconfig.LoadDefaultConfig(cmd.Context(), awssdkconfig.WithRegion(region))
			if err != nil {
				return fmt.Errorf("load AWS config: %w", err)
			}
			if err := tofu.EnsureBackend(cmd.Context(), backend, s3.NewFromConfig(awsCfg), dynamodb.NewFromConfig(awsCfg)); err != nil {
				return err
			}
			ex, err := tofu.NewExecutor()
			if err != nil {
				return err
			}
			if err := ex.Init(cmd.Context(), workdir, backend); err != nil {
				return err
			}
			plan, err := ex.Plan(cmd.Context(), workdir)
			if err != nil {
				return err
			}
			if !approve {
				fmt.Printf("layer=cluster hash=%s — plan complete (changes pending: %v)\n", hash, plan.ChangesPending)
				fmt.Println("re-run with --approve to apply")
				return nil
			}
			if err := ex.Apply(cmd.Context(), workdir); err != nil {
				return err
			}
			fmt.Printf("layer=cluster hash=%s applied\n", hash)
			fmt.Printf("pin outputs: %s / %s (see `tofu -chdir=%s output`)\n", asbx.EnvInstanceProfile, asbx.EnvScriptsBucket, workdir)
			return nil
		},
	}
	c.Flags().StringVar(&clusterYAML, "file", "", "path to cluster.yaml (default ./cluster.yaml)")
	c.Flags().StringVar(&region, "region", "", "AWS region (else cluster.yaml region / $"+asbx.EnvRegion+")")
	c.Flags().StringVar(&scriptsBucket, "scripts-bucket", "", "S3 bucket for bootstrap script-sets (else $"+asbx.EnvScriptsBucket+")")
	c.Flags().StringVar(&manifestBucket, "manifest-bucket", "", "S3 bucket for collective peer manifests (else $"+asbx.EnvManifestBucket+")")
	c.Flags().StringVar(&stateBucket, "state-bucket", "", "S3 bucket for tofu state (else $"+asbx.EnvStateBucket+")")
	c.Flags().StringVar(&lockTable, "lock-table", "", "DynamoDB table for tofu state lock (else $"+asbx.EnvLockTable+")")
	c.Flags().StringVar(&workdir, "workdir", "", "where generated HCL is written (default .q0/tofu/<layer>)")
	c.Flags().IntVar(&azCount, "az-count", 2, "availability zones to spread a generated VPC across")
	c.Flags().StringVar(&adminCIDR, "admin-cidr", "0.0.0.0/0", "source CIDR allowed to SSH the controller")
	c.Flags().BoolVar(&approve, "approve", false, "apply the plan (default: plan only)")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "render HCL and print intent; touch no AWS or tofu")
	return c
}

// firstNonEmpty returns the first non-empty string.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
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

// cmdSweep reaps generation-orphaned instances — those left behind by a missed
// teardown (a crashed suspend, a superseded partitions.yaml apply). A missed
// Terminate is a silent cost leak, not a visible failure (ARCHITECTURE §12), so
// this is the durable backstop, run periodically (e.g. cron). It reads the same
// Q0_* environment as q0-resume/q0-suspend.
func cmdSweep() *cobra.Command {
	var grace time.Duration
	var dryRun bool
	var partition string
	c := &cobra.Command{
		Use:   "sweep",
		Short: "reap generation-orphaned instances left by missed teardowns",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			settings := asbx.SettingsFromEnv(partition)
			bridge, err := asbx.BuildBridge(cmd.Context(), settings)
			if err != nil {
				return err
			}
			res, err := bridge.Sweep(cmd.Context(), slurm.SweepOptions{Grace: grace, DryRun: dryRun})
			if err != nil {
				return err
			}
			verb := "reaped"
			if dryRun {
				verb = "would reap"
			}
			for _, d := range res.Reaped {
				fmt.Printf("%s %s (%s, gen=%s): %s\n", verb, d.Entity, d.ProviderID, d.Generation, d.Reason)
			}
			for _, d := range res.Spared {
				fmt.Printf("spared %s (%s, gen=%s): %s\n", d.Entity, d.ProviderID, d.Generation, d.Reason)
			}
			fmt.Printf("sweep: %d %s, %d spared\n", len(res.Reaped), verb, len(res.Spared))
			return nil
		},
	}
	c.Flags().DurationVar(&grace, "grace", 10*time.Minute, "minimum age before a stale-generation instance is reaped")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "report what would be reaped without terminating")
	c.Flags().StringVar(&partition, "partition", "", "partition hint (sweep is cluster-wide; rarely needed)")
	return c
}

// cmdBootstrap groups producer-side bootstrap commands. `push` packages a
// script-set directory, uploads it content-addressed to S3, and prints the
// s3:// URI to pin in Q0_BOOTSTRAP_S3 (which the launch-time userdata shim then
// fetches + verifies + execs — ARCHITECTURE §11).
func cmdBootstrap() *cobra.Command {
	c := &cobra.Command{
		Use:   "bootstrap",
		Short: "build and publish node bootstrap script-sets",
	}
	c.AddCommand(cmdBootstrapPush())
	return c
}

func cmdBootstrapPush() *cobra.Command {
	var bucket, region string
	var dryRun bool
	c := &cobra.Command{
		Use:   "push <dir>",
		Short: "package a script-set, upload it content-addressed, print the URI to pin",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := args[0]
			if bucket == "" {
				bucket = os.Getenv(asbx.EnvScriptsBucket)
			}

			// Package + hash in one pass (no AWS needed yet).
			var buf bytes.Buffer
			digest, err := bootstrap.Pack(dir, &buf)
			if err != nil {
				return err
			}
			uri := "s3://" + bucket + "/" + bootstrap.ScriptKey(digest)

			if dryRun {
				if bucket == "" {
					uri = "s3://<bucket>/" + bootstrap.ScriptKey(digest)
				}
				fmt.Printf("would upload %s (%d bytes, sha256=%s)\n", uri, buf.Len(), digest)
				return nil
			}
			if bucket == "" {
				return fmt.Errorf("no scripts bucket: set --bucket or %s", asbx.EnvScriptsBucket)
			}
			if region == "" {
				region = os.Getenv(asbx.EnvRegion)
			}
			if region == "" {
				return fmt.Errorf("no region: set --region or %s", asbx.EnvRegion)
			}

			awsCfg, err := awssdkconfig.LoadDefaultConfig(cmd.Context(), awssdkconfig.WithRegion(region))
			if err != nil {
				return fmt.Errorf("load AWS config: %w", err)
			}
			uploader := awssub.NewBootstrapUploader(s3.NewFromConfig(awsCfg), bucket)
			gotURI, skipped, err := uploader.PutScriptSet(cmd.Context(), digest, buf.Bytes())
			if err != nil {
				return err
			}
			note := ""
			if skipped {
				note = " (already present)"
			}
			fmt.Printf("%s%s\n", gotURI, note)
			fmt.Printf("pin it: export %s=%s\n", asbx.EnvBootstrapS3, gotURI)
			return nil
		},
	}
	c.Flags().StringVar(&bucket, "bucket", "", "S3 bucket for script-sets (else $"+asbx.EnvScriptsBucket+")")
	c.Flags().StringVar(&region, "region", "", "AWS region (else $"+asbx.EnvRegion+")")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "package and print the URI/digest without uploading")
	return c
}
