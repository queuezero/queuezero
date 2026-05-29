package spec

import (
	"testing"

	"github.com/queuezero/queuezero/internal/cohort"
)

const goodYAML = `
stackHash: abc123
partitions:
  - name: gpu
    executionAccount: "111122223333"
    maxNodes: 64
    collective: true
    fallbackChain:
      - instanceType: p5.48xlarge
        availZone: us-east-1a
        capacityModel: ondemand
      - instanceType: p5.48xlarge
        availZone: us-east-1b
        capacityModel: spot
  - name: serial
    nodePrefix: cpu
    executionAccount: "111122223333"
    maxNodes: 100
    fallbackChain:
      - instanceType: m7i.xlarge
        availZone: us-east-1a
        capacityModel: ondemand
`

func TestParsePartitions_Good(t *testing.T) {
	p, err := ParsePartitions([]byte(goodYAML))
	if err != nil {
		t.Fatalf("ParsePartitions: %v", err)
	}
	if len(p.Partitions) != 2 {
		t.Fatalf("got %d partitions, want 2", len(p.Partitions))
	}
	idx := p.Index()
	gpu, ok := idx["gpu"]
	if !ok {
		t.Fatal("gpu partition not indexed")
	}
	if !gpu.Collective {
		t.Error("gpu should be collective")
	}

	chain, err := gpu.CohortFallbackChain()
	if err != nil {
		t.Fatalf("CohortFallbackChain: %v", err)
	}
	if len(chain) != 2 {
		t.Fatalf("chain len %d want 2", len(chain))
	}
	if chain[0].CapacityModel != cohort.CapacityOnDemand || chain[1].CapacityModel != cohort.CapacitySpot {
		t.Errorf("capacity models wrong: %v %v", chain[0].CapacityModel, chain[1].CapacityModel)
	}
	if chain[0].AccountID != "111122223333" {
		t.Errorf("AccountID not stamped: %q", chain[0].AccountID)
	}
}

func TestCohortBudget_NilIsZero(t *testing.T) {
	p, _ := ParsePartitions([]byte(goodYAML))
	b := p.Index()["gpu"].CohortBudget()
	if b != (cohort.PhaseBudget{}) {
		t.Errorf("nil budget should be zero PhaseBudget, got %+v", b)
	}
}

func TestResolveForNode(t *testing.T) {
	p, _ := ParsePartitions([]byte(goodYAML))
	idx := p.Index()

	// gpu has no NodePrefix -> matches Name "gpu".
	if part, ok := idx.ResolveForNode("gpu-042"); !ok || part.Name != "gpu" {
		t.Errorf("gpu-042 should resolve to gpu, got %q ok=%v", part.Name, ok)
	}
	// serial has NodePrefix "cpu".
	if part, ok := idx.ResolveForNode("cpu-001"); !ok || part.Name != "serial" {
		t.Errorf("cpu-001 should resolve to serial, got %q ok=%v", part.Name, ok)
	}
	if _, ok := idx.ResolveForNode("zzz-9"); ok {
		t.Error("zzz-9 should not resolve")
	}
}

func TestParsePartitions_Errors(t *testing.T) {
	cases := map[string]string{
		"no partitions": `stackHash: x`,
		"empty name": `
partitions:
  - executionAccount: "1"
    fallbackChain:
      - instanceType: m7i.xlarge
        capacityModel: ondemand
`,
		"empty chain": `
partitions:
  - name: p1
    fallbackChain: []
`,
		"empty instanceType": `
partitions:
  - name: p1
    fallbackChain:
      - capacityModel: ondemand
`,
		"bad capacity model": `
partitions:
  - name: p1
    fallbackChain:
      - instanceType: m7i.xlarge
        capacityModel: bananas
`,
		"duplicate name": `
partitions:
  - name: dup
    fallbackChain:
      - instanceType: m7i.xlarge
        capacityModel: ondemand
  - name: dup
    fallbackChain:
      - instanceType: m7i.xlarge
        capacityModel: spot
`,
	}
	for name, y := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParsePartitions([]byte(y)); err == nil {
				t.Errorf("expected error for %q, got nil", name)
			}
		})
	}
}

func TestCapacityModelFromString(t *testing.T) {
	ok := map[string]cohort.CapacityModel{
		"ondemand": cohort.CapacityOnDemand, "on-demand": cohort.CapacityOnDemand,
		"": cohort.CapacityOnDemand, "SPOT": cohort.CapacitySpot,
		"reserved": cohort.CapacityReserved, "odcr": cohort.CapacityReserved,
		"capacity-block": cohort.CapacityReserved,
	}
	for in, want := range ok {
		got, err := capacityModelFromString(in)
		if err != nil || got != want {
			t.Errorf("capacityModelFromString(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := capacityModelFromString("nonsense"); err == nil {
		t.Error("nonsense should error")
	}
}

func TestLeadingPrefix(t *testing.T) {
	for in, want := range map[string]string{
		"gpu-042": "gpu", "aws-cpu-001": "aws-cpu", "node": "node", "x1": "x",
	} {
		if got := leadingPrefix(in); got != want {
			t.Errorf("leadingPrefix(%q)=%q want %q", in, got, want)
		}
	}
}
