package spec

import (
	"strings"
	"testing"
)

const goodCluster = `
name: gauss
controlAccount: "111122223333"
region: us-east-1
network:
  byo: true
  vpcId: vpc-abc
  subnetIds: [subnet-1, subnet-2]
controller:
  instanceType: m7i.2xlarge
  standbyHost: gauss-ctl-2
  stateDir: /shared/slurm/state
  accountingDb: gauss.rds.amazonaws.com
  amiHash: ami-deadbeef
storage:
  - kind: fsx-lustre
    mountPath: /scratch
`

func TestParseCluster_Good(t *testing.T) {
	c, err := ParseCluster([]byte(goodCluster))
	if err != nil {
		t.Fatalf("ParseCluster: %v", err)
	}
	if c.Name != "gauss" || c.ControlAccount != "111122223333" || c.Region != "us-east-1" {
		t.Errorf("identity not parsed: %+v", c)
	}
	if !c.Network.BYO || len(c.Network.SubnetIDs) != 2 {
		t.Errorf("network not parsed: %+v", c.Network)
	}
	if c.Controller.AMIHash != "ami-deadbeef" || len(c.Storage) != 1 {
		t.Errorf("controller/storage not parsed: %+v", c)
	}
}

func TestParseCluster_MissingFields(t *testing.T) {
	cases := map[string]string{
		"no name":    "controlAccount: \"1\"\nregion: us-east-1\n",
		"no account": "name: g\nregion: us-east-1\n",
		"no region":  "name: g\ncontrolAccount: \"1\"\n",
	}
	for name, y := range cases {
		if _, err := ParseCluster([]byte(y)); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

// A generated network with a valid CIDR and no controller (network-only bring-up).
const generatedCluster = `
name: gauss
controlAccount: "111122223333"
region: us-east-1
network:
  byo: false
  cidr: 10.0.0.0/16
`

func TestParseCluster_GeneratedNetwork_NoController_OK(t *testing.T) {
	c, err := ParseCluster([]byte(generatedCluster))
	if err != nil {
		t.Fatalf("generated network without controller should be valid: %v", err)
	}
	if c.Network.BYO || c.Network.CIDR != "10.0.0.0/16" {
		t.Errorf("network not parsed: %+v", c.Network)
	}
}

func TestParseCluster_NetworkControllerValidation(t *testing.T) {
	base := "name: g\ncontrolAccount: \"1\"\nregion: us-east-1\n"
	cases := map[string]string{
		"byo without vpc":      base + "network:\n  byo: true\n  subnetIds: [s-1]\n",
		"byo without subnets":  base + "network:\n  byo: true\n  vpcId: vpc-1\n",
		"generated without cidr": base + "network:\n  byo: false\n",
		"generated bad cidr":   base + "network:\n  byo: false\n  cidr: not-a-cidr\n",
		"controller no instancetype": base + "network:\n  byo: false\n  cidr: 10.0.0.0/16\ncontroller:\n  amiHash: ami-1\n",
		"controller no ami":    base + "network:\n  byo: false\n  cidr: 10.0.0.0/16\ncontroller:\n  instanceType: m7i.large\n",
	}
	for name, y := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseCluster([]byte(y)); err == nil {
				t.Errorf("expected validation error for %q", name)
			}
		})
	}
}

func TestCluster_ContentHash_StableAndShaped(t *testing.T) {
	c, _ := ParseCluster([]byte(goodCluster))
	h1, err := c.ContentHash()
	if err != nil {
		t.Fatal(err)
	}
	h2, _ := c.ContentHash()
	if h1 != h2 {
		t.Errorf("hash not stable: %s vs %s", h1, h2)
	}
	if !strings.HasPrefix(h1, "q0-") || len(h1) != len("q0-")+32 {
		t.Errorf("hash shape wrong: %q", h1)
	}
	// A different cluster hashes differently.
	c2, _ := ParseCluster([]byte(goodCluster))
	c2.Region = "us-west-2"
	h3, _ := c2.ContentHash()
	if h3 == h1 {
		t.Error("different clusters should hash differently")
	}
}
