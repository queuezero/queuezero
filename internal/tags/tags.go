// Package tags holds the canonical q0 control-channel EC2 tag keys and the
// readiness tag values, shared between the WRITER (the on-node reporter,
// internal/spored) and the READER (the hybrid Observer, internal/substrate/aws).
//
// Both sides referencing one source is the whole point: if the writer emits
// q0:phase="enrolled" and the reader checked for "ready", phase-3 enrollment
// would silently never converge. Keeping the keys and the phase vocabulary here
// makes that drift a compile-time impossibility.
//
// Tags carry small signals; S3 carries payloads (ARCHITECTURE §11). This package
// is pure data — it imports nothing — so any layer may reference it.
package tags

// Config tags written by ASBX at launch (RunInstances TagSpecifications).
const (
	Cluster     = "q0:cluster"
	Entity      = "q0:entity"
	Generation  = "q0:generation"
	Cohort      = "q0:cohort"
	BootstrapS3 = "q0:bootstrap-s3" // S3 URI of the hash-pinned bootstrap script-set
)

// Readiness tags written by the on-node reporter (internal/spored) and read by
// the hybrid Observer for phase-3 enrollment.
const (
	Phase  = "q0:phase"
	Ready  = "q0:ready"  // "true"/"false" (strconv.ParseBool)
	Detail = "q0:detail" // human-readable, surfaced by q0 explain
)

// Phase values for the q0:phase tag. The Observer treats PhaseEnrolled as the
// signal that the node is operational (mounts healthy, slurmd checked in) —
// spored advances to it only after every health probe passes.
const (
	PhaseBooting  = "booting"  // reporter is up, probes not all passing yet
	PhaseEnrolled = "enrolled" // all probes passed; node is operational
)
