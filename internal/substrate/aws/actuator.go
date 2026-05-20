package aws

import (
	"context"

	"github.com/queuezero/queuezero/internal/cohort"
)

// Actuator implements cohort.Actuator and cohort.Observer over EC2. It always
// operates on a single named entity — there is no count-shaped call anywhere
// in this file, by design.
type Actuator struct {
	// TODO(phase-1): hold a substrate.Client and the cross-account role
	// assumption logic (partitions.yaml maps partition -> account).
}

func (a *Actuator) Launch(ctx context.Context, intent cohort.EntityIntent) (cohort.Observation, error) {
	panic("aws.Actuator.Launch: not yet implemented")
}

func (a *Actuator) Start(ctx context.Context, id cohort.EntityID) (cohort.Observation, error) {
	// StartInstances can itself ICE — a stopped instance is not reserved
	// capacity. The reconciler treats warm-start as an advanceable rung.
	panic("aws.Actuator.Start: not yet implemented")
}

func (a *Actuator) Stop(ctx context.Context, id cohort.EntityID, mode cohort.StopMode) error {
	panic("aws.Actuator.Stop: not yet implemented")
}

func (a *Actuator) Terminate(ctx context.Context, id cohort.EntityID) error {
	panic("aws.Actuator.Terminate: not yet implemented")
}

func (a *Actuator) Observe(ctx context.Context, ids []cohort.EntityID) ([]cohort.Observation, error) {
	// DescribeInstances is eventually consistent: a miss is StateUnknown, not
	// StateAbsent.
	panic("aws.Actuator.Observe: not yet implemented")
}
