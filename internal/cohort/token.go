package cohort

import (
	"crypto/sha256"
	"encoding/hex"
)

// Token derives a deterministic idempotency token from the cluster, entity,
// and generation. This is the canonical derivation for cohort — provider-agnostic.
//
// Determinism is the entire point: re-issuing a mutation with the same token
// after an Ambiguous fault returns the already-created resource rather than
// creating a duplicate. A random token would silently break this guarantee.
//
// Format: "q0-" + first 16 bytes of SHA-256(cluster + NUL + entity + NUL + generation)
// as lowercase hex. This matches the format of substrate.Token, which will be
// retired in favor of this function once cohort is extracted to its own module.
func Token(cluster, entity, generation string) string {
	h := sha256.Sum256([]byte(cluster + "\x00" + entity + "\x00" + generation))
	return "q0-" + hex.EncodeToString(h[:16])
}
