package tofu

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/queuezero/queuezero/internal/spec"
)

func testCluster() *spec.Cluster {
	return &spec.Cluster{
		Name: "gauss", ControlAccount: "111122223333", Region: "us-west-2",
		Network: spec.NetworkSpec{BYO: false, CIDR: "10.0.0.0/16"},
	}
}

// generatedWithController: generated VPC + a controller pet.
func generatedWithController() *spec.Cluster {
	c := testCluster()
	c.Controller = spec.ControllerSpec{
		InstanceType: "m7i.2xlarge", AMIHash: "ami-deadbeef",
		StandbyHost: "gauss-ctl-2", StateDir: "/shared/state",
	}
	return c
}

// byoCluster: bring-your-own network, no controller.
func byoCluster() *spec.Cluster {
	return &spec.Cluster{
		Name: "gauss", ControlAccount: "111122223333", Region: "us-west-2",
		Network: spec.NetworkSpec{BYO: true, VPCID: "vpc-abc", SubnetIDs: []string{"subnet-1", "subnet-2"}},
	}
}

// withStorage: generated network + controller whose StateDir lives on a declared efs mount.
func withStorage() *spec.Cluster {
	c := generatedWithController()
	c.Controller.StateDir = "/shared/state"
	c.Storage = []spec.StorageSpec{{Kind: "efs", MountPath: "/shared"}}
	return c
}

func TestGenerateClusterFoundation_RendersExpectedResources(t *testing.T) {
	files, err := GenerateClusterFoundation(testCluster(), FoundationOpts{
		ScriptsBucket:  "gauss-q0-scripts",
		ManifestBucket: "gauss-q0-manifest",
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	main := files["main.tf"]
	wants := []string{
		`backend "s3" {}`,                                  // state backend wired
		`region = "us-west-2"`,                             // from cluster.yaml
		`resource "aws_s3_bucket" "scripts"`,               // scripts bucket
		`bucket = "gauss-q0-scripts"`,
		`resource "aws_s3_bucket_versioning" "scripts"`,    // versioned
		`resource "aws_s3_bucket_public_access_block" "scripts"`, // public-access-blocked
		`resource "aws_s3_bucket" "manifest"`,              // manifest bucket (opt set)
		`resource "aws_iam_role" "node"`,                   // node role
		`name = "q0-node"`,                                 // default role name
		`resource "aws_iam_instance_profile" "node"`,       // instance profile
		`"s3:GetObject"`,                                   // scoped read
		`"${aws_s3_bucket.scripts.arn}/*"`,                 // scoped to scripts bucket
	}
	for _, w := range wants {
		if !strings.Contains(main, w) {
			t.Errorf("main.tf missing %q", w)
		}
	}
	out := files["outputs.tf"]
	if !strings.Contains(out, "node_instance_profile_arn") || !strings.Contains(out, "scripts_bucket") {
		t.Errorf("outputs.tf missing expected outputs:\n%s", out)
	}
}

func TestGenerateClusterFoundation_NoManifestBucket(t *testing.T) {
	files, err := GenerateClusterFoundation(testCluster(), FoundationOpts{ScriptsBucket: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(files["main.tf"], `"manifest"`) {
		t.Error("manifest bucket should be omitted when ManifestBucket is empty")
	}
	if strings.Contains(files["outputs.tf"], "manifest_bucket") {
		t.Error("manifest output should be omitted when ManifestBucket is empty")
	}
}

func TestGenerateClusterFoundation_RequiresScriptsBucket(t *testing.T) {
	if _, err := GenerateClusterFoundation(testCluster(), FoundationOpts{}); err == nil {
		t.Error("expected error when ScriptsBucket is empty")
	}
}

func TestGenerateClusterFoundation_Deterministic(t *testing.T) {
	o := FoundationOpts{ScriptsBucket: "b", ManifestBucket: "m"}
	// Exercise the full template set, including storage, for determinism.
	a, _ := GenerateClusterFoundation(withStorage(), o)
	b, _ := GenerateClusterFoundation(withStorage(), o)
	for name := range a {
		if a[name] != b[name] {
			t.Errorf("%s render is non-deterministic", name)
		}
	}
}

func TestWriteFiles(t *testing.T) {
	dir := t.TempDir()
	files, _ := GenerateClusterFoundation(testCluster(), FoundationOpts{ScriptsBucket: "b"})
	if err := WriteFiles(dir, files); err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}
	for _, n := range []string{"main.tf", "variables.tf", "outputs.tf"} {
		if _, err := os.ReadFile(filepath.Join(dir, n)); err != nil {
			t.Errorf("%s not written: %v", n, err)
		}
	}
}

// Generated network renders VPC + per-AZ subnets + IGW + NAT + route tables + SGs.
func TestGenerate_GeneratedNetwork(t *testing.T) {
	files, err := GenerateClusterFoundation(testCluster(), FoundationOpts{ScriptsBucket: "b", AZCount: 2})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	net := files["network.tf"]
	for _, w := range []string{
		`data "aws_availability_zones" "available"`,
		`resource "aws_vpc" "this"`,
		`cidr_block           = "10.0.0.0/16"`,
		`resource "aws_subnet" "private"`,
		`resource "aws_subnet" "public"`,
		`count             = 2`,
		`resource "aws_internet_gateway" "this"`,
		`resource "aws_nat_gateway" "this"`,           // default egress = nat-gateway
		`resource "aws_route_table" "private"`,
		`nat_gateway_id         = aws_nat_gateway.this.id`, // private default route via NAT
		`resource "aws_vpc_endpoint" "s3"`,            // free gateway endpoints in all modes
		`resource "aws_vpc_endpoint" "dynamodb"`,
		`resource "aws_security_group" "controller"`,
		`resource "aws_security_group" "compute"`,
		`local.vpc_id`,
	} {
		if !strings.Contains(net, w) {
			t.Errorf("network.tf missing %q", w)
		}
	}
	// Default mode must NOT render a NAT instance.
	if strings.Contains(net, `resource "aws_instance" "nat"`) {
		t.Error("default egress (nat-gateway) must not render a NAT instance")
	}
}

// nat-instance egress => a NAT instance (not a managed gateway), the SSM AMI
// data source, and the private default route via the instance ENI.
func TestGenerate_Egress_NATInstance(t *testing.T) {
	c := testCluster()
	c.Network.Egress = "nat-instance"
	files, err := GenerateClusterFoundation(c, FoundationOpts{ScriptsBucket: "b", AZCount: 2})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	net := files["network.tf"]
	for _, w := range []string{
		`resource "aws_instance" "nat"`,
		`source_dest_check           = false`,
		`data "aws_ssm_parameter" "nat_ami"`,
		`resource "aws_security_group" "nat"`,
		`network_interface_id   = aws_instance.nat.primary_network_interface_id`,
		`resource "aws_vpc_endpoint" "s3"`,
	} {
		if !strings.Contains(net, w) {
			t.Errorf("nat-instance network.tf missing %q", w)
		}
	}
	if strings.Contains(net, `resource "aws_nat_gateway"`) {
		t.Error("nat-instance must not render a managed NAT gateway")
	}
}

// endpoints-only egress => no NAT of any kind and no default (0.0.0.0/0) private
// route; only the gateway endpoints reach AWS services.
func TestGenerate_Egress_EndpointsOnly(t *testing.T) {
	c := testCluster()
	c.Network.Egress = "endpoints-only"
	files, err := GenerateClusterFoundation(c, FoundationOpts{ScriptsBucket: "b", AZCount: 2})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	net := files["network.tf"]
	if strings.Contains(net, `resource "aws_nat_gateway"`) || strings.Contains(net, `resource "aws_instance" "nat"`) {
		t.Error("endpoints-only must render no NAT gateway and no NAT instance")
	}
	if strings.Contains(net, `resource "aws_route" "private_default"`) {
		t.Error("endpoints-only must have no default (0.0.0.0/0) private route")
	}
	for _, w := range []string{`resource "aws_vpc_endpoint" "s3"`, `resource "aws_vpc_endpoint" "dynamodb"`} {
		if !strings.Contains(net, w) {
			t.Errorf("endpoints-only network.tf missing %q", w)
		}
	}
}

// BYO network renders the locals passthrough and NO aws_vpc.
func TestGenerate_BYONetwork(t *testing.T) {
	files, err := GenerateClusterFoundation(byoCluster(), FoundationOpts{ScriptsBucket: "b"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	net := files["network.tf"]
	if strings.Contains(net, `resource "aws_vpc"`) {
		t.Error("BYO network must not create a VPC")
	}
	if !strings.Contains(net, `vpc_id     = "vpc-abc"`) || !strings.Contains(net, `"subnet-1", "subnet-2"`) {
		t.Errorf("BYO network should pass through vpc/subnets via locals:\n%s", net)
	}
	// SGs are still created (they reference local.vpc_id).
	if !strings.Contains(net, `resource "aws_security_group" "controller"`) {
		t.Error("SGs should be created even for BYO")
	}
}

// Controller present => exactly one AMI-pinned aws_instance with the slurmctld tag.
func TestGenerate_ControllerPresent(t *testing.T) {
	files, err := GenerateClusterFoundation(generatedWithController(), FoundationOpts{ScriptsBucket: "b"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	ctl := files["controller.tf"]
	for _, w := range []string{
		`resource "aws_instance" "controller"`,
		`ami                    = "ami-deadbeef"`,
		`instance_type          = "m7i.2xlarge"`,
		`Name          = "gauss-slurmctld"`,
		`"q0:standby"  = "gauss-ctl-2"`,
		`resource "aws_iam_instance_profile" "controller"`,
	} {
		if !strings.Contains(ctl, w) {
			t.Errorf("controller.tf missing %q", w)
		}
	}
	// Exactly one PRIMARY controller instance (a pet, not a count). The needle ends
	// in the closing quote after "controller", so it does not match the distinct
	// "controller_standby" resource.
	if n := strings.Count(ctl, `resource "aws_instance" "controller"`); n != 1 {
		t.Errorf("want exactly 1 primary controller instance, got %d", n)
	}
	if strings.Contains(ctl, "count =") {
		t.Error("controller must not use count (it is a named pet, not an ASG)")
	}
	// The fixture sets StandbyHost=gauss-ctl-2 => a second named standby pet.
	for _, w := range []string{
		`resource "aws_instance" "controller_standby"`,
		`Name          = "gauss-ctl-2"`,
		`"q0:role"     = "controller-standby"`,
	} {
		if !strings.Contains(ctl, w) {
			t.Errorf("controller.tf missing standby %q", w)
		}
	}
	out := files["outputs.tf"]
	if !strings.Contains(out, "controller_private_ip") {
		t.Error("outputs.tf should export controller_private_ip when a controller is present")
	}
	if !strings.Contains(out, "controller_standby_private_ip") {
		t.Error("outputs.tf should export controller_standby_private_ip when a standby is declared")
	}
}

// A controller WITHOUT a StandbyHost renders no standby resource or output.
func TestGenerate_ControllerNoStandby(t *testing.T) {
	c := generatedWithController()
	c.Controller.StandbyHost = ""
	files, err := GenerateClusterFoundation(c, FoundationOpts{ScriptsBucket: "b"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if strings.Contains(files["controller.tf"], "controller_standby") {
		t.Error("no StandbyHost => no standby instance")
	}
	if strings.Contains(files["outputs.tf"], "controller_standby_private_ip") {
		t.Error("no StandbyHost => no standby output")
	}
	// The primary controller is still there.
	if !strings.Contains(files["controller.tf"], `resource "aws_instance" "controller"`) {
		t.Error("primary controller should still render without a standby")
	}
}

// Controller absent => no aws_instance.
func TestGenerate_ControllerAbsent(t *testing.T) {
	files, _ := GenerateClusterFoundation(testCluster(), FoundationOpts{ScriptsBucket: "b"})
	if strings.Contains(files["controller.tf"], `resource "aws_instance"`) {
		t.Error("no controller requested => no aws_instance")
	}
	if strings.Contains(files["outputs.tf"], "controller_private_ip") {
		t.Error("no controller => no controller output")
	}
}

// EFS storage renders the file system + per-AZ mount targets + the NFS SG + outputs.
func TestGenerate_Storage_EFS(t *testing.T) {
	files, err := GenerateClusterFoundation(withStorage(), FoundationOpts{ScriptsBucket: "b", AZCount: 2})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	st := files["storage.tf"]
	for _, w := range []string{
		`resource "aws_security_group" "nfs"`,
		`from_port       = 2049`,
		`security_groups = [aws_security_group.compute.id]`,
		`security_groups = [aws_security_group.controller.id]`,
		`resource "aws_efs_file_system" "efs_0"`,
		`encrypted      = true`,
		`creation_token = "gauss-efs-0"`,
		`resource "aws_efs_mount_target" "efs_0"`,
		`count           = 2`,
		`subnet_id       = local.subnet_ids[count.index]`,
	} {
		if !strings.Contains(st, w) {
			t.Errorf("storage.tf missing %q", w)
		}
	}
	out := files["outputs.tf"]
	if !strings.Contains(out, "efs_0_id") || !strings.Contains(out, "efs_0_dns") {
		t.Errorf("outputs.tf missing efs outputs:\n%s", out)
	}
}

// No storage declared => storage.tf has no resources.
func TestGenerate_Storage_None(t *testing.T) {
	files, _ := GenerateClusterFoundation(testCluster(), FoundationOpts{ScriptsBucket: "b"})
	if strings.Contains(files["storage.tf"], `resource "aws_efs`) {
		t.Error("no storage => no efs resources")
	}
	if strings.Contains(files["outputs.tf"], "efs_0_id") {
		t.Error("no storage => no efs outputs")
	}
}

func TestGenerate_Storage_FSxLustre(t *testing.T) {
	c := testCluster()
	c.Storage = []spec.StorageSpec{{Kind: "fsx-lustre", MountPath: "/scratch"}} // bare => generator defaults
	files, err := GenerateClusterFoundation(c, FoundationOpts{ScriptsBucket: "b", AZCount: 2})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	st := files["storage.tf"]
	for _, w := range []string{
		`resource "aws_security_group" "lustre"`,
		`from_port       = 988`,
		`from_port       = 1018`,
		`to_port         = 1023`,
		`resource "aws_fsx_lustre_file_system" "fsx_0"`,
		`storage_capacity   = 1200`,    // default
		`deployment_type    = "SCRATCH_2"`, // default
		`subnet_ids         = [local.subnet_ids[0]]`,
		`security_group_ids = [aws_security_group.lustre.id]`,
	} {
		if !strings.Contains(st, w) {
			t.Errorf("storage.tf missing %q\n---\n%s", w, st)
		}
	}
	// bare fsx => no DRA, no NFS SG (no efs entry).
	if strings.Contains(st, "aws_fsx_data_repository_association") {
		t.Error("no s3Linkage => no data repository association")
	}
	if strings.Contains(st, `resource "aws_security_group" "nfs"`) {
		t.Error("no efs entry => no NFS security group")
	}
	out := files["outputs.tf"]
	for _, w := range []string{"fsx_0_id", "fsx_0_dns", "fsx_0_mountname"} {
		if !strings.Contains(out, w) {
			t.Errorf("outputs.tf missing %q:\n%s", w, out)
		}
	}
}

func TestGenerate_Storage_FSxLustreWithDRA(t *testing.T) {
	c := testCluster()
	c.Storage = []spec.StorageSpec{{
		Kind: "fsx-lustre", MountPath: "/scratch",
		CapacityGiB: 2400, DeploymentType: "PERSISTENT_2", S3Linkage: "s3://bucket/prefix",
	}}
	files, err := GenerateClusterFoundation(c, FoundationOpts{ScriptsBucket: "b", AZCount: 2})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	st := files["storage.tf"]
	for _, w := range []string{
		`storage_capacity   = 2400`,
		`deployment_type    = "PERSISTENT_2"`,
		`resource "aws_fsx_data_repository_association" "fsx_0"`,
		`data_repository_path = "s3://bucket/prefix"`,
		`file_system_path     = "/scratch"`,
	} {
		if !strings.Contains(st, w) {
			t.Errorf("storage.tf missing %q\n---\n%s", w, st)
		}
	}
}

// A cluster with BOTH kinds renders both security groups and both resource sets.
func TestGenerate_Storage_Mixed(t *testing.T) {
	c := generatedWithController()
	c.Controller.StateDir = "/shared/state"
	c.Storage = []spec.StorageSpec{
		{Kind: "efs", MountPath: "/shared"},
		{Kind: "fsx-lustre", MountPath: "/scratch"},
	}
	files, err := GenerateClusterFoundation(c, FoundationOpts{ScriptsBucket: "b", AZCount: 2})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	st := files["storage.tf"]
	for _, w := range []string{
		`resource "aws_security_group" "nfs"`,
		`resource "aws_security_group" "lustre"`,
		`resource "aws_efs_file_system" "efs_0"`,
		`resource "aws_fsx_lustre_file_system" "fsx_1"`,
	} {
		if !strings.Contains(st, w) {
			t.Errorf("mixed storage.tf missing %q\n---\n%s", w, st)
		}
	}
	out := files["outputs.tf"]
	for _, w := range []string{"efs_0_dns", "fsx_1_dns", "fsx_1_mountname"} {
		if !strings.Contains(out, w) {
			t.Errorf("mixed outputs.tf missing %q", w)
		}
	}
}

// ---- shared fakeExecutor (used by command-level checks) ---------------------

type fakeExecutor struct {
	calls      []string
	planResult PlanSummary
	applyErr   error
	outputs    map[string]string
}

func (f *fakeExecutor) Init(_ context.Context, _ string, _ BackendConfig) error {
	f.calls = append(f.calls, "init")
	return nil
}
func (f *fakeExecutor) Plan(_ context.Context, _ string) (PlanSummary, error) {
	f.calls = append(f.calls, "plan")
	return f.planResult, nil
}
func (f *fakeExecutor) Apply(_ context.Context, _ string) error {
	f.calls = append(f.calls, "apply")
	return f.applyErr
}
func (f *fakeExecutor) Output(_ context.Context, _ string) (map[string]string, error) {
	f.calls = append(f.calls, "output")
	return f.outputs, nil
}

func TestFakeExecutor_PlanThenOptionalApply(t *testing.T) {
	// Documents the contract the CLI relies on: plan-only never reaches apply.
	f := &fakeExecutor{planResult: PlanSummary{ChangesPending: true}}
	_ = f.Init(context.Background(), "d", BackendConfig{})
	_, _ = f.Plan(context.Background(), "d")
	got := strings.Join(f.calls, ",")
	if got != "init,plan" {
		t.Errorf("calls=%q want init,plan", got)
	}
}
