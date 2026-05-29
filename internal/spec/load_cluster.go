package spec

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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
