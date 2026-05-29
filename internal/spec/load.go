package spec

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/queuezero/queuezero/internal/cohort"
)

// LoadPartitions reads and validates a partitions.yaml file from disk.
func LoadPartitions(path string) (*Partitions, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("spec: read partitions %s: %w", path, err)
	}
	return ParsePartitions(data)
}

// ParsePartitions parses and validates partitions.yaml from bytes. Kept
// separate from LoadPartitions so it is testable without touching disk.
func ParsePartitions(data []byte) (*Partitions, error) {
	var p Partitions
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("spec: parse partitions: %w", err)
	}
	if err := p.validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

// validate fails loud on anything that would reach a launch as garbage —
// mirroring cohort.NewEntityIntent's construction-time validation. The operator
// approves the fallback chain by committing this file; a malformed file must
// never silently degrade into a bad launch.
func (p *Partitions) validate() error {
	if len(p.Partitions) == 0 {
		return errors.New("spec: partitions.yaml has no partitions")
	}
	seen := make(map[string]struct{}, len(p.Partitions))
	for i, part := range p.Partitions {
		if part.Name == "" {
			return fmt.Errorf("spec: partition[%d] has empty name", i)
		}
		if _, dup := seen[part.Name]; dup {
			return fmt.Errorf("spec: duplicate partition name %q", part.Name)
		}
		seen[part.Name] = struct{}{}
		if len(part.FallbackChain) == 0 {
			return fmt.Errorf("spec: partition %q has empty fallbackChain", part.Name)
		}
		for j, r := range part.FallbackChain {
			if r.InstanceType == "" {
				return fmt.Errorf("spec: partition %q rung[%d] has empty instanceType", part.Name, j)
			}
			if _, err := capacityModelFromString(r.CapacityModel); err != nil {
				return fmt.Errorf("spec: partition %q rung[%d]: %w", part.Name, j, err)
			}
		}
	}
	return nil
}

// PartitionIndex maps partition name -> Partition for O(1) lookup.
type PartitionIndex map[string]Partition

// Index builds a name -> Partition index.
func (p *Partitions) Index() PartitionIndex {
	idx := make(PartitionIndex, len(p.Partitions))
	for _, part := range p.Partitions {
		idx[part.Name] = part
	}
	return idx
}

// ResolveForNode picks the partition a bare node name belongs to when the
// caller could not pass the partition explicitly. It matches the node name's
// leading non-numeric segment against each partition's NodePrefix (or Name when
// NodePrefix is empty). Prefer passing the partition explicitly; this is the
// fallback. Returns false if no partition matches.
func (idx PartitionIndex) ResolveForNode(node string) (Partition, bool) {
	prefix := leadingPrefix(node)
	// Exact prefix match first.
	for _, part := range idx {
		want := part.NodePrefix
		if want == "" {
			want = part.Name
		}
		if want == prefix || strings.HasPrefix(node, want) {
			return part, true
		}
	}
	return Partition{}, false
}

// leadingPrefix returns the leading run of the node name up to the first digit
// or separator, trimming a trailing '-'. e.g. "gpu-042" -> "gpu", "aws-cpu-001"
// -> "aws-cpu".
func leadingPrefix(node string) string {
	i := strings.IndexFunc(node, func(r rune) bool { return r >= '0' && r <= '9' })
	if i < 0 {
		return node
	}
	return strings.TrimRight(node[:i], "-")
}

// CohortFallbackChain converts a partition's []spec.Rung to []cohort.Rung,
// stamping AccountID = ExecutionAccount on every rung (multi-account, §3) so the
// reconciler knows which execution account each rung launches into.
func (part Partition) CohortFallbackChain() ([]cohort.Rung, error) {
	chain := make([]cohort.Rung, 0, len(part.FallbackChain))
	for j, r := range part.FallbackChain {
		cm, err := capacityModelFromString(r.CapacityModel)
		if err != nil {
			return nil, fmt.Errorf("spec: partition %q rung[%d]: %w", part.Name, j, err)
		}
		chain = append(chain, cohort.Rung{
			InstanceType:  r.InstanceType,
			AvailZone:     r.AvailZone,
			CapacityModel: cm,
			AccountID:     part.ExecutionAccount,
		})
	}
	return chain, nil
}

// CohortBudget converts an optional *BudgetSpec to a cohort.PhaseBudget. A nil
// budget returns the zero PhaseBudget, which the cohort constructors fill
// field-by-field from DefaultBudget(); a zero individual field does the same.
func (part Partition) CohortBudget() cohort.PhaseBudget {
	if part.Budget == nil {
		return cohort.PhaseBudget{}
	}
	return cohort.PhaseBudget{
		LaunchAcked:    part.Budget.LaunchAcked,
		Running:        part.Budget.Running,
		Enrolled:       part.Budget.Enrolled,
		CohortBarrier:  part.Budget.CohortBarrier,
		CohortAssembly: part.Budget.CohortAssembly,
	}
}

// capacityModelFromString maps the YAML capacity-model string to the cohort
// enum. There is no safe default — an unknown value is an error, not silently
// on-demand, because the operator's approved intent must be honored exactly.
func capacityModelFromString(s string) (cohort.CapacityModel, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "ondemand", "on-demand", "":
		// Empty defaults to on-demand: the conventional baseline rung.
		return cohort.CapacityOnDemand, nil
	case "spot":
		return cohort.CapacitySpot, nil
	case "reserved", "odcr", "capacity-block":
		return cohort.CapacityReserved, nil
	default:
		return 0, fmt.Errorf("unknown capacityModel %q (want ondemand|spot|reserved)", s)
	}
}
