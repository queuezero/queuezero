package main

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
)

// regionGetter is the slice of the IMDS client used to discover the node's
// region. *imds.Client satisfies it; tests inject a fake. IMDS is a link-local
// endpoint (169.254.169.254), so it needs no region itself — which is exactly
// why it can answer "what region am I in?" before any region is configured.
type regionGetter interface {
	GetRegion(ctx context.Context, params *imds.GetRegionInput, optFns ...func(*imds.Options)) (*imds.GetRegionOutput, error)
}

// resolveRegion returns the region q0-spored should use: the explicit env value
// when set, otherwise the node's own region from IMDS. The env wins so an
// operator can override; the IMDS fallback means a node never needs the region
// delivered to it (the q0 control channel carries config, not the obvious
// self-knowledge IMDS already provides). Pure except for the injected getter, so
// the precedence + fallback logic is unit-testable.
func resolveRegion(ctx context.Context, envRegion string, imds regionGetter) (string, error) {
	if envRegion != "" {
		return envRegion, nil
	}
	out, err := imds.GetRegion(ctx, &imdsGetRegionInput)
	if err != nil {
		return "", fmt.Errorf("region: Q0_REGION unset and IMDS lookup failed: %w", err)
	}
	if out.Region == "" {
		return "", fmt.Errorf("region: Q0_REGION unset and IMDS returned an empty region")
	}
	return out.Region, nil
}

// imdsGetRegionInput is a shared empty input (GetRegionInput has no fields).
var imdsGetRegionInput imds.GetRegionInput
