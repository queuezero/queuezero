// Package aws is the AWS implementation of queuezero's provider seam.
//
// It supplies:
//   - an Actuator/Observer (cohort ports) over aws-sdk-go-v2 EC2
//   - the AWS Classifier — the explicit error-code -> fault-class table
//
// The Classifier is the single most provider-specific artifact in queuezero
// and is NOT portable. An Azure or GCP port reimplements exactly this file's
// table against entirely different codes, timings, and consistency windows.
// See docs/ARCHITECTURE.md §13.
package aws
