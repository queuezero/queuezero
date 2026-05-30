// Package preflight runs read-only, no-mutation checks of a cluster + partitions
// spec against the target cloud — the "fail in 10 seconds instead of 20 minutes"
// pre-check (ARCHITECTURE §13). It validates that what the spec asks for actually
// exists/is offered before `q0 apply` or a resume wastes 20 minutes discovering
// otherwise.
//
// It is provider-agnostic: all cloud questions go through the Checker interface,
// so this package imports only internal/spec + stdlib (the AWS implementation
// lives behind Checker, and a truffle-backed impl can replace it later). That
// keeps Run unit-testable with a fake Checker — no AWS.
package preflight

import (
	"context"
	"fmt"

	"github.com/queuezero/queuezero/internal/spec"
)

// Checker answers read-only capability questions about the target cloud. The
// production implementation is the AWS substrate client (direct SDK); a
// truffle-backed implementation can replace it later behind this same interface.
type Checker interface {
	// InstanceTypeOffered reports whether instanceType can be launched in az.
	InstanceTypeOffered(ctx context.Context, instanceType, az string) (bool, error)
	// ImageExists reports whether an AMI id resolves in the target region.
	ImageExists(ctx context.Context, ami string) (bool, error)
	// SubnetExists reports whether a subnet id resolves in the target region.
	SubnetExists(ctx context.Context, subnetID string) (bool, error)
	// AvailabilityZones lists the available AZ names in the target region.
	AvailabilityZones(ctx context.Context) ([]string, error)
}

// Result is one check's verdict, in the decision+reason style used elsewhere
// (cf. slurm.SweepDecision). Detail is human-readable and surfaced by the CLI.
type Result struct {
	Check  string // e.g. "instance-type-offered", "ami-exists", "subnet-exists", "az-valid"
	Target string // what was checked, e.g. "p5.48xlarge@us-east-1a"
	OK     bool
	Detail string
}

// Report is the full set of check results from one preflight run.
type Report struct {
	Results []Result
}

// OK reports whether every check passed.
func (r Report) OK() bool {
	for _, res := range r.Results {
		if !res.OK {
			return false
		}
	}
	return true
}

// Run validates a cluster (and optional partitions) against the Checker and
// returns a Report with one Result per check. It collects ALL failures — it
// never bails on the first — so the operator sees everything wrong at once. A
// returned error means the checks could not be performed (e.g. the AZ listing
// failed), distinct from a check that ran and failed.
func Run(ctx context.Context, cl *spec.Cluster, parts *spec.Partitions, c Checker) (Report, error) {
	var rep Report
	if cl == nil {
		return rep, fmt.Errorf("preflight: nil cluster")
	}

	// Resolve the region's AZs once — used to validate every rung's AvailZone and
	// as a prerequisite (a bad region/credentials surfaces here).
	zones, err := c.AvailabilityZones(ctx)
	if err != nil {
		return rep, fmt.Errorf("preflight: list availability zones (region %q): %w", cl.Region, err)
	}
	zoneSet := make(map[string]struct{}, len(zones))
	for _, z := range zones {
		zoneSet[z] = struct{}{}
	}

	// Controller AMI (if a controller is requested).
	if cl.Controller.AMIHash != "" {
		ok, err := c.ImageExists(ctx, cl.Controller.AMIHash)
		if err != nil {
			return rep, fmt.Errorf("preflight: describe image %q: %w", cl.Controller.AMIHash, err)
		}
		rep.Results = append(rep.Results, Result{
			Check: "ami-exists", Target: cl.Controller.AMIHash, OK: ok,
			Detail: okText(ok, "controller AMI found", "controller AMI not found in region"),
		})
	}

	// BYO subnets must exist.
	if cl.Network.BYO {
		for _, sn := range cl.Network.SubnetIDs {
			ok, err := c.SubnetExists(ctx, sn)
			if err != nil {
				return rep, fmt.Errorf("preflight: describe subnet %q: %w", sn, err)
			}
			rep.Results = append(rep.Results, Result{
				Check: "subnet-exists", Target: sn, OK: ok,
				Detail: okText(ok, "subnet found", "subnet not found"),
			})
		}
	}

	// Every partition fallback rung: AZ valid + instance type offered there.
	if parts != nil {
		for _, p := range parts.Partitions {
			for _, r := range p.FallbackChain {
				target := fmt.Sprintf("%s@%s", r.InstanceType, r.AvailZone)

				if _, azOK := zoneSet[r.AvailZone]; !azOK {
					rep.Results = append(rep.Results, Result{
						Check: "az-valid", Target: r.AvailZone, OK: false,
						Detail: fmt.Sprintf("partition %q: availability zone not in region %q", p.Name, cl.Region),
					})
					continue // can't ask about offerings in a non-existent AZ
				}

				ok, err := c.InstanceTypeOffered(ctx, r.InstanceType, r.AvailZone)
				if err != nil {
					return rep, fmt.Errorf("preflight: instance-type offerings %s: %w", target, err)
				}
				rep.Results = append(rep.Results, Result{
					Check: "instance-type-offered", Target: target, OK: ok,
					Detail: okText(ok,
						fmt.Sprintf("partition %q: offered", p.Name),
						fmt.Sprintf("partition %q: instance type not offered in this AZ", p.Name)),
				})
			}
		}
	}

	return rep, nil
}

func okText(ok bool, yes, no string) string {
	if ok {
		return yes
	}
	return no
}
