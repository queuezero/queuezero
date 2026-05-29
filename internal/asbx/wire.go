// Package asbx wires the production Slurm-domain Bridge: it constructs the
// AWS-backed provider ports (substrate.Client, Actuator, Observer, Classifier),
// the rate limiter, the record store, the scontrol seam, and loads
// partitions.yaml — then hands a fully-built slurm.Bridge to the resume/suspend
// binaries.
//
// It lives apart from package slurm so the domain stays unit-testable with
// fakes (no AWS SDK pulled into slurm's test binary), and apart from each cmd
// main so q0-resume and q0-suspend share one wiring path. This is the
// composition root for ASBX (ARCHITECTURE §11/§12).
package asbx

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	awssdkconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/queuezero/queuezero/internal/cohort"
	"github.com/queuezero/queuezero/internal/recordstore"
	"github.com/queuezero/queuezero/internal/slurm"
	"github.com/queuezero/queuezero/internal/spec"
	"github.com/queuezero/queuezero/internal/substrate"
	awssub "github.com/queuezero/queuezero/internal/substrate/aws"
)

// Env names mirror the Q0_RECORDS_DIR convention already used by `q0 explain`.
const (
	EnvCluster       = "Q0_CLUSTER"
	EnvRegion        = "Q0_REGION"
	EnvPartitions    = "Q0_PARTITIONS_YAML"
	EnvStateDir      = "Q0_STATE_DIR"
	EnvGeneration    = "Q0_GENERATION"
	EnvBootstrapS3   = "Q0_BOOTSTRAP_S3"
	EnvManifestBucket = "Q0_MANIFEST_BUCKET"
	EnvPartition     = "Q0_PARTITION"
	envSlurmResumePartition = "SLURM_RESUME_PARTITION"

	defaultStateDir = "/var/lib/queuezero"
)

// Settings is the resolved configuration for a resume/suspend invocation.
type Settings struct {
	Cluster        string
	Region         string
	PartitionsYAML string
	StateDir       string
	Generation     string
	BootstrapS3    string
	// ManifestBucket is the S3 bucket the collective peer manifest is published
	// to. Empty => no Assembler is wired and collective resume stays gated.
	ManifestBucket string
	// Partition is the partition name slurmctld is resuming/suspending, if known
	// (from --partition or the Slurm/Q0 env). Empty => resolve by node name.
	Partition string
}

// SettingsFromEnv reads the Q0_* environment. partitionFlag, when non-empty,
// overrides the env-derived partition.
func SettingsFromEnv(partitionFlag string) Settings {
	stateDir := os.Getenv(EnvStateDir)
	if stateDir == "" {
		stateDir = defaultStateDir
	}
	partition := partitionFlag
	if partition == "" {
		partition = firstNonEmpty(os.Getenv(envSlurmResumePartition), os.Getenv(EnvPartition))
	}
	return Settings{
		Cluster:        os.Getenv(EnvCluster),
		Region:         os.Getenv(EnvRegion),
		PartitionsYAML: os.Getenv(EnvPartitions),
		StateDir:       stateDir,
		Generation:     os.Getenv(EnvGeneration),
		BootstrapS3:    os.Getenv(EnvBootstrapS3),
		ManifestBucket: os.Getenv(EnvManifestBucket),
		Partition:      partition,
	}
}

// Validate checks the mandatory settings before any AWS call.
func (s Settings) Validate() error {
	if s.Cluster == "" {
		return fmt.Errorf("asbx: %s is required", EnvCluster)
	}
	if s.Region == "" {
		return fmt.Errorf("asbx: %s is required", EnvRegion)
	}
	if s.PartitionsYAML == "" {
		return fmt.Errorf("asbx: %s is required", EnvPartitions)
	}
	return nil
}

// BuildBridge constructs a production slurm.Bridge from settings. The Assembler
// is wired to an S3 manifest publisher when Q0_MANIFEST_BUCKET is set, enabling
// collective resume; otherwise it stays nil and slurm.Resume gates collective
// partitions with a clear error. The Describer is always wired (the orphan
// sweeper needs it).
func BuildBridge(ctx context.Context, s Settings) (*slurm.Bridge, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}

	parts, err := spec.LoadPartitions(s.PartitionsYAML)
	if err != nil {
		return nil, err
	}

	awsCfg, err := awssdkconfig.LoadDefaultConfig(ctx, awssdkconfig.WithRegion(s.Region))
	if err != nil {
		return nil, fmt.Errorf("asbx: load AWS config: %w", err)
	}
	ec2c := ec2.NewFromConfig(awsCfg)
	lim := substrate.NewLimiter(substrate.LimiterConfig{}, nil)
	client := awssub.NewClient(ec2c, lim)

	actCfg := awssub.ActuatorConfig{
		ClusterName:        s.Cluster,
		Region:             s.Region,
		DefaultBootstrapS3: s.BootstrapS3,
	}
	act := awssub.NewActuator(client, actCfg)
	obs := awssub.NewObserver(client, actCfg)
	clf := awssub.Classifier{}
	enr := slurm.NewEnroller(obs) // *Observer satisfies mpi.ReadinessProbe via ReadReadiness

	store, err := recordstore.NewFileStore(filepath.Join(s.StateDir, "records"))
	if err != nil {
		return nil, err
	}

	// Collective resume needs a real Assembler backed by an S3 manifest
	// publisher. Wire one only when a manifest bucket is configured; when absent,
	// Assembler stays nil and slurm.Resume keeps gating collective partitions
	// with its clear error (no regression).
	var assembler cohort.Assembler
	if s.ManifestBucket != "" {
		s3c := s3.NewFromConfig(awsCfg)
		assembler = slurm.NewAssembler(awssub.NewS3Publisher(s3c, s.ManifestBucket))
	}

	return &slurm.Bridge{
		Reconciler: func(asm cohort.Assembler) *cohort.Reconciler {
			return cohort.NewReconciler(act, obs, clf, enr, asm, lim)
		},
		Actuator:  act,
		Assembler: assembler,
		Scontrol:  slurm.NewScontrol(),
		Records:   store,
		Describer: obs, // *Observer.DescribeCluster satisfies slurm.ClusterDescriber
		Cfg: slurm.Config{
			Cluster:          s.Cluster,
			Region:           s.Region,
			Generation:       cohort.Generation(s.Generation),
			Partitions:       parts,
			DefaultPartition: s.Partition,
			BootstrapS3:      s.BootstrapS3,
		},
	}, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
