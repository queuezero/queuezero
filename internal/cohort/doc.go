// Package cohort is queuezero's reconciliation core.
//
// A cohort reconciler converges named sets of identity-bearing entities
// against eventually-consistent infrastructure, where a set succeeds, fails,
// and fast-fails AS A UNIT, and where set-completion is followed by a
// domain-defined assembly phase. The unit of reconciliation is the cohort.
// The single entity is the 1-cohort.
//
// # Why this package exists
//
// The standard cloud toolbox (ASG, managed node groups, Batch, Kubernetes
// Deployments) is built on abstraction-by-erasure: it works by throwing
// entity identity away. cohort assumes the opposite — that entities are
// named, placed, stateful participants that must come up together, learn
// about each other, and fail together. That assumption is the product.
//
// # IMPORT DISCIPLINE — DO NOT VIOLATE
//
// This package MUST NOT import:
//   - any AWS SDK package (github.com/aws/aws-sdk-go-v2/...)
//   - any Slurm-specific package
//   - any other cloud-provider or scheduler package
//
// cohort deals only in the interfaces declared in ports.go. The provider
// (AWS, Azure) is supplied via Actuator/Observer/Classifier; the domain
// (Slurm/MPI, Globus) is supplied via Enroller/Assembler. This rule is what
// keeps the eventual extraction of cohort into its own module a `git mv`
// rather than an archaeology project.
//
// The internal/ path keyword makes this package uncallable from outside the
// queuezero module, so its interfaces may be thrashed freely until a second
// real consumer (the Globus domain layer) proves where the edges are. See
// docs/ARCHITECTURE.md §14.
package cohort
