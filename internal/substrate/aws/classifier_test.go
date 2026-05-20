package aws

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"testing"

	smithy "github.com/aws/smithy-go"

	"github.com/queuezero/queuezero/internal/cohort"
)

// syntheticAPIErr is a minimal smithy.APIError.
type syntheticAPIErr struct {
	code    string
	message string
	fault   smithy.ErrorFault
}

func (e *syntheticAPIErr) Error() string             { return fmt.Sprintf("%s: %s", e.code, e.message) }
func (e *syntheticAPIErr) ErrorCode() string         { return e.code }
func (e *syntheticAPIErr) ErrorMessage() string      { return e.message }
func (e *syntheticAPIErr) ErrorFault() smithy.ErrorFault { return e.fault }

func apiErr(code, msg string) error {
	return &syntheticAPIErr{code: code, message: msg, fault: smithy.FaultServer}
}

func wrappedAPIErr(code, msg string) error {
	return fmt.Errorf("ec2 operation failed: %w", apiErr(code, msg))
}

var clf = Classifier{}

// Every structured code in awsFaultTable, plus unmapped → Terminal.
func TestClassifier_StructuredCodes(t *testing.T) {
	cases := []struct {
		code      string
		wantClass cohort.FaultClass
	}{
		// RetryableConsistency
		{"InvalidInstanceID.NotFound", cohort.FaultRetryableConsistency},
		{"InvalidAMIID.NotFound", cohort.FaultRetryableConsistency},
		{"InvalidGroup.NotFound", cohort.FaultRetryableConsistency},
		{"InvalidSubnetID.NotFound", cohort.FaultRetryableConsistency},
		{"InvalidNetworkInterfaceID.NotFound", cohort.FaultRetryableConsistency},
		{"InvalidParameterValue.IamInstanceProfileNotReady", cohort.FaultRetryableConsistency},
		{"InvalidPlacementGroup.NotFound", cohort.FaultRetryableConsistency},
		{"InvalidKeyPair.NotFound", cohort.FaultRetryableConsistency},
		// Throttle
		{"RequestLimitExceeded", cohort.FaultThrottle},
		{"Throttling", cohort.FaultThrottle},
		{"ThrottlingException", cohort.FaultThrottle},
		{"EC2ThrottledException", cohort.FaultThrottle},
		{"RequestExpired", cohort.FaultThrottle},
		// CapacityExhausted
		{"InsufficientInstanceCapacity", cohort.FaultCapacityExhausted},
		{"InsufficientHostCapacity", cohort.FaultCapacityExhausted},
		{"SpotMaxPriceTooLow", cohort.FaultCapacityExhausted},
		{"MaxSpotInstanceCountExceeded", cohort.FaultCapacityExhausted},
		{"InsufficientFreeAddressesInSubnet", cohort.FaultCapacityExhausted},
		{"Unsupported", cohort.FaultCapacityExhausted},
		// Terminal — explicit
		{"UnauthorizedOperation", cohort.FaultTerminal},
		{"AccessDenied", cohort.FaultTerminal},
		{"AuthFailure", cohort.FaultTerminal},
		{"InstanceLimitExceeded", cohort.FaultTerminal},
		{"VcpuLimitExceeded", cohort.FaultTerminal},
		{"InvalidParameterValue", cohort.FaultTerminal},
		{"InvalidParameterCombination", cohort.FaultTerminal},
		{"InvalidAMIID.Malformed", cohort.FaultTerminal},
		{"InvalidSpotInstanceRequest", cohort.FaultTerminal},
		{"InvalidBlockDeviceMapping", cohort.FaultTerminal},
		{"InvalidInstanceType", cohort.FaultTerminal},
		// Unmapped → Terminal
		{"SomeCompletelyUnknownCode", cohort.FaultTerminal},
		{"", cohort.FaultTerminal},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			f := clf.Classify(apiErr(tc.code, "test message"))
			if f.Class != tc.wantClass {
				t.Errorf("code=%q: got class %v want %v", tc.code, f.Class, tc.wantClass)
			}
			if f.Code != tc.code {
				t.Errorf("code=%q: Fault.Code=%q (paraphrased)", tc.code, f.Code)
			}
		})
	}
}

// A3: unmapped code must carry the VERBATIM AWS code, not "unknown" or empty.
func TestClassifier_UnmappedCodeVerbatim(t *testing.T) {
	codes := []string{
		"SomeFutureEC2Code",
		"InvalidFoo.Bar",
		"WeirdNewThrottleVariant",
	}
	for _, code := range codes {
		f := clf.Classify(apiErr(code, "some message"))
		if f.Class != cohort.FaultTerminal {
			t.Errorf("code=%q: got class %v want Terminal", code, f.Class)
		}
		if f.Code != code {
			t.Errorf("code=%q: Fault.Code=%q — verbatim code not preserved", code, f.Code)
		}
	}
}

// errors.As must unwrap through a wrapping layer.
func TestClassifier_WrappedAPIErr(t *testing.T) {
	f := clf.Classify(wrappedAPIErr("InsufficientInstanceCapacity", "no capacity"))
	if f.Class != cohort.FaultCapacityExhausted {
		t.Errorf("wrapped ICE: got %v want CapacityExhausted", f.Class)
	}
	if f.Code != "InsufficientInstanceCapacity" {
		t.Errorf("wrapped ICE: Fault.Code=%q", f.Code)
	}
}

// Retryable is true iff Consistency or Throttle.
func TestClassifier_RetryableFlag(t *testing.T) {
	for _, code := range []string{"InvalidInstanceID.NotFound", "RequestLimitExceeded"} {
		f := clf.Classify(apiErr(code, ""))
		if !f.Retryable {
			t.Errorf("code=%q: want Retryable=true", code)
		}
	}
	for _, code := range []string{"InsufficientInstanceCapacity", "UnauthorizedOperation", "SomeUnknown"} {
		f := clf.Classify(apiErr(code, ""))
		if f.Retryable {
			t.Errorf("code=%q: want Retryable=false", code)
		}
	}
}

// A1: context.Canceled → Terminal (WE cancelled — no retry, no orphan launch).
func TestClassifier_ContextCanceled_IsTerminal(t *testing.T) {
	f := clf.Classify(context.Canceled)
	if f.Class != cohort.FaultTerminal {
		t.Errorf("context.Canceled: got %v want Terminal", f.Class)
	}
	if f.Retryable {
		t.Errorf("context.Canceled: want Retryable=false")
	}
	if f.Code != "ContextCanceled" {
		t.Errorf("context.Canceled: Code=%q want ContextCanceled", f.Code)
	}
}

// A1: context.DeadlineExceeded → Ambiguous (call may have landed).
func TestClassifier_ContextDeadlineExceeded_IsAmbiguous(t *testing.T) {
	f := clf.Classify(context.DeadlineExceeded)
	if f.Class != cohort.FaultAmbiguous {
		t.Errorf("context.DeadlineExceeded: got %v want Ambiguous", f.Class)
	}
	if f.Code != "ContextDeadlineExceeded" {
		t.Errorf("context.DeadlineExceeded: Code=%q", f.Code)
	}
}

// Wrapped context sentinels must also classify correctly.
func TestClassifier_WrappedContextSentinels(t *testing.T) {
	wrappedCanceled := fmt.Errorf("op failed: %w", context.Canceled)
	f := clf.Classify(wrappedCanceled)
	if f.Class != cohort.FaultTerminal {
		t.Errorf("wrapped Canceled: got %v want Terminal", f.Class)
	}

	wrappedDeadline := fmt.Errorf("op failed: %w", context.DeadlineExceeded)
	f = clf.Classify(wrappedDeadline)
	if f.Class != cohort.FaultAmbiguous {
		t.Errorf("wrapped DeadlineExceeded: got %v want Ambiguous", f.Class)
	}
}

// A2: transport errors use TYPED checks only (net.Error, url.Error).
func TestClassifier_TypedTransportErrors(t *testing.T) {
	netTimeout := &net.OpError{Op: "dial", Err: &timeoutError{}}
	urlErr := &url.Error{Op: "Get", URL: "https://ec2.amazonaws.com", Err: errors.New("conn refused")}

	for _, tc := range []struct {
		name string
		err  error
	}{
		{"net.Error", netTimeout},
		{"url.Error", urlErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := clf.Classify(tc.err)
			if f.Class != cohort.FaultAmbiguous {
				t.Errorf("%s: got %v want Ambiguous", tc.name, f.Class)
			}
		})
	}
}

// Plain strings that look like transport errors are NOT special — terminal.
func TestClassifier_PlainStringErrors_AreTerminal(t *testing.T) {
	for _, msg := range []string{
		"connection reset by peer",
		"unexpected EOF",
		"i/o timeout",
		"no such host",
	} {
		f := clf.Classify(errors.New(msg))
		if f.Class != cohort.FaultTerminal {
			t.Errorf("plain string %q: got %v want Terminal (no string matching)", msg, f.Class)
		}
	}
}

// nil → RetryableConsistency (defensive).
func TestClassifier_NilError(t *testing.T) {
	f := clf.Classify(nil)
	if f.Class != cohort.FaultRetryableConsistency {
		t.Errorf("nil: got %v want RetryableConsistency", f.Class)
	}
}

// Unknown non-API, non-transport error → Terminal.
func TestClassifier_UnknownPlainError(t *testing.T) {
	f := clf.Classify(errors.New("something completely unexpected"))
	if f.Class != cohort.FaultTerminal {
		t.Errorf("unknown: got %v want Terminal", f.Class)
	}
}

// timeoutError implements net.Error.
type timeoutError struct{}

func (t *timeoutError) Error() string   { return "i/o timeout" }
func (t *timeoutError) Timeout() bool   { return true }
func (t *timeoutError) Temporary() bool { return true }
