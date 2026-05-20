package aws

import (
	"context"
	"fmt"
	"strings"
	"time"

	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"

	"github.com/queuezero/queuezero/internal/cohort"
	"github.com/queuezero/queuezero/internal/substrate"
)

// Retry bound tuning knobs. Raise if operational data shows these are too tight;
// lower if the phase-1 deadline is being blown by internal retries.
const (
	maxAmbiguousRetries = 3 // TUNING KNOB: re-issues of the same idempotency-tokened call
	maxThrottleRetries  = 5 // TUNING KNOB: attempts after Throttle before surfacing the fault
)

// throttleBackoff is the substrate's own BackoffPolicy for the throttle retry
// loop. Longer cap than cohort's reconciler policy: throttle recovery takes
// longer than consistency lag.
var throttleBackoff = cohort.BackoffPolicy{
	Base:   500 * time.Millisecond,
	Cap:    60 * time.Second,
	Jitter: 0.2,
}

// Client is the single chokepoint to AWS EC2 for queuezero. It owns:
//   - idempotency tokens on every mutation (via substrate.Token)
//   - rate limiting (via substrate.LimiterIface)
//   - fault classification (via aws.Classifier)
//   - two bounded retry loops: Throttle and Ambiguous
//
// What it surfaces to callers: success, FaultRetryableConsistency,
// FaultCapacityExhausted, FaultTerminal. It NEVER surfaces FaultThrottle or
// FaultAmbiguous — those are consumed here.
type Client struct {
	ec2     EC2API
	limiter substrate.LimiterIface
	clf     Classifier
}

// NewClient constructs a Client. ec2api is typically *ec2.Client from
// aws-sdk-go-v2; tests inject a fake.
func NewClient(ec2api EC2API, limiter substrate.LimiterIface) *Client {
	return &Client{ec2: ec2api, limiter: limiter}
}

// RunInstance launches exactly one instance. The idempotency token in req is
// passed as ClientToken to RunInstances; re-issuing after an Ambiguous fault
// is safe and will return the already-created instance rather than creating
// a new one.
func (c *Client) RunInstance(ctx context.Context, req substrate.RunRequest) (substrate.Instance, error) {
	// Build the EC2 input once; the same object is re-used on Ambiguous retry
	// so the ClientToken is stable across all attempts (B2, B3).
	input := runRequestToEC2(req)

	var result *ec2.RunInstancesOutput
	err := c.callWithRetry(ctx, func(ctx context.Context) error {
		if err := c.limiter.Acquire(ctx); err != nil {
			return err
		}
		var callErr error
		result, callErr = c.ec2.RunInstances(ctx, input)
		return callErr
	})
	if err != nil {
		return substrate.Instance{}, err
	}
	if len(result.Instances) == 0 {
		return substrate.Instance{}, faultErr(cohort.Fault{
			Class:   cohort.FaultTerminal,
			Code:    "EmptyRunInstancesResponse",
			Message: "RunInstances returned no instances",
		})
	}
	return instanceFromEC2(result.Instances[0]), nil
}

// StartInstance resumes a stopped or hibernated instance. Idempotent on
// instance ID; no creation ClientToken needed (B2).
func (c *Client) StartInstance(ctx context.Context, id string) (substrate.Instance, error) {
	input := &ec2.StartInstancesInput{InstanceIds: []string{id}}

	var result *ec2.StartInstancesOutput
	err := c.callWithRetry(ctx, func(ctx context.Context) error {
		if err := c.limiter.Acquire(ctx); err != nil {
			return err
		}
		var callErr error
		result, callErr = c.ec2.StartInstances(ctx, input)
		return callErr
	})
	if err != nil {
		return substrate.Instance{}, err
	}
	if len(result.StartingInstances) == 0 {
		return substrate.Instance{}, faultErr(cohort.Fault{
			Class:   cohort.FaultTerminal,
			Code:    "EmptyStartInstancesResponse",
			Message: "StartInstances returned no state changes",
		})
	}
	change := result.StartingInstances[0]
	return substrate.Instance{
		ProviderID: aws.ToString(change.InstanceId),
		State:      string(change.CurrentState.Name),
	}, nil
}

// StopInstance stops or hibernates an instance.
func (c *Client) StopInstance(ctx context.Context, id string, hibernate bool) error {
	input := &ec2.StopInstancesInput{
		InstanceIds: []string{id},
		Hibernate:   aws.Bool(hibernate),
	}
	return c.callWithRetry(ctx, func(ctx context.Context) error {
		if err := c.limiter.Acquire(ctx); err != nil {
			return err
		}
		_, callErr := c.ec2.StopInstances(ctx, input)
		return callErr
	})
}

// TerminateInstance destroys an instance. Idempotent: already-terminated
// instances return success.
func (c *Client) TerminateInstance(ctx context.Context, id string) error {
	input := &ec2.TerminateInstancesInput{InstanceIds: []string{id}}
	return c.callWithRetry(ctx, func(ctx context.Context) error {
		if err := c.limiter.Acquire(ctx); err != nil {
			return err
		}
		_, callErr := c.ec2.TerminateInstances(ctx, input)
		return callErr
	})
}

// DescribeByTag returns instances matching all provided tag key=value pairs.
// Results are ADVISORY and eventually consistent (B5): a miss on a freshly
// launched instance is StateUnknown, not StateAbsent. The idempotency token
// in RunInstance is the authority on whether an instance was created.
func (c *Client) DescribeByTag(ctx context.Context, tags map[string]string) ([]substrate.Instance, error) {
	filters := make([]ec2types.Filter, 0, len(tags))
	for k, v := range tags {
		k, v := k, v
		filters = append(filters, ec2types.Filter{
			Name:   aws.String("tag:" + k),
			Values: []string{v},
		})
	}
	input := &ec2.DescribeInstancesInput{Filters: filters}

	if err := c.limiter.Acquire(ctx); err != nil {
		return nil, err
	}
	out, err := c.ec2.DescribeInstances(ctx, input)
	if err != nil {
		f := c.clf.Classify(err)
		return nil, faultErr(f)
	}
	var instances []substrate.Instance
	for _, r := range out.Reservations {
		for _, i := range r.Instances {
			instances = append(instances, instanceFromEC2(i))
		}
	}
	return instances, nil
}

// callWithRetry runs fn, consuming FaultThrottle (up to maxThrottleRetries)
// and FaultAmbiguous (up to maxAmbiguousRetries) internally. The same fn
// closure is re-invoked on Ambiguous retry — because fn uses the same
// pre-built input with the same ClientToken, idempotency is preserved (B3).
//
// Surfaces: nil (success), or a faultErr wrapping Consistency, Capacity, or Terminal.
// NEVER surfaces Throttle or Ambiguous to the caller (B4).
func (c *Client) callWithRetry(ctx context.Context, fn func(context.Context) error) error {
	throttleAttempt := 0
	ambiguousAttempt := 0

	for {
		err := fn(ctx)
		if err == nil {
			return nil
		}

		// Classify — context.Canceled classifies Terminal, short-circuits immediately.
		f := c.clf.Classify(err)

		switch f.Class {
		case cohort.FaultThrottle:
			if throttleAttempt >= maxThrottleRetries {
				return faultErr(f)
			}
			d := throttleBackoff.Duration(throttleAttempt)
			c.limiter.Backoff(d)
			throttleAttempt++
			// Reset ambiguous counter: a throttle response confirms the call landed.
			ambiguousAttempt = 0

		case cohort.FaultAmbiguous:
			// Re-issue the SAME call with the SAME idempotency token.
			// If the original call landed, the provider returns the existing resource.
			if ambiguousAttempt >= maxAmbiguousRetries {
				// Exhausted — resolve to Terminal so callers never see Ambiguous (B4).
				return faultErr(cohort.Fault{
					Class:   cohort.FaultTerminal,
					Code:    f.Code,
					Message: fmt.Sprintf("ambiguous fault unresolved after %d retries: %s", maxAmbiguousRetries, f.Message),
				})
			}
			ambiguousAttempt++
			// No sleep: the idempotency token makes the re-issue safe and immediate.

		default:
			// Consistency, Capacity, Terminal — surface to the reconciler.
			return faultErr(f)
		}
	}
}

// FaultError wraps a classified cohort.Fault as an error so callers can
// inspect it via errors.As.
type FaultError struct {
	Fault cohort.Fault
}

func (e *FaultError) Error() string {
	return fmt.Sprintf("%s/%s: %s", e.Fault.Class, e.Fault.Code, e.Fault.Message)
}

func faultErr(f cohort.Fault) error { return &FaultError{Fault: f} }

// runRequestToEC2 converts the provider-agnostic substrate.RunRequest to the
// EC2-specific RunInstancesInput. One-to-one; no count, no pool.
func runRequestToEC2(req substrate.RunRequest) *ec2.RunInstancesInput {
	in := &ec2.RunInstancesInput{
		ImageId:      aws.String(req.AMI),
		InstanceType: ec2types.InstanceType(req.InstanceType),
		MinCount:     aws.Int32(1),
		MaxCount:     aws.Int32(1),
		ClientToken:  aws.String(req.IdempotencyToken),
		TagSpecifications: []ec2types.TagSpecification{
			{
				ResourceType: ec2types.ResourceTypeInstance,
				Tags:         mapToEC2Tags(req.Tags),
			},
		},
	}
	if req.SubnetID != "" {
		in.SubnetId = aws.String(req.SubnetID)
	}
	if len(req.SecurityGroupIDs) > 0 {
		in.SecurityGroupIds = req.SecurityGroupIDs
	}
	if req.IAMInstanceArn != "" {
		in.IamInstanceProfile = &ec2types.IamInstanceProfileSpecification{
			Arn: aws.String(req.IAMInstanceArn),
		}
	}
	if req.Spot {
		in.InstanceMarketOptions = &ec2types.InstanceMarketOptionsRequest{
			MarketType: ec2types.MarketTypeSpot,
		}
	}
	if req.PlacementGroup != "" {
		in.Placement = &ec2types.Placement{
			GroupName:        aws.String(req.PlacementGroup),
			AvailabilityZone: aws.String(req.AvailZone),
		}
	} else if req.AvailZone != "" {
		in.Placement = &ec2types.Placement{
			AvailabilityZone: aws.String(req.AvailZone),
		}
	}
	return in
}

func mapToEC2Tags(m map[string]string) []ec2types.Tag {
	tags := make([]ec2types.Tag, 0, len(m))
	for k, v := range m {
		tags = append(tags, ec2types.Tag{Key: aws.String(k), Value: aws.String(v)})
	}
	return tags
}

func instanceFromEC2(i ec2types.Instance) substrate.Instance {
	inst := substrate.Instance{
		ProviderID:   aws.ToString(i.InstanceId),
		State:        string(i.State.Name),
		InstanceType: string(i.InstanceType),
	}
	if i.PrivateIpAddress != nil {
		inst.PrivateAddr = aws.ToString(i.PrivateIpAddress)
	}
	if i.Placement != nil {
		inst.AvailZone = aws.ToString(i.Placement.AvailabilityZone)
	}
	// Extract generation tag for DescribeByTag callers (B5).
	for _, t := range i.Tags {
		if strings.EqualFold(aws.ToString(t.Key), "q0:generation") {
			break
		}
	}
	return inst
}
