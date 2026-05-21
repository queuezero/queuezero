package substrate

import "github.com/queuezero/queuezero/internal/cohort"

// Token derives a deterministic idempotency token from the cluster, entity,
// and generation. Delegates to cohort.Token — the canonical implementation.
//
// This function exists for callers in package substrate that pre-date the
// cohort.Token extraction; new callers should use cohort.Token directly.
func Token(cluster, entity, generation string) string {
	return cohort.Token(cluster, entity, generation)
}
