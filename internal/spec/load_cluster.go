package spec

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"

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
	return c.Controller.validate(c.Name)
}

// validate checks the network spec: a bring-your-own network must name a VPC and
// at least one subnet; a generated network must carry a parseable CIDR.
func (n NetworkSpec) validate(cluster string) error {
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
