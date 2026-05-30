package spec

import "time"

// Cluster is cluster.yaml — the static substrate, applied via OpenTofu.
type Cluster struct {
	Name           string         `yaml:"name"`
	ControlAccount string         `yaml:"controlAccount"`
	Region         string         `yaml:"region"`
	Network        NetworkSpec    `yaml:"network"` // BYO or generated
	Controller     ControllerSpec `yaml:"controller"`
	Storage        []StorageSpec  `yaml:"storage"`
}

type NetworkSpec struct {
	BYO       bool     `yaml:"byo"`
	VPCID     string   `yaml:"vpcId,omitempty"`
	SubnetIDs []string `yaml:"subnetIds,omitempty"`
	CIDR      string   `yaml:"cidr,omitempty"`
	// Egress selects how a GENERATED VPC's private subnets reach the internet:
	// nat-gateway (managed NAT, default), nat-instance (a cheap t4g.nano NAT
	// instance), or endpoints-only (no general egress — S3/DynamoDB gateway
	// endpoints only, for fully-baked AMIs). Empty defaults to nat-gateway.
	// Ignored when BYO (a brought network owns its own routing).
	Egress string `yaml:"egress,omitempty"`
}

// Egress modes for a generated VPC (NetworkSpec.Egress).
const (
	EgressNATGateway    = "nat-gateway"
	EgressNATInstance   = "nat-instance"
	EgressEndpointsOnly = "endpoints-only"
)

// ControllerSpec describes the slurmctld pair. The controller is an
// explicitly named, stateful singleton with a named standby — NOT an ASG of
// one. Durability lives in StateDir and the accounting DB, not in instance
// fungibility. See ARCHITECTURE §9.
type ControllerSpec struct {
	InstanceType string `yaml:"instanceType"`
	StandbyHost  string `yaml:"standbyHost"`  // Slurm backup SlurmctldHost
	StateDir     string `yaml:"stateDir"`     // on durable shared storage
	AccountingDB string `yaml:"accountingDb"` // RDS endpoint, not on-box
	AMIHash      string `yaml:"amiHash"`
}

type StorageSpec struct {
	Kind      string `yaml:"kind"` // fsx-lustre | efs | ...
	MountPath string `yaml:"mountPath"`
	// S3Linkage is the S3 data-repository path (e.g. "s3://bucket/prefix") for an
	// fsx-lustre mount's Data Repository Association. Empty => no DRA. Ignored for efs.
	S3Linkage string `yaml:"s3Linkage,omitempty"`
	// CapacityGiB is the fsx-lustre filesystem size. The generator defaults it to
	// 1200 (the FSx minimum) when 0; a non-zero value must be a positive multiple
	// of 1200. Ignored for efs (EFS is elastic).
	CapacityGiB int `yaml:"capacityGiB,omitempty"`
	// DeploymentType is the fsx-lustre deployment type (SCRATCH_1|SCRATCH_2|
	// PERSISTENT_1|PERSISTENT_2). The generator defaults it to SCRATCH_2 when empty.
	// Ignored for efs.
	DeploymentType string `yaml:"deploymentType,omitempty"`
}

// Partitions is partitions.yaml.
type Partitions struct {
	StackHash  string      `yaml:"stackHash"` // pins the stack.yaml layer
	Partitions []Partition `yaml:"partitions"`
}

// Partition maps a Slurm partition to an execution account and an ordered,
// operator-approved capacity fallback chain.
type Partition struct {
	Name             string        `yaml:"name"`
	ExecutionAccount string        `yaml:"executionAccount"` // multi-account, §3
	MaxNodes         int           `yaml:"maxNodes"`
	FallbackChain    []Rung        `yaml:"fallbackChain"` // ordered, approved
	WarmPool         WarmPoolSpec  `yaml:"warmPool"`
	Budget           *BudgetSpec   `yaml:"budget,omitempty"`     // overrides DefaultBudget
	Collective       bool          `yaml:"collective"`           // true => MPI-style all-or-nothing barrier

	// NodePrefix optionally decouples the Slurm node-name prefix from the
	// partition Name, for resolving which partition a bare node name belongs to
	// when slurmctld did not pass the partition explicitly. Empty => match
	// against Name. See internal/spec.PartitionIndex.ResolveForNode.
	NodePrefix string `yaml:"nodePrefix,omitempty"`
}

// Rung is one option in a fallback chain. ASBA may PROPOSE rungs; the operator
// APPROVES them by committing this file; queuezero never substitutes outside it.
type Rung struct {
	InstanceType  string `yaml:"instanceType"`
	AvailZone     string `yaml:"availZone"`
	CapacityModel string `yaml:"capacityModel"` // ondemand | spot | reserved
}

// WarmPoolSpec sizes the stopped/hibernated pool for a partition. Pool size is
// a spend-rate knob owned by ASBB, since warm instances bill for EBS.
type WarmPoolSpec struct {
	Stopped    int  `yaml:"stopped"`
	Hibernated int  `yaml:"hibernated"`
}

type BudgetSpec struct {
	LaunchAcked    time.Duration `yaml:"launchAcked"`
	Running        time.Duration `yaml:"running"`
	Enrolled       time.Duration `yaml:"enrolled"`
	CohortBarrier  time.Duration `yaml:"cohortBarrier"`
	CohortAssembly time.Duration `yaml:"cohortAssembly"`
}
