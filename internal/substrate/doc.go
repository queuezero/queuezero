// Package substrate is queuezero's single chokepoint to cloud provider APIs.
//
// NOTHING else in queuezero touches a cloud SDK directly. substrate.Client
// does exactly three things, and everything provider-facing goes through it:
//
//  1. Idempotency tokens on every mutation. RunInstances carries a
//     deterministic ClientToken derived from (cluster, entity, generation).
//     This collapses the Ambiguous fault class — after a timeout, re-issuing
//     the same call returns the existing instance or creates it. The token is
//     also the authority over eventually-consistent reads: Describe is
//     advisory, the token is ground truth.
//
//  2. Classification. The provider Classifier (cohort.Classifier) maps every
//     error into one of five fault classes via an explicit table.
//
//  3. Account-shared adaptive rate limiting. Throttling is a property of the
//     account, not the call site — one client-side token bucket backs the
//     whole client off on Throttle, rather than each goroutine independently.
//
// The static substrate (VPC, IAM, controller, storage, partition definitions)
// is NOT managed here — that is internal/tofu, which generates and applies
// OpenTofu. substrate manages only the elastic fleet: the cattle.
//
// Provider implementations live in subpackages (substrate/aws). The cohort
// core depends on the cohort.Actuator/Observer/Classifier interfaces, which
// these subpackages satisfy; cohort never imports substrate.
package substrate
