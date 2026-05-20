package aws

import "github.com/queuezero/queuezero/internal/cohort"

// Classifier maps AWS EC2 error codes to fault classes. This table IS the
// product (ARCHITECTURE §5). It is exhaustive and explicit — never ad-hoc
// string matching scattered through the resume path.
type Classifier struct{}

// awsFaultTable is the authoritative mapping. Extend it as new codes are
// observed; an unmapped code defaults to FaultTerminal (fail loud, never hang).
var awsFaultTable = map[string]cohort.FaultClass{
	// --- RetryableConsistency: propagation lag, not failure -----------------
	"InvalidInstanceID.NotFound":      cohort.FaultRetryableConsistency,
	"InvalidAMIID.NotFound":           cohort.FaultRetryableConsistency, // inside the consistency window
	"InvalidGroup.NotFound":           cohort.FaultRetryableConsistency,
	"InvalidSubnetID.NotFound":        cohort.FaultRetryableConsistency,
	"InvalidNetworkInterfaceID.NotFound": cohort.FaultRetryableConsistency,
	// IAM instance-profile propagation surfaces as this on RunInstances:
	"InvalidParameterValue.IamInstanceProfileNotReady": cohort.FaultRetryableConsistency,

	// --- Throttle: slow the whole client ------------------------------------
	"RequestLimitExceeded": cohort.FaultThrottle,
	"Throttling":           cohort.FaultThrottle,
	"ThrottlingException":  cohort.FaultThrottle,
	"EC2ThrottledException": cohort.FaultThrottle,

	// --- CapacityExhausted: advance the chain, never retry in place ---------
	// NOTE: purchase-model-independent. On-demand RunInstances ICEs too.
	"InsufficientInstanceCapacity":  cohort.FaultCapacityExhausted,
	"InsufficientHostCapacity":      cohort.FaultCapacityExhausted,
	"SpotMaxPriceTooLow":            cohort.FaultCapacityExhausted, // treat as "this rung unavailable"
	"MaxSpotInstanceCountExceeded":  cohort.FaultCapacityExhausted,
	"Unsupported":                   cohort.FaultCapacityExhausted, // type not offered in this AZ

	// --- Terminal: fail immediately, loud -----------------------------------
	"UnauthorizedOperation":          cohort.FaultTerminal,
	"AccessDenied":                   cohort.FaultTerminal,
	"InstanceLimitExceeded":          cohort.FaultTerminal, // a quota, not a capacity, problem
	"VcpuLimitExceeded":              cohort.FaultTerminal,
	"InvalidParameterValue":          cohort.FaultTerminal,
	"InvalidParameterCombination":    cohort.FaultTerminal,
	"InvalidAMIID.Malformed":         cohort.FaultTerminal,
}

// Classify implements cohort.Classifier.
func (Classifier) Classify(err error) cohort.Fault {
	// TODO(phase-1):
	//   1. unwrap to smithy.APIError to get the code + message verbatim.
	//   2. look up awsFaultTable; default unmapped -> FaultTerminal.
	//   3. detect transport-level timeout/reset/5xx -> FaultAmbiguous. The
	//      caller (substrate.Client) collapses Ambiguous via the idempotency
	//      token before the fault ever reaches the cohort reconciler.
	panic("aws.Classifier.Classify: not yet implemented")
}
