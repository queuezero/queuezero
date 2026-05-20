package substrate

import (
	"crypto/sha256"
	"encoding/hex"
)

// Token derives a deterministic idempotency token from the cluster, entity,
// and generation. Re-issuing a mutation with the same token after an
// Ambiguous fault (timeout / reset / 5xx) is safe: the provider returns the
// existing resource or creates it. This is what removes the Ambiguous class
// from everything downstream — see ARCHITECTURE.md §5, §6.
//
// The token is ALSO the authority over eventually-consistent reads: when a
// Describe comes back empty right after a launch, re-issuing the tokened
// mutation yields a consistent answer to "did this get created" that Describe
// cannot give.
func Token(cluster, entity, generation string) string {
	h := sha256.Sum256([]byte(cluster + "\x00" + entity + "\x00" + generation))
	return "q0-" + hex.EncodeToString(h[:16])
}
