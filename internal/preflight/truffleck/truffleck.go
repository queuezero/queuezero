// Package truffleck backs q0 preflight's cloud checks with the spore.host
// truffle library (ARCHITECTURE §13: preflight is "largely a truffle-backed
// command"). It keeps the truffle import OUT of internal/preflight, so the
// preflight core stays provider-agnostic and fake-testable — truffle lives only
// here, behind the preflight.Checker / preflight.QuotaChecker interfaces.
//
// This is queuezero's first spore.host library dependency. It is the safe
// direction of the planned convergence (ARCHITECTURE §15): q0 depends on a
// spore.host leaf library, while internal/cohort — the part of q0 that will
// itself graduate into the spore.host monorepo — stays dependency-free
// (guard-cohort enforced).
package truffleck

import (
	"context"
	"fmt"
	"regexp"
	"sync"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	truffleaws "github.com/spore-host/truffle/pkg/aws"
	trufflequotas "github.com/spore-host/truffle/pkg/quotas"

	"github.com/queuezero/queuezero/internal/preflight"
	"github.com/queuezero/queuezero/internal/substrate"
	awssub "github.com/queuezero/queuezero/internal/substrate/aws"
)

// Checker satisfies preflight.Checker (the four EC2 reads, delegated to the q0
// substrate client) AND preflight.QuotaChecker (Service Quotas, via truffle).
type Checker struct {
	*awssub.Client // embeds the EC2-read Checker (InstanceTypeOffered/ImageExists/SubnetExists/AvailabilityZones)

	taws   *truffleaws.Client
	tq     *trufflequotas.Client
	region string

	mu       sync.Mutex
	quotas   *trufflequotas.QuotaInfo // cached: one GetQuotas per region serves all rungs
	vcpuByIT map[string]int32         // cached: instance type -> vCPU count
}

// New builds a truffle-backed checker over a shared AWS config + region. The
// embedded q0 substrate client handles the EC2 reads (offerings/AMI/subnet/AZ);
// truffle handles the Service-Quota verdict.
func New(cfg awssdk.Config, region string) *Checker {
	return &Checker{
		Client:   awssub.NewClient(ec2.NewFromConfig(cfg), substrate.NewLimiter(substrate.LimiterConfig{}, nil)),
		taws:     truffleaws.NewClientFromConfig(cfg),
		tq:       trufflequotas.NewClientFromConfig(cfg),
		region:   region,
		vcpuByIT: map[string]int32{},
	}
}

// ensure Checker satisfies both preflight seams.
var (
	_ preflight.Checker      = (*Checker)(nil)
	_ preflight.QuotaChecker = (*Checker)(nil)
)

// ServiceQuotaOK reports whether the account's Service Quota admits instanceType
// under its purchase model. q0 holds no L-code table or vCPU math — truffle owns
// the instance-type→vCPU and family→quota-code mappings.
//
// Reserved-capacity rungs are not bound by the on-demand/spot vCPU quotas (they
// run against a reservation), so they short-circuit to OK.
func (c *Checker) ServiceQuotaOK(ctx context.Context, instanceType, az, capacityModel string) (bool, string, error) {
	if capacityModel == "reserved" {
		return true, "reserved capacity: not vCPU-quota-bound", nil
	}

	info, err := c.getQuotas(ctx)
	if err != nil {
		return false, "", fmt.Errorf("truffleck: get quotas (region %s): %w", c.region, err)
	}
	vcpus, err := c.vcpus(ctx, instanceType)
	if err != nil {
		return false, "", fmt.Errorf("truffleck: vcpu lookup %s: %w", instanceType, err)
	}
	ok, detail := verdict(c.tq, instanceType, vcpus, capacityModel, info)
	return ok, detail, nil
}

// verdict maps a rung's purchase model to truffle's spot/on-demand quota check
// and returns a human-readable detail. Pure (no I/O) so the model→flag mapping
// is unit-testable without AWS. truffle's CanLaunch returns an empty reason on
// success; we substitute a positive detail so the preflight Result is legible.
func verdict(q *trufflequotas.Client, instanceType string, vcpus int32, capacityModel string, info *trufflequotas.QuotaInfo) (bool, string) {
	spot := capacityModel == "spot"
	ok, reason := q.CanLaunch(instanceType, vcpus, info, spot)
	if ok {
		model := "on-demand"
		if spot {
			model = "spot"
		}
		return true, fmt.Sprintf("%d vCPU within %s quota for %s", vcpus, model, trufflequotas.GetQuotaFamily(instanceType))
	}
	return false, reason
}

// getQuotas fetches (and caches) the region's quota snapshot once.
func (c *Checker) getQuotas(ctx context.Context) (*trufflequotas.QuotaInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.quotas != nil {
		return c.quotas, nil
	}
	info, err := c.tq.GetQuotas(ctx, c.region)
	if err != nil {
		return nil, err
	}
	c.quotas = info
	return info, nil
}

// vcpus resolves (and caches) an instance type's authoritative vCPU count via
// truffle's DescribeInstanceTypes-backed search.
func (c *Checker) vcpus(ctx context.Context, instanceType string) (int32, error) {
	c.mu.Lock()
	if v, ok := c.vcpuByIT[instanceType]; ok {
		c.mu.Unlock()
		return v, nil
	}
	c.mu.Unlock()

	matcher := regexp.MustCompile("^" + regexp.QuoteMeta(instanceType) + "$")
	results, err := c.taws.SearchInstanceTypes(ctx, []string{c.region}, matcher, truffleaws.FilterOptions{})
	if err != nil {
		return 0, err
	}
	if len(results) == 0 {
		return 0, fmt.Errorf("instance type %q not found in region %s", instanceType, c.region)
	}
	v := results[0].VCPUs
	c.mu.Lock()
	c.vcpuByIT[instanceType] = v
	c.mu.Unlock()
	return v, nil
}
