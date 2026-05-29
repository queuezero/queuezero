package aws

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/queuezero/queuezero/internal/cohort"
	"github.com/queuezero/queuezero/internal/substrate"
)

// ---- fake EC2 ---------------------------------------------------------------

// fakeEC2 lets tests specify a response sequence per call type.
type fakeEC2 struct {
	runResponses  []runResponse
	runCallCount  int32 // atomic
	runTokensSeen []string

	startErr error
	stopErr  error
	termErr  error

	describeInstances []ec2types.Instance
	describeErr       error
}

type runResponse struct {
	out *ec2.RunInstancesOutput
	err error
}

func (f *fakeEC2) RunInstances(_ context.Context, in *ec2.RunInstancesInput, _ ...func(*ec2.Options)) (*ec2.RunInstancesOutput, error) {
	n := int(atomic.AddInt32(&f.runCallCount, 1)) - 1
	if in.ClientToken != nil {
		f.runTokensSeen = append(f.runTokensSeen, *in.ClientToken)
	}
	if n < len(f.runResponses) {
		r := f.runResponses[n]
		return r.out, r.err
	}
	// Default: success with a dummy instance.
	return &ec2.RunInstancesOutput{
		Instances: []ec2types.Instance{{
			InstanceId:       awssdk.String("i-default"),
			State:            &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning},
			PrivateIpAddress: awssdk.String("10.0.0.1"),
		}},
	}, nil
}

func (f *fakeEC2) StartInstances(_ context.Context, in *ec2.StartInstancesInput, _ ...func(*ec2.Options)) (*ec2.StartInstancesOutput, error) {
	if f.startErr != nil {
		return nil, f.startErr
	}
	return &ec2.StartInstancesOutput{
		StartingInstances: []ec2types.InstanceStateChange{{
			InstanceId:   awssdk.String(in.InstanceIds[0]),
			CurrentState: &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning},
		}},
	}, nil
}

func (f *fakeEC2) StopInstances(_ context.Context, _ *ec2.StopInstancesInput, _ ...func(*ec2.Options)) (*ec2.StopInstancesOutput, error) {
	return &ec2.StopInstancesOutput{}, f.stopErr
}

func (f *fakeEC2) TerminateInstances(_ context.Context, _ *ec2.TerminateInstancesInput, _ ...func(*ec2.Options)) (*ec2.TerminateInstancesOutput, error) {
	return &ec2.TerminateInstancesOutput{}, f.termErr
}

func (f *fakeEC2) DescribeInstances(_ context.Context, _ *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	if f.describeErr != nil {
		return nil, f.describeErr
	}
	return &ec2.DescribeInstancesOutput{
		Reservations: []ec2types.Reservation{{Instances: f.describeInstances}},
	}, nil
}

func (f *fakeEC2) DescribeTags(_ context.Context, _ *ec2.DescribeTagsInput, _ ...func(*ec2.Options)) (*ec2.DescribeTagsOutput, error) {
	return &ec2.DescribeTagsOutput{}, nil
}

// ---- helpers ----------------------------------------------------------------

// transportErr creates a net.Error for testing Ambiguous classification.
type fakeNetErr struct{ msg string }

func (e *fakeNetErr) Error() string   { return e.msg }
func (e *fakeNetErr) Timeout() bool   { return true }
func (e *fakeNetErr) Temporary() bool { return true }

func wrapNetErr(msg string) error {
	return &net.OpError{Op: "dial", Err: &fakeNetErr{msg: msg}}
}

func newLimiter() *substrate.Limiter {
	return substrate.NewLimiter(substrate.LimiterConfig{BaseRate: 1000, MaxBurst: 1000}, nil)
}

func newClient(fake *fakeEC2) *Client {
	return NewClient(fake, newLimiter())
}

func newClientWithLimiter(fake *fakeEC2, l limiterIface) *Client {
	return newClientForTest(fake, l)
}

func runReq(token string) substrate.RunRequest {
	return substrate.RunRequest{
		AMI:              "ami-test",
		InstanceType:     "m5.xlarge",
		IdempotencyToken: token,
		Tags:             map[string]string{"q0:entity": "e1"},
	}
}

func okRun(id string) runResponse {
	return runResponse{out: &ec2.RunInstancesOutput{
		Instances: []ec2types.Instance{{
			InstanceId: awssdk.String(id),
			State:      &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning},
		}},
	}}
}

// ---- tests ------------------------------------------------------------------

// Ambiguous once, then success — Client retries and returns the instance.
func TestClient_RunInstance_AmbiguousThenSuccess(t *testing.T) {
	fake := &fakeEC2{runResponses: []runResponse{
		{err: wrapNetErr("i/o timeout")}, // classified Ambiguous
		okRun("i-recovered"),
	}}
	c := newClient(fake)
	inst, err := c.RunInstance(context.Background(), runReq("tok-1"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inst.ProviderID != "i-recovered" {
		t.Errorf("ProviderID=%q want i-recovered", inst.ProviderID)
	}
	if atomic.LoadInt32(&fake.runCallCount) != 2 {
		t.Errorf("runCallCount=%d want 2", fake.runCallCount)
	}
}

// Ambiguous exhausted — surfaces as Terminal, never as Ambiguous.
func TestClient_RunInstance_AmbiguousExhausted_NeverSurfacesAmbiguous(t *testing.T) {
	responses := make([]runResponse, maxAmbiguousRetries+1)
	for i := range responses {
		responses[i] = runResponse{err: wrapNetErr("connection refused")}
	}
	fake := &fakeEC2{runResponses: responses}
	c := newClient(fake)
	_, err := c.RunInstance(context.Background(), runReq("tok-exhaust"))
	if err == nil {
		t.Fatal("expected error after exhausted ambiguous retries")
	}
	var fe *FaultError
	if !errors.As(err, &fe) {
		t.Fatalf("error is not *FaultError: %T %v", err, err)
	}
	if fe.Fault.Class == cohort.FaultAmbiguous {
		t.Errorf("FaultAmbiguous escaped Client — must never happen")
	}
	if fe.Fault.Class != cohort.FaultTerminal {
		t.Errorf("got class %v want Terminal after ambiguous exhaustion", fe.Fault.Class)
	}
}

// Explicit assertion: no Client method ever returns FaultAmbiguous.
func TestClient_NeverReturnsAmbiguous(t *testing.T) {
	ambErr := wrapNetErr("net timeout")

	t.Run("RunInstance", func(t *testing.T) {
		responses := make([]runResponse, maxAmbiguousRetries+2)
		for i := range responses {
			responses[i] = runResponse{err: ambErr}
		}
		c := newClient(&fakeEC2{runResponses: responses})
		_, err := c.RunInstance(context.Background(), runReq("tok-noamb"))
		assertNotAmbiguous(t, err)
	})

	t.Run("StartInstance", func(t *testing.T) {
		c := newClient(&fakeEC2{startErr: ambErr})
		// Start goes through callWithRetry — exhaust and check.
		_, err := c.StartInstance(context.Background(), "i-123")
		assertNotAmbiguous(t, err)
	})

	t.Run("StopInstance", func(t *testing.T) {
		c := newClient(&fakeEC2{stopErr: ambErr})
		err := c.StopInstance(context.Background(), "i-123", false)
		assertNotAmbiguous(t, err)
	})

	t.Run("TerminateInstance", func(t *testing.T) {
		c := newClient(&fakeEC2{termErr: ambErr})
		err := c.TerminateInstance(context.Background(), "i-123")
		assertNotAmbiguous(t, err)
	})
}

func assertNotAmbiguous(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	var fe *FaultError
	if errors.As(err, &fe) && fe.Fault.Class == cohort.FaultAmbiguous {
		t.Errorf("FaultAmbiguous escaped the Client: %v", err)
	}
}

// Throttle: Limiter.Backoff is invoked, then a successful retry follows.
func TestClient_RunInstance_ThrottleThenSuccess(t *testing.T) {
	var backoffCalled int32
	l := &noopLimiter{backoffCalls: &backoffCalled}
	fake := &fakeEC2{runResponses: []runResponse{
		{err: apiErr("Throttling", "rate exceeded")},
		okRun("i-throttled"),
	}}
	c := newClientWithLimiter(fake, l)
	inst, err := c.RunInstance(context.Background(), runReq("tok-throttle"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inst.ProviderID != "i-throttled" {
		t.Errorf("ProviderID=%q want i-throttled", inst.ProviderID)
	}
	if atomic.LoadInt32(&backoffCalled) == 0 {
		t.Error("Limiter.Backoff was not called on Throttle")
	}
}

// Token stability: all re-issues of one RunInstance carry the same ClientToken.
func TestClient_RunInstance_TokenStable(t *testing.T) {
	const token = "q0-stabletok"
	// Ambiguous twice, then success.
	fake := &fakeEC2{runResponses: []runResponse{
		{err: wrapNetErr("timeout")},
		{err: wrapNetErr("timeout")},
		okRun("i-stable"),
	}}
	c := newClient(fake)
	_, err := c.RunInstance(context.Background(), runReq(token))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for i, seen := range fake.runTokensSeen {
		if seen != token {
			t.Errorf("call %d: ClientToken=%q want %q", i, seen, token)
		}
	}
	if len(fake.runTokensSeen) != 3 {
		t.Errorf("expected 3 calls, got %d", len(fake.runTokensSeen))
	}
}

// DescribeByTag miss → empty slice, no error (StateUnknown, not StateAbsent).
func TestClient_DescribeByTag_MissIsUnknown(t *testing.T) {
	fake := &fakeEC2{describeInstances: nil}
	c := newClient(fake)
	instances, err := c.DescribeByTag(context.Background(), map[string]string{"q0:entity": "ghost"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(instances) != 0 {
		t.Errorf("got %d instances want 0 (miss should be empty, not error)", len(instances))
	}
}

// DescribeByTag returns results when found.
func TestClient_DescribeByTag_Found(t *testing.T) {
	fake := &fakeEC2{
		describeInstances: []ec2types.Instance{
			{
				InstanceId:       awssdk.String("i-found"),
				State:            &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning},
				PrivateIpAddress: awssdk.String("10.0.1.5"),
				Placement:        &ec2types.Placement{AvailabilityZone: awssdk.String("us-east-1a")},
			},
		},
	}
	c := newClient(fake)
	instances, err := c.DescribeByTag(context.Background(), map[string]string{"q0:entity": "e1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(instances) != 1 {
		t.Fatalf("got %d instances want 1", len(instances))
	}
	if instances[0].ProviderID != "i-found" {
		t.Errorf("ProviderID=%q want i-found", instances[0].ProviderID)
	}
}

// instanceFromEC2 surfaces the q0:generation / q0:entity tags and LaunchTime so
// the orphan sweeper can identify and reap superseded instances by name.
func TestClient_DescribeByTag_PopulatesGenerationEntityLaunchTime(t *testing.T) {
	launch := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	fake := &fakeEC2{
		describeInstances: []ec2types.Instance{
			{
				InstanceId: awssdk.String("i-gen"),
				State:      &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning},
				LaunchTime: awssdk.Time(launch),
				Tags: []ec2types.Tag{
					{Key: awssdk.String("q0:generation"), Value: awssdk.String("g3")},
					{Key: awssdk.String("q0:entity"), Value: awssdk.String("gpu-007")},
					{Key: awssdk.String("q0:cluster"), Value: awssdk.String("test")},
				},
			},
		},
	}
	c := newClient(fake)
	instances, err := c.DescribeByTag(context.Background(), map[string]string{"q0:cluster": "test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := instances[0]
	if got.Generation != "g3" {
		t.Errorf("Generation=%q want g3", got.Generation)
	}
	if got.Entity != "gpu-007" {
		t.Errorf("Entity=%q want gpu-007", got.Entity)
	}
	if !got.LaunchTime.Equal(launch) {
		t.Errorf("LaunchTime=%v want %v", got.LaunchTime, launch)
	}
}

// Terminal errors surface immediately without retry.
func TestClient_RunInstance_TerminalNoRetry(t *testing.T) {
	fake := &fakeEC2{runResponses: []runResponse{
		{err: apiErr("UnauthorizedOperation", "not allowed")},
	}}
	c := newClient(fake)
	_, err := c.RunInstance(context.Background(), runReq("tok-term"))
	if err == nil {
		t.Fatal("expected error for Terminal fault")
	}
	var fe *FaultError
	if !errors.As(err, &fe) || fe.Fault.Class != cohort.FaultTerminal {
		t.Errorf("got %v want FaultTerminal", err)
	}
	if atomic.LoadInt32(&fake.runCallCount) != 1 {
		t.Errorf("runCallCount=%d want 1 (no retry on Terminal)", fake.runCallCount)
	}
}

// context.Canceled → Terminal immediately, no retry.
func TestClient_RunInstance_ContextCanceled_NoOrphan(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fake := &fakeEC2{runResponses: []runResponse{
		{err: fmt.Errorf("wrapped: %w", context.Canceled)},
	}}
	c := newClient(fake)
	_, err := c.RunInstance(ctx, runReq("tok-cancel"))
	var fe *FaultError
	if errors.As(err, &fe) && fe.Fault.Class == cohort.FaultAmbiguous {
		t.Error("context.Canceled classified as Ambiguous — would create orphan launch")
	}
}

// A3: cancelled context abandons throttle retry promptly.
func TestClient_ThrottleRetry_ContextCancelled_Abandons(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	var backoffCalled int32
	l := &noopLimiter{backoffCalls: &backoffCalled}
	// Every call returns Throttle — without cancellation check it would spin.
	responses := make([]runResponse, maxThrottleRetries+2)
	for i := range responses {
		responses[i] = runResponse{err: apiErr("Throttling", "rate exceeded")}
	}
	c := newClientWithLimiter(&fakeEC2{runResponses: responses}, l)
	_, err := c.RunInstance(ctx, runReq("tok-ctx-throttle"))
	if err == nil {
		t.Fatal("expected error when context cancelled during throttle retry")
	}
	var fe *FaultError
	if errors.As(err, &fe) && fe.Fault.Class == cohort.FaultAmbiguous {
		t.Error("cancelled throttle retry returned Ambiguous")
	}
	// Backoff must have been called at least once — limiter stays degraded.
	if atomic.LoadInt32(&backoffCalled) == 0 {
		t.Error("Limiter.Backoff not called — limiter was not degraded for subsequent callers")
	}
}

// A3: after exhausting throttle retries, Backoff WAS called on the final
// failing attempt — the limiter remains degraded for the next caller.
func TestClient_ThrottleExhausted_LimiterRemainsDegraded(t *testing.T) {
	var backoffCalled int32
	l := &noopLimiter{backoffCalls: &backoffCalled}
	responses := make([]runResponse, maxThrottleRetries+2)
	for i := range responses {
		responses[i] = runResponse{err: apiErr("Throttling", "rate exceeded")}
	}
	c := newClientWithLimiter(&fakeEC2{runResponses: responses}, l)
	_, err := c.RunInstance(context.Background(), runReq("tok-throttle-exhaust"))
	if err == nil {
		t.Fatal("expected error after throttle exhaustion")
	}
	// maxThrottleRetries+1 Backoff calls: one per attempt including the final one.
	want := int32(maxThrottleRetries + 1)
	got := atomic.LoadInt32(&backoffCalled)
	if got != want {
		t.Errorf("Backoff called %d times want %d (final attempt must degrade limiter)", got, want)
	}
}

// ---- test limiter helpers ---------------------------------------------------

// noopLimiter satisfies limiterIface without any real rate limiting — suitable
// for tests that care about call ordering, not timing.
type noopLimiter struct {
	backoffCalls *int32
}

func (l *noopLimiter) Acquire(_ context.Context) error { return nil }
func (l *noopLimiter) Backoff(_ time.Duration) {
	if l.backoffCalls != nil {
		atomic.AddInt32(l.backoffCalls, 1)
	}
}
