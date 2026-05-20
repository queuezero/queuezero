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

// syntheticAPIErr builds a minimal smithy.APIError.
type syntheticAPIErr struct {
	code    string
	message string
	fault   smithy.ErrorFault
}

func (e *syntheticAPIErr) Error() string        { return fmt.Sprintf("%s: %s", e.code, e.message) }
func (e *syntheticAPIErr) ErrorCode() string    { return e.code }
func (e *syntheticAPIErr) ErrorMessage() string { return e.message }
func (e *syntheticAPIErr) ErrorFault() smithy.ErrorFault { return e.fault }

func apiErr(code, msg string) error {
	return &syntheticAPIErr{code: code, message: msg, fault: smithy.FaultServer}
}

// wrappedAPIErr wraps a smithy.APIError inside a plain error so errors.As is
// exercised, not just a direct type assertion.
func wrappedAPIErr(code, msg string) error {
	return fmt.Errorf("ec2 operation failed: %w", apiErr(code, msg))
}

var clf = Classifier{}

// Table: every structured code in awsFaultTable plus unknown → Terminal.
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

		// Terminal
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

		// Unknown code → Terminal (fail loud, never hang)
		{"SomeCompletelyUnknownCode", cohort.FaultTerminal},
		{"", cohort.FaultTerminal},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			f := clf.Classify(apiErr(tc.code, "test message"))
			if f.Class != tc.wantClass {
				t.Errorf("code=%q: got class %v want %v", tc.code, f.Class, tc.wantClass)
			}
			// Verbatim code must be preserved.
			if f.Code != tc.code {
				t.Errorf("code=%q: Fault.Code=%q (paraphrased)", tc.code, f.Code)
			}
		})
	}
}

// errors.As must work through a wrapping layer.
func TestClassifier_WrappedAPIErr(t *testing.T) {
	f := clf.Classify(wrappedAPIErr("InsufficientInstanceCapacity", "no capacity"))
	if f.Class != cohort.FaultCapacityExhausted {
		t.Errorf("wrapped ICE: got %v want CapacityExhausted", f.Class)
	}
	if f.Code != "InsufficientInstanceCapacity" {
		t.Errorf("wrapped ICE: Fault.Code=%q", f.Code)
	}
}

// Retryable flag: true iff Consistency or Throttle.
func TestClassifier_RetryableFlag(t *testing.T) {
	retryable := []string{"InvalidInstanceID.NotFound", "RequestLimitExceeded"}
	for _, code := range retryable {
		f := clf.Classify(apiErr(code, ""))
		if !f.Retryable {
			t.Errorf("code=%q: want Retryable=true", code)
		}
	}
	notRetryable := []string{"InsufficientInstanceCapacity", "UnauthorizedOperation", "SomeUnknown"}
	for _, code := range notRetryable {
		f := clf.Classify(apiErr(code, ""))
		if f.Retryable {
			t.Errorf("code=%q: want Retryable=false", code)
		}
	}
}

// Transport-level errors → FaultAmbiguous.
func TestClassifier_TransportErrors(t *testing.T) {
	netTimeout := &net.OpError{
		Op:  "dial",
		Err: &timeoutError{},
	}
	cases := []struct {
		name string
		err  error
	}{
		{"context deadline", context.DeadlineExceeded},
		{"context canceled", context.Canceled},
		{"net timeout", netTimeout},
		{"url error", &url.Error{Op: "Get", URL: "https://ec2.amazonaws.com", Err: errors.New("connection reset by peer")}},
		{"plain connection reset", errors.New("connection reset by peer")},
		{"plain eof", errors.New("unexpected EOF")},
		{"io timeout string", errors.New("i/o timeout")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := clf.Classify(tc.err)
			if f.Class != cohort.FaultAmbiguous {
				t.Errorf("%s: got %v want FaultAmbiguous", tc.name, f.Class)
			}
		})
	}
}

// nil error → RetryableConsistency (defensive; caller should not pass nil).
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

// timeoutError implements net.Error for test purposes.
type timeoutError struct{}

func (t *timeoutError) Error() string   { return "i/o timeout" }
func (t *timeoutError) Timeout() bool   { return true }
func (t *timeoutError) Temporary() bool { return true }
