package slurm

import (
	"testing"

	"github.com/queuezero/queuezero/internal/cohort"
	"github.com/queuezero/queuezero/internal/mpi"
)

// The graduation milestone (ARCHITECTURE §15): the SAME unmodified cohort core
// must satisfy its domain seam from BOTH the MPI domain and the Slurm domain.
// These compile-time assertions prove both domains fill cohort.Enroller /
// cohort.Assembler. Combined with `make guard-cohort` staying green (cohort
// imports no provider/scheduler/domain package), this is the evidence the domain
// seam is a real seam and not a Slurm-shaped hole.
var (
	_ cohort.Enroller  = (*slurmEnrollerType)(nil)
	_ cohort.Assembler = (*slurmAssemblerType)(nil)
	_ cohort.Enroller  = (*mpi.Enroller)(nil)
	_ cohort.Assembler = (*mpi.Assembler)(nil)
)

// aliases so the assertions read clearly above.
type (
	slurmEnrollerType  = Enroller
	slurmAssemblerType = Assembler
)

// TestGraduation_BothDomainsSatisfyTheSeam is a runtime no-op; the proof is the
// compile-time assertions above. Kept as a named test so the milestone is
// discoverable in `go test -v`.
func TestGraduation_BothDomainsSatisfyTheSeam(t *testing.T) {
	t.Log("MPI and Slurm domains both satisfy cohort.Enroller/Assembler against the unmodified core")
}
