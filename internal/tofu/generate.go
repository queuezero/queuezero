package tofu

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/queuezero/queuezero/internal/spec"
)

// FoundationOpts names the resources the cluster-foundation layer creates. These
// are exactly the substrate the elastic-fleet runtime (phases 2c-2e) already
// assumes: an IAM instance profile the bootstrap shim runs under, and the S3
// buckets the uploader and manifest publisher write to.
type FoundationOpts struct {
	ScriptsBucket  string // q0:bootstrap-s3 / Q0_SCRIPTS_BUCKET target
	ManifestBucket string // Q0_MANIFEST_BUCKET target (collective peer manifests)
	NodeRoleName   string // IAM role + instance profile name (e.g. "q0-node"); default applied if empty

	// AZCount is how many availability zones the generated VPC spreads across
	// (private + public subnet per AZ). Default 2. Ignored when Network.BYO.
	AZCount int
	// AdminCIDR is the source range allowed to SSH the controller. Default
	// 0.0.0.0/0 (with a caller warning). Ignored when no controller is requested.
	AdminCIDR string
}

const (
	defaultNodeRole       = "q0-node"
	defaultControllerRole = "q0-controller"
	defaultAZCount        = 2
	defaultAdminCIDR      = "0.0.0.0/0"
)

// templateData is the single struct every foundation template renders against.
// It carries the whole *spec.Cluster (so network/controller templates read
// Network/Controller directly) plus the derived bucket/role/sizing inputs.
type templateData struct {
	Cluster        *spec.Cluster
	Name           string
	Region         string
	ScriptsBucket  string
	ManifestBucket string
	NodeRole       string
	ControllerRole string
	AZCount        int
	AdminCIDR      string
	Storage        []storageTmpl // derived efs entries the storage template renders
}

// storageTmpl is the generator's view of one efs shared-storage mount, derived
// from a spec.StorageSpec so the template never re-parses Kind.
type storageTmpl struct {
	Index     int
	MountPath string
	Token     string // EFS creation_token, deterministic per cluster+index
}

// buildStorage converts the cluster's efs storage entries into template inputs.
// It errors on fsx-lustre (not yet generatable — a later sub-phase); the spec
// stays liberal but the generator is explicit about what it can emit.
func buildStorage(c *spec.Cluster) ([]storageTmpl, error) {
	var out []storageTmpl
	for i, s := range c.Storage {
		switch s.Kind {
		case "efs":
			out = append(out, storageTmpl{
				Index:     i,
				MountPath: s.MountPath,
				Token:     fmt.Sprintf("%s-efs-%d", c.Name, i),
			})
		case "fsx-lustre":
			return nil, fmt.Errorf("tofu: storage[%d] kind fsx-lustre not yet implemented; use efs", i)
		default:
			return nil, fmt.Errorf("tofu: storage[%d] unknown kind %q", i, s.Kind)
		}
	}
	return out, nil
}

// GenerateClusterFoundation renders the .tf files for the foundation layer and
// returns them as filename->content (also written to dir). It does NOT run tofu;
// the caller applies them via an Executor. The generated HCL is deterministic so
// it is golden-file testable and produces stable plans.
func GenerateClusterFoundation(c *spec.Cluster, opts FoundationOpts) (map[string]string, error) {
	if c == nil {
		return nil, fmt.Errorf("tofu: nil cluster")
	}
	if opts.ScriptsBucket == "" {
		return nil, fmt.Errorf("tofu: ScriptsBucket is required (the bootstrap shim fetches from it)")
	}
	if opts.NodeRoleName == "" {
		opts.NodeRoleName = defaultNodeRole
	}
	if opts.AZCount <= 0 {
		opts.AZCount = defaultAZCount
	}
	if opts.AdminCIDR == "" {
		opts.AdminCIDR = defaultAdminCIDR
	}
	storage, err := buildStorage(c)
	if err != nil {
		return nil, err
	}
	data := templateData{
		Cluster:        c,
		Name:           c.Name,
		Region:         c.Region,
		ScriptsBucket:  opts.ScriptsBucket,
		ManifestBucket: opts.ManifestBucket,
		NodeRole:       opts.NodeRoleName,
		ControllerRole: defaultControllerRole,
		AZCount:        opts.AZCount,
		AdminCIDR:      opts.AdminCIDR,
		Storage:        storage,
	}

	files := map[string]string{}
	for name, tmpl := range foundationTemplates {
		var b strings.Builder
		if err := tmpl.Execute(&b, data); err != nil {
			return nil, fmt.Errorf("tofu: render %s: %w", name, err)
		}
		files[name] = b.String()
	}
	return files, nil
}

// WriteFiles persists rendered HCL into dir, creating it if needed.
func WriteFiles(dir string, files map[string]string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("tofu: mkdir %s: %w", dir, err)
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(files[n]), 0o644); err != nil {
			return fmt.Errorf("tofu: write %s: %w", n, err)
		}
	}
	return nil
}

var foundationTemplates = map[string]*template.Template{
	"main.tf":       template.Must(template.New("main").Parse(mainTF)),
	"network.tf":    template.Must(template.New("network").Parse(networkTF)),
	"controller.tf": template.Must(template.New("controller").Parse(controllerTF)),
	"storage.tf":    template.Must(template.New("storage").Parse(storageTF)),
	"variables.tf":  template.Must(template.New("vars").Parse(variablesTF)),
	"outputs.tf":    template.Must(template.New("out").Parse(outputsTF)),
}

const mainTF = `# GENERATED by q0 apply — do not edit by hand. Edit cluster.yaml and re-apply.
terraform {
  backend "s3" {}
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
}

provider "aws" {
  region = "{{.Region}}"
}

# --- Bootstrap script-set bucket (q0 bootstrap push target; shim fetches from here) ---
resource "aws_s3_bucket" "scripts" {
  bucket = "{{.ScriptsBucket}}"
}

resource "aws_s3_bucket_versioning" "scripts" {
  bucket = aws_s3_bucket.scripts.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_public_access_block" "scripts" {
  bucket                  = aws_s3_bucket.scripts.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}
{{if .ManifestBucket}}
# --- Collective peer-manifest bucket (cohort assembler publishes here) ---
resource "aws_s3_bucket" "manifest" {
  bucket = "{{.ManifestBucket}}"
}

resource "aws_s3_bucket_public_access_block" "manifest" {
  bucket                  = aws_s3_bucket.manifest.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}
{{end}}
# --- Node IAM role + instance profile (the bootstrap shim runs under this) ---
resource "aws_iam_role" "node" {
  name = "{{.NodeRole}}"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "ec2.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

# Scoped to GetObject on the scripts bucket only — least privilege for the shim.
resource "aws_iam_role_policy" "node_scripts_read" {
  name = "{{.NodeRole}}-scripts-read"
  role = aws_iam_role.node.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = ["s3:GetObject"]
      Resource = "${aws_s3_bucket.scripts.arn}/*"
    }]
  })
}

resource "aws_iam_instance_profile" "node" {
  name = "{{.NodeRole}}"
  role = aws_iam_role.node.name
}
`

// networkTF renders the VPC/subnets/SGs (or a BYO passthrough). It exposes the
// resolved network through two locals — local.vpc_id and local.subnet_ids — so
// controller.tf and outputs.tf reference the network uniformly regardless of
// whether it was generated or brought-your-own.
const networkTF = `# GENERATED by q0 apply — do not edit by hand. Edit cluster.yaml and re-apply.
{{- if .Cluster.Network.BYO}}
# --- Bring-your-own network: use the operator-supplied VPC + subnets ---
locals {
  vpc_id     = "{{.Cluster.Network.VPCID}}"
  subnet_ids = [{{range $i, $s := .Cluster.Network.SubnetIDs}}{{if $i}}, {{end}}"{{$s}}"{{end}}]
}
{{- else}}
# --- Generated network: VPC across {{.AZCount}} AZ(s), private subnets for
# compute+controller, public subnets + NAT for egress ---
data "aws_availability_zones" "available" {
  state = "available"
}

resource "aws_vpc" "this" {
  cidr_block           = "{{.Cluster.Network.CIDR}}"
  enable_dns_support   = true
  enable_dns_hostnames = true
  tags = { Name = "{{.Name}}", "q0:cluster" = "{{.Name}}" }
}

resource "aws_internet_gateway" "this" {
  vpc_id = aws_vpc.this.id
  tags   = { Name = "{{.Name}}-igw", "q0:cluster" = "{{.Name}}" }
}

resource "aws_subnet" "private" {
  count             = {{.AZCount}}
  vpc_id            = aws_vpc.this.id
  availability_zone = data.aws_availability_zones.available.names[count.index]
  cidr_block        = cidrsubnet(aws_vpc.this.cidr_block, 8, count.index)
  tags = { Name = "{{.Name}}-private-${count.index}", "q0:cluster" = "{{.Name}}" }
}

resource "aws_subnet" "public" {
  count                   = {{.AZCount}}
  vpc_id                  = aws_vpc.this.id
  availability_zone       = data.aws_availability_zones.available.names[count.index]
  cidr_block              = cidrsubnet(aws_vpc.this.cidr_block, 8, count.index + 128)
  map_public_ip_on_launch = true
  tags = { Name = "{{.Name}}-public-${count.index}", "q0:cluster" = "{{.Name}}" }
}

# TODO(cost): a managed NAT gateway is expensive (~$32/mo + $/GB, per-AZ). Revisit:
# S3/DynamoDB VPC gateway endpoints (free, covers bootstrap fetch), a single shared
# NAT, a NAT instance (fck-nat), or public subnets. Make it a cluster.yaml knob.
# See memory: nat-gateway-cost-revisit.
resource "aws_eip" "nat" {
  domain = "vpc"
  tags   = { Name = "{{.Name}}-nat", "q0:cluster" = "{{.Name}}" }
}

resource "aws_nat_gateway" "this" {
  allocation_id = aws_eip.nat.id
  subnet_id     = aws_subnet.public[0].id
  tags          = { Name = "{{.Name}}-nat", "q0:cluster" = "{{.Name}}" }
  depends_on    = [aws_internet_gateway.this]
}

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.this.id
  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.this.id
  }
  tags = { Name = "{{.Name}}-public", "q0:cluster" = "{{.Name}}" }
}

resource "aws_route_table" "private" {
  vpc_id = aws_vpc.this.id
  route {
    cidr_block     = "0.0.0.0/0"
    nat_gateway_id = aws_nat_gateway.this.id
  }
  tags = { Name = "{{.Name}}-private", "q0:cluster" = "{{.Name}}" }
}

resource "aws_route_table_association" "public" {
  count          = {{.AZCount}}
  subnet_id      = aws_subnet.public[count.index].id
  route_table_id = aws_route_table.public.id
}

resource "aws_route_table_association" "private" {
  count          = {{.AZCount}}
  subnet_id      = aws_subnet.private[count.index].id
  route_table_id = aws_route_table.private.id
}

locals {
  vpc_id     = aws_vpc.this.id
  subnet_ids = aws_subnet.private[*].id
}
{{- end}}

# --- Security groups (referenced by the controller and by launched compute) ---
resource "aws_security_group" "compute" {
  name        = "{{.Name}}-compute"
  description = "queuezero compute nodes: intra-cluster all-traffic (MPI/EFA), egress all"
  vpc_id      = local.vpc_id
  ingress {
    description = "intra-cluster (self)"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    self        = true
  }
  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
  tags = { Name = "{{.Name}}-compute", "q0:cluster" = "{{.Name}}" }
}

resource "aws_security_group" "controller" {
  name        = "{{.Name}}-controller"
  description = "queuezero slurmctld: slurmctld port from compute, SSH from admin, egress all"
  vpc_id      = local.vpc_id
  ingress {
    description     = "slurmctld from compute"
    from_port       = 6817
    to_port         = 6817
    protocol        = "tcp"
    security_groups = [aws_security_group.compute.id]
  }
  ingress {
    description = "SSH from admin"
    from_port   = 22
    to_port     = 22
    protocol    = "tcp"
    cidr_blocks = ["{{.AdminCIDR}}"]
  }
  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
  tags = { Name = "{{.Name}}-controller", "q0:cluster" = "{{.Name}}" }
}
`

// controllerTF renders the slurmctld pet — exactly one named, AMI-pinned EC2
// instance (no ASG, no count; ARCHITECTURE §9). Rendered only when a controller
// is requested (Controller.InstanceType set). The named standby is deferred.
const controllerTF = `# GENERATED by q0 apply — do not edit by hand.
{{- if .Cluster.Controller.InstanceType}}
resource "aws_iam_role" "controller" {
  name = "{{.ControllerRole}}"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "ec2.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

# The controller forks the resume/suspend programs, which describe/launch EC2.
resource "aws_iam_role_policy" "controller_ec2" {
  name = "{{.ControllerRole}}-ec2"
  role = aws_iam_role.controller.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow"
      Action = [
        "ec2:DescribeInstances",
        "ec2:RunInstances",
        "ec2:StartInstances",
        "ec2:StopInstances",
        "ec2:TerminateInstances",
        "ec2:CreateTags",
        "ec2:DescribeTags",
      ]
      Resource = "*"
    }]
  })
}

resource "aws_iam_instance_profile" "controller" {
  name = "{{.ControllerRole}}"
  role = aws_iam_role.controller.name
}

# The slurmctld controller: a named, AMI-pinned, stateful singleton — a pet, not
# an ASG of one (ARCHITECTURE §9). StandbyHost {{if .Cluster.Controller.StandbyHost}}({{.Cluster.Controller.StandbyHost}}) {{end}}is recorded but the
# second instance + failover wiring is a later sub-phase.
resource "aws_instance" "controller" {
  ami                    = "{{.Cluster.Controller.AMIHash}}"
  instance_type          = "{{.Cluster.Controller.InstanceType}}"
  subnet_id              = local.subnet_ids[0]
  vpc_security_group_ids = [aws_security_group.controller.id]
  iam_instance_profile   = aws_iam_instance_profile.controller.name
  tags = {
    Name          = "{{.Name}}-slurmctld"
    "q0:cluster"  = "{{.Name}}"
    "q0:role"     = "controller"
    {{- if .Cluster.Controller.StandbyHost}}
    "q0:standby"  = "{{.Cluster.Controller.StandbyHost}}"
    {{- end}}
  }
}
{{- else}}
# No controller requested in cluster.yaml (network-only bring-up).
{{- end}}
`

// storageTF renders shared EFS file systems + per-AZ mount targets + one NFS
// security group. The controller's StateDir lives here (durability, §9). The
// mount-on-boot (fstab/mount) is NOT here — it is the deferred S3 bootstrap
// script's job (§11); this only provisions the durable filesystem.
const storageTF = `# GENERATED by q0 apply — do not edit by hand.
{{- if .Storage}}
# --- NFS security group: EFS mount targets accept 2049 from compute+controller ---
resource "aws_security_group" "nfs" {
  name        = "{{.Name}}-nfs"
  description = "queuezero shared EFS: NFS 2049 from compute + controller, egress all"
  vpc_id      = local.vpc_id
  ingress {
    description     = "NFS from compute"
    from_port       = 2049
    to_port         = 2049
    protocol        = "tcp"
    security_groups = [aws_security_group.compute.id]
  }
  ingress {
    description     = "NFS from controller"
    from_port       = 2049
    to_port         = 2049
    protocol        = "tcp"
    security_groups = [aws_security_group.controller.id]
  }
  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
  tags = { Name = "{{.Name}}-nfs", "q0:cluster" = "{{.Name}}" }
}
{{- range .Storage}}

# --- Shared EFS for mount {{.MountPath}} (encrypted at rest) ---
resource "aws_efs_file_system" "efs_{{.Index}}" {
  creation_token = "{{.Token}}"
  encrypted      = true
  tags = { Name = "{{.Token}}", "q0:cluster" = "{{$.Name}}", "q0:mount" = "{{.MountPath}}" }
}

resource "aws_efs_mount_target" "efs_{{.Index}}" {
  count           = {{$.AZCount}}
  file_system_id  = aws_efs_file_system.efs_{{.Index}}.id
  subnet_id       = local.subnet_ids[count.index]
  security_groups = [aws_security_group.nfs.id]
}
{{- end}}
{{- else}}
# No shared storage declared in cluster.yaml.
{{- end}}
`

const variablesTF = `# GENERATED by q0 apply — do not edit by hand.
# The foundation layer takes no input variables; all values are rendered from
# cluster.yaml at generation time. Backend values are supplied via
# -backend-config at init.
`

const outputsTF = `# GENERATED by q0 apply — do not edit by hand.
output "node_instance_profile_arn" {
  value       = aws_iam_instance_profile.node.arn
  description = "Set Q0_INSTANCE_PROFILE_ARN to this for q0-resume."
}

output "scripts_bucket" {
  value       = aws_s3_bucket.scripts.id
  description = "Set Q0_SCRIPTS_BUCKET to this for q0 bootstrap push."
}
{{if .ManifestBucket}}
output "manifest_bucket" {
  value       = aws_s3_bucket.manifest.id
  description = "Set Q0_MANIFEST_BUCKET to this for collective resume."
}
{{end}}
output "vpc_id" {
  value       = local.vpc_id
  description = "The cluster VPC (generated or BYO)."
}

output "subnet_ids" {
  value       = local.subnet_ids
  description = "Private subnets compute + controller launch into."
}

output "controller_sg_id" {
  value       = aws_security_group.controller.id
  description = "Security group for the slurmctld controller."
}

output "compute_sg_id" {
  value       = aws_security_group.compute.id
  description = "Security group for launched compute nodes."
}
{{if .Cluster.Controller.InstanceType}}
output "controller_private_ip" {
  value       = aws_instance.controller.private_ip
  description = "The slurmctld controller's private IP (SlurmctldHost)."
}

output "controller_instance_id" {
  value       = aws_instance.controller.id
  description = "The slurmctld controller instance id."
}
{{end}}
{{range .Storage}}
output "efs_{{.Index}}_id" {
  value       = aws_efs_file_system.efs_{{.Index}}.id
  description = "EFS file system id for mount {{.MountPath}}."
}

output "efs_{{.Index}}_dns" {
  value       = aws_efs_file_system.efs_{{.Index}}.dns_name
  description = "EFS DNS name to mount {{.MountPath}} (used by the bootstrap script)."
}
{{end}}`
