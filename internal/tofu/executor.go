// Package tofu generates and applies OpenTofu for the STATIC substrate only —
// VPC/IAM/controller/storage/partition definitions. NO CloudFormation, NO CDK.
//
// The operator never sees HCL: they edit spec/*.yaml, and this package emits
// HCL, runs `tofu apply`, and keeps state in S3 + a DynamoDB lock. The point of
// generating OpenTofu rather than hand-rolling a reconciler is that the AWS
// provider already implements resource CRUD and drift detection correctly —
// reimplementing that is a year badly spent (ARCHITECTURE §2).
//
// The elastic fleet is NOT managed here — that is internal/substrate.
package tofu

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
)

// Executor runs the OpenTofu (or Terraform) binary against a generated working
// directory. It is an interface so `q0 apply` is testable without a real tofu
// binary or AWS.
//
// Shelling `tofu` does NOT violate the "link, don't shell" rule (non-negotiable
// #7), which governs spore.host PROVIDER tools whose errors must arrive as Go
// values. `tofu` is the documented static-substrate seam (ARCHITECTURE §2): it
// owns resource CRUD + drift, and its errors are human-readable operator output,
// not a fault taxonomy. Mirrors the scontrol seam (internal/slurm/scontrol.go).
type Executor interface {
	// Init runs `tofu init` in dir, wiring the S3+DynamoDB backend.
	Init(ctx context.Context, dir string, backend BackendConfig) error
	// Plan runs `tofu plan` and reports whether changes are pending.
	Plan(ctx context.Context, dir string) (PlanSummary, error)
	// Apply runs `tofu apply -auto-approve` — the mutating step, gated by the
	// caller behind an explicit --approve.
	Apply(ctx context.Context, dir string) error
}

// PlanSummary is the coarse outcome of a plan. tofu's -detailed-exitcode gives
// 0 (no changes) or 2 (changes present); 1 is an error.
type PlanSummary struct {
	ChangesPending bool
	Output         string // raw plan output, for the operator
}

// execTofu shells the real binary. bin is the resolved path to `tofu` (preferred)
// or `terraform`; available is false when neither is on PATH.
type execTofu struct {
	bin       string
	available bool
}

// NewExecutor resolves `tofu` (then `terraform`) on PATH. Unlike the scontrol
// seam, absence IS an error: a real apply cannot proceed without the binary, and
// silently no-opping infrastructure changes would be dangerous. The CLI only
// calls this on the non-dry-run path.
func NewExecutor() (Executor, error) {
	for _, name := range []string{"tofu", "terraform"} {
		if path, err := exec.LookPath(name); err == nil {
			return &execTofu{bin: path, available: true}, nil
		}
	}
	return nil, fmt.Errorf("tofu: neither `tofu` nor `terraform` found on PATH")
}

func (t *execTofu) Init(ctx context.Context, dir string, backend BackendConfig) error {
	args := []string{"-chdir=" + dir, "init", "-input=false"}
	for _, kv := range backend.initArgs() {
		args = append(args, "-backend-config="+kv)
	}
	return t.run(ctx, args...)
}

func (t *execTofu) Plan(ctx context.Context, dir string) (PlanSummary, error) {
	cmd := exec.CommandContext(ctx, t.bin, "-chdir="+dir, "plan", "-input=false", "-detailed-exitcode")
	out, err := cmd.CombinedOutput()
	summary := PlanSummary{Output: string(out)}
	if err == nil {
		return summary, nil // exit 0: no changes
	}
	// -detailed-exitcode: exit 2 means "changes present", not a failure.
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 2 {
		summary.ChangesPending = true
		return summary, nil
	}
	return summary, fmt.Errorf("tofu plan: %w\n%s", err, out)
}

func (t *execTofu) Apply(ctx context.Context, dir string) error {
	return t.run(ctx, "-chdir="+dir, "apply", "-input=false", "-auto-approve")
}

// run executes a tofu subcommand, streaming output to the operator's terminal.
func (t *execTofu) run(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, t.bin, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("tofu %v: %w", args, err)
	}
	return nil
}
