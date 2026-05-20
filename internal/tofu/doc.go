// Package tofu generates and applies OpenTofu for the STATIC substrate only —
// VPC/IAM/controller/storage/partition definitions. NO CloudFormation, NO CDK.
//
// The operator never sees HCL: they edit spec/*.yaml, and this package emits
// HCL, runs `tofu apply`, and keeps state in S3 + a DynamoDB lock. The point
// of generating OpenTofu rather than hand-rolling a reconciler is that the
// AWS provider already implements resource CRUD and drift detection correctly
// — reimplementing that is a year badly spent (ARCHITECTURE §2).
//
// The elastic fleet is NOT managed here — that is internal/substrate.
package tofu
