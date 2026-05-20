package aws

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"

	smithy "github.com/aws/smithy-go"

	"github.com/queuezero/queuezero/internal/cohort"
)

// Classifier maps AWS EC2 error codes to fault classes. This table IS the
// product (ARCHITECTURE §5). It is exhaustive and explicit — never ad-hoc
// string matching scattered through the resume path.
type Classifier struct{}

// awsFaultTable is the authoritative mapping. Extend it as new codes are
// observed; an unmapped code defaults to FaultTerminal (fail loud, never hang).
var awsFaultTable = map[string]cohort.FaultClass{
	// --- RetryableConsistency: propagation lag, not failure -----------------

	// Fresh-resource consistency gaps: IAM profile, AMI, SG, subnet, ENI all
	// take seconds to propagate to RunInstances after creation.
	"InvalidInstanceID.NotFound":                        cohort.FaultRetryableConsistency,
	"InvalidAMIID.NotFound":                             cohort.FaultRetryableConsistency,
	"InvalidGroup.NotFound":                             cohort.FaultRetryableConsistency,
	"InvalidSubnetID.NotFound":                          cohort.FaultRetryableConsistency,
	"InvalidNetworkInterfaceID.NotFound":                cohort.FaultRetryableConsistency,
	"InvalidParameterValue.IamInstanceProfileNotReady": cohort.FaultRetryableConsistency,
	// Placement group may not be visible immediately after creation.
	"InvalidPlacementGroup.NotFound": cohort.FaultRetryableConsistency,
	// Key pair consistency gap.
	"InvalidKeyPair.NotFound": cohort.FaultRetryableConsistency,

	// --- Throttle: slow the whole client ------------------------------------

	"RequestLimitExceeded":  cohort.FaultThrottle,
	"Throttling":            cohort.FaultThrottle,
	"ThrottlingException":   cohort.FaultThrottle,
	"EC2ThrottledException": cohort.FaultThrottle,
	// Service-level throttle on Describe calls.
	"RequestExpired": cohort.FaultThrottle,

	// --- CapacityExhausted: advance the chain, never retry in place ---------
	// NOTE: purchase-model-independent. Plain on-demand RunInstances ICEs too.
	// There is no safe baseline — only a chain of rungs with different ICE
	// probabilities (ARCHITECTURE §5, §7).

	"InsufficientInstanceCapacity": cohort.FaultCapacityExhausted,
	"InsufficientHostCapacity":      cohort.FaultCapacityExhausted,
	// Spot-specific capacity signals — treat as "this rung unavailable".
	"SpotMaxPriceTooLow":             cohort.FaultCapacityExhausted,
	"MaxSpotInstanceCountExceeded":   cohort.FaultCapacityExhausted,
	"InsufficientFreeAddressesInSubnet": cohort.FaultCapacityExhausted,
	// Instance type not offered in this AZ — advance the chain.
	"Unsupported": cohort.FaultCapacityExhausted,

	// --- Terminal: fail immediately, loud -----------------------------------
	// These are misconfiguration or hard limits; retrying will not help.

	"UnauthorizedOperation":       cohort.FaultTerminal,
	"AccessDenied":                cohort.FaultTerminal,
	"AuthFailure":                 cohort.FaultTerminal,
	"InstanceLimitExceeded":       cohort.FaultTerminal, // quota, not capacity
	"VcpuLimitExceeded":           cohort.FaultTerminal,
	"InvalidParameterValue":       cohort.FaultTerminal,
	"InvalidParameterCombination": cohort.FaultTerminal,
	"InvalidAMIID.Malformed":      cohort.FaultTerminal,
	"InvalidSpotInstanceRequest":  cohort.FaultTerminal,
	"InvalidBlockDeviceMapping":   cohort.FaultTerminal,
	"InvalidInstanceType":         cohort.FaultTerminal,
}

// Classify implements cohort.Classifier.
//
// Classification order:
//  1. Unwrap to smithy.APIError — extract verbatim code + message.
//  2. Look up awsFaultTable; default unmapped codes → FaultTerminal.
//  3. Transport-level errors (timeout, connection reset, 5xx without a
//     structured code) → FaultAmbiguous. The substrate.Client (Step 3) is
//     responsible for collapsing FaultAmbiguous via idempotency-token retry
//     before it ever reaches the cohort reconciler.
func (Classifier) Classify(err error) cohort.Fault {
	if err == nil {
		return cohort.Fault{Class: cohort.FaultRetryableConsistency}
	}

	// 1. Try to unwrap a structured API error.
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		code := apiErr.ErrorCode()
		msg := apiErr.ErrorMessage()
		class, ok := awsFaultTable[code]
		if !ok {
			class = cohort.FaultTerminal
		}
		return cohort.Fault{
			Class:     class,
			Code:      code,
			Retryable: class == cohort.FaultRetryableConsistency || class == cohort.FaultThrottle,
			Message:   msg,
		}
	}

	// 2. Transport-level signals → FaultAmbiguous.
	//    Mutation status is unknown; the idempotency token (Step 3) resolves it.
	if isTransportError(err) {
		return cohort.Fault{
			Class:     cohort.FaultAmbiguous,
			Code:      "TransportError",
			Retryable: false,
			Message:   err.Error(),
		}
	}

	// 3. Unrecognised error type — treat as terminal to avoid hanging.
	return cohort.Fault{
		Class:     cohort.FaultTerminal,
		Code:      "UnknownError",
		Retryable: false,
		Message:   err.Error(),
	}
}

// isTransportError returns true for network-level failures where the mutation
// status is unknown: timeouts, connection resets, and HTTP 5xx responses that
// did not produce a structured API error body.
func isTransportError(err error) bool {
	msg := strings.ToLower(err.Error())

	// Context deadline / timeout.
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}

	// Net-level: connection reset, refused, EOF, timeout.
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}

	// String-level fallback for smithy transport errors that wrap without
	// implementing a typed interface.
	for _, fragment := range []string{
		"connection reset", "connection refused", "broken pipe",
		"eof", "i/o timeout", "no such host",
		"tls handshake", "context deadline exceeded", "context canceled",
	} {
		if strings.Contains(msg, fragment) {
			return true
		}
	}
	return false
}

