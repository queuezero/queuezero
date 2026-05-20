package aws

import (
	"context"
	"errors"
	"net"
	"net/url"

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
	// take seconds to propagate after creation.
	"InvalidInstanceID.NotFound":                        cohort.FaultRetryableConsistency,
	"InvalidAMIID.NotFound":                             cohort.FaultRetryableConsistency,
	"InvalidGroup.NotFound":                             cohort.FaultRetryableConsistency,
	"InvalidSubnetID.NotFound":                          cohort.FaultRetryableConsistency,
	"InvalidNetworkInterfaceID.NotFound":                cohort.FaultRetryableConsistency,
	"InvalidParameterValue.IamInstanceProfileNotReady": cohort.FaultRetryableConsistency,
	"InvalidPlacementGroup.NotFound":                    cohort.FaultRetryableConsistency,
	"InvalidKeyPair.NotFound":                           cohort.FaultRetryableConsistency,

	// --- Throttle: slow the whole client ------------------------------------

	"RequestLimitExceeded":  cohort.FaultThrottle,
	"Throttling":            cohort.FaultThrottle,
	"ThrottlingException":   cohort.FaultThrottle,
	"EC2ThrottledException": cohort.FaultThrottle,
	"RequestExpired":        cohort.FaultThrottle,

	// --- CapacityExhausted: advance the chain, never retry in place ---------
	// Purchase-model-independent: plain on-demand RunInstances ICEs too.
	// There is no safe baseline — only a chain of rungs (ARCHITECTURE §5, §7).

	"InsufficientInstanceCapacity":      cohort.FaultCapacityExhausted,
	"InsufficientHostCapacity":           cohort.FaultCapacityExhausted,
	"SpotMaxPriceTooLow":                 cohort.FaultCapacityExhausted,
	"MaxSpotInstanceCountExceeded":       cohort.FaultCapacityExhausted,
	"InsufficientFreeAddressesInSubnet":  cohort.FaultCapacityExhausted,
	"Unsupported":                        cohort.FaultCapacityExhausted,

	// --- Terminal: fail immediately, loud -----------------------------------

	"UnauthorizedOperation":       cohort.FaultTerminal,
	"AccessDenied":                cohort.FaultTerminal,
	"AuthFailure":                 cohort.FaultTerminal,
	"InstanceLimitExceeded":       cohort.FaultTerminal,
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
//  1. context.Canceled → FaultTerminal. We cancelled: no retry, no orphan launch.
//  2. context.DeadlineExceeded → FaultAmbiguous. Call timed out; mutation status
//     genuinely unknown. substrate.Client collapses this via idempotency token.
//  3. Typed transport error (net.Error, url.Error) → FaultAmbiguous.
//  4. Structured smithy.APIError → table lookup; unmapped → FaultTerminal.
//  5. Anything else → FaultTerminal.
func (Classifier) Classify(err error) cohort.Fault {
	if err == nil {
		return cohort.Fault{Class: cohort.FaultRetryableConsistency}
	}

	// 1. WE cancelled — shutdown or cohort fast-fail. Do NOT retry: firing a
	//    fresh RunInstances on the way out creates an orphan nobody reconciles.
	if errors.Is(err, context.Canceled) {
		return cohort.Fault{
			Class:     cohort.FaultTerminal,
			Code:      "ContextCanceled",
			Retryable: false,
			Message:   err.Error(),
		}
	}

	// 2. Call timed out — mutation may or may not have landed.
	//    substrate.Client re-issues with the same idempotency token to resolve.
	if errors.Is(err, context.DeadlineExceeded) {
		return cohort.Fault{
			Class:     cohort.FaultAmbiguous,
			Code:      "ContextDeadlineExceeded",
			Retryable: false,
			Message:   err.Error(),
		}
	}

	// 3. Typed transport error — mutation status unknown, same resolution path.
	if isTypedTransportError(err) {
		return cohort.Fault{
			Class:     cohort.FaultAmbiguous,
			Code:      "TransportError",
			Retryable: false,
			Message:   err.Error(),
		}
	}

	// 4. Structured AWS API error — verbatim code preserved.
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

	// 5. Unrecognised — terminal to avoid hanging on an unknown condition.
	return cohort.Fault{
		Class:     cohort.FaultTerminal,
		Code:      "UnknownError",
		Retryable: false,
		Message:   err.Error(),
	}
}

// isTypedTransportError returns true for network-level failures using only
// typed interface checks — no string matching. String matching is the
// anti-pattern the taxonomy exists to kill: brittle across SDK versions and
// locales. net.Error and url.Error cover the transport errors the smithy HTTP
// client and net/http layer produce on connection failure, reset, and timeout.
// context sentinels are handled before this function is reached.
func isTypedTransportError(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}
	return false
}
