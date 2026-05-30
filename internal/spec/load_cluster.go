package spec

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// LoadCluster reads and validates a cluster.yaml file from disk.
func LoadCluster(path string) (*Cluster, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("spec: read cluster %s: %w", path, err)
	}
	return ParseCluster(data)
}

// ParseCluster parses and validates cluster.yaml from bytes. Kept separate from
// LoadCluster so it is testable without touching disk.
func ParseCluster(data []byte) (*Cluster, error) {
	var c Cluster
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("spec: parse cluster: %w", err)
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// validate fails loud on the mandatory identity fields — a cluster with no name,
// control account, or region cannot produce a coherent static substrate.
func (c *Cluster) validate() error {
	if c.Name == "" {
		return errors.New("spec: cluster.yaml has empty name")
	}
	if c.ControlAccount == "" {
		return fmt.Errorf("spec: cluster %q has empty controlAccount", c.Name)
	}
	if c.Region == "" {
		return fmt.Errorf("spec: cluster %q has empty region", c.Name)
	}
	if err := c.Network.validate(c.Name); err != nil {
		return err
	}
	if err := c.validateStorage(); err != nil {
		return err
	}
	if err := c.Controller.validate(c.Name); err != nil {
		return err
	}
	return c.validateStateDir()
}

// validateStorage checks each storage entry and rejects duplicate mount paths.
// Kind is checked against the known set here; the fsx-lustre tuning fields
// (capacity, deployment type) are validated when present. Both efs and
// fsx-lustre are generatable.
func (c *Cluster) validateStorage() error {
	seen := make(map[string]struct{}, len(c.Storage))
	for i, s := range c.Storage {
		if s.MountPath == "" {
			return fmt.Errorf("spec: cluster %q storage[%d] has empty mountPath", c.Name, i)
		}
		switch s.Kind {
		case "efs", "fsx-lustre":
		default:
			return fmt.Errorf("spec: cluster %q storage[%d] has unknown kind %q (want efs|fsx-lustre)", c.Name, i, s.Kind)
		}
		if s.Kind == "fsx-lustre" {
			if err := validateFSxLustre(c.Name, i, s); err != nil {
				return err
			}
		}
		if _, dup := seen[s.MountPath]; dup {
			return fmt.Errorf("spec: cluster %q has duplicate storage mountPath %q", c.Name, s.MountPath)
		}
		seen[s.MountPath] = struct{}{}
	}
	return nil
}

// fsxDeploymentTypes is the set of FSx-Lustre deployment types AWS accepts.
var fsxDeploymentTypes = map[string]struct{}{
	"SCRATCH_1": {}, "SCRATCH_2": {}, "PERSISTENT_1": {}, "PERSISTENT_2": {},
}

// validateFSxLustre checks the optional fsx-lustre tuning fields. Empty fields
// are allowed (the generator defaults them); set fields must be well-formed.
func validateFSxLustre(cluster string, i int, s StorageSpec) error {
	if s.DeploymentType != "" {
		if _, ok := fsxDeploymentTypes[s.DeploymentType]; !ok {
			return fmt.Errorf("spec: cluster %q storage[%d] fsx-lustre deploymentType %q is not one of "+
				"SCRATCH_1|SCRATCH_2|PERSISTENT_1|PERSISTENT_2", cluster, i, s.DeploymentType)
		}
	}
	if s.CapacityGiB != 0 && (s.CapacityGiB < 0 || s.CapacityGiB%1200 != 0) {
		return fmt.Errorf("spec: cluster %q storage[%d] fsx-lustre capacityGiB %d must be a positive multiple of 1200",
			cluster, i, s.CapacityGiB)
	}
	return nil
}

// validateStateDir enforces the §9 durability invariant: the controller's Slurm
// save-state dir must live on declared shared storage, never on the controller's
// ephemeral instance disk. A controller with no StateDir is allowed (dev/degenerate).
func (c *Cluster) validateStateDir() error {
	if c.Controller.InstanceType == "" || c.Controller.StateDir == "" {
		return nil
	}
	for _, s := range c.Storage {
		if underMount(c.Controller.StateDir, s.MountPath) {
			return nil
		}
	}
	return fmt.Errorf("spec: cluster %q controller stateDir %q must live under a declared shared-storage mountPath (ARCHITECTURE §9)",
		c.Name, c.Controller.StateDir)
}

// underMount reports whether path is at or below mount (path-prefix on a
// path-segment boundary), so /shared/state is under /shared but /shared-x is not.
func underMount(path, mount string) bool {
	path = filepath.Clean(path)
	mount = filepath.Clean(mount)
	if path == mount {
		return true
	}
	return strings.HasPrefix(path, mount+string(filepath.Separator))
}

// validate checks the network spec: a bring-your-own network must name a VPC and
// at least one subnet; a generated network must carry a parseable CIDR.
func (n NetworkSpec) validate(cluster string) error {
	// The egress mode is validated regardless of BYO so a typo is caught early,
	// even though BYO networks bring their own routing and ignore it.
	switch n.Egress {
	case "", EgressNATGateway, EgressNATInstance, EgressEndpointsOnly:
	default:
		return fmt.Errorf("spec: cluster %q network.egress %q is not one of "+
			"nat-gateway|nat-instance|endpoints-only", cluster, n.Egress)
	}
	if n.BYO {
		if n.VPCID == "" {
			return fmt.Errorf("spec: cluster %q network.byo=true but vpcId is empty", cluster)
		}
		if len(n.SubnetIDs) == 0 {
			return fmt.Errorf("spec: cluster %q network.byo=true but subnetIds is empty", cluster)
		}
		return nil
	}
	if n.CIDR == "" {
		return fmt.Errorf("spec: cluster %q network is generated (byo=false) but cidr is empty", cluster)
	}
	if _, _, err := net.ParseCIDR(n.CIDR); err != nil {
		return fmt.Errorf("spec: cluster %q network.cidr %q is not a valid CIDR: %w", cluster, n.CIDR, err)
	}
	return nil
}

// validate checks the controller spec. An all-empty ControllerSpec means "no
// controller this apply" (allowed during network-only bring-up); but if a
// controller is requested at all, it must have an instance type and an
// AMI-pinned image — a pet cannot launch without them (ARCHITECTURE §9).
func (c ControllerSpec) validate(cluster string) error {
	if c == (ControllerSpec{}) {
		return nil // no controller requested
	}
	if c.InstanceType == "" {
		return fmt.Errorf("spec: cluster %q controller requested but instanceType is empty", cluster)
	}
	if c.AMIHash == "" {
		return fmt.Errorf("spec: cluster %q controller requested but amiHash is empty (the controller must be AMI-pinned)", cluster)
	}
	return nil
}

// ContentHash is the deterministic content address of this cluster layer —
// "q0-" + the first 16 bytes of SHA-256 over its canonical YAML, as lowercase
// hex. Each spec file is a composable layer with its own hash (ARCHITECTURE §2);
// this is that hash, surfaced for legibility and layer composition. Re-marshaling
// through yaml.v3 canonicalizes key order, so the same logical cluster always
// hashes the same regardless of source formatting.
func (c *Cluster) ContentHash() (string, error) {
	canon, err := yaml.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("spec: canonicalize cluster: %w", err)
	}
	h := sha256.Sum256(canon)
	return "q0-" + hex.EncodeToString(h[:16]), nil
}
