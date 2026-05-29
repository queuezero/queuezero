package mpi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/queuezero/queuezero/internal/cohort"
)

// ManifestPublisher is the provider port the Assembler publishes the converged
// peer manifest through. In production this is an S3-backed writer (the payload
// channel of ARCHITECTURE §11: tags carry signals, S3 carries payloads); in
// tests it is a fake that captures the bytes.
//
// The manifest is published ONCE, after the barrier, over a complete and
// simultaneously-live cohort. Members re-fetch it from S3 to learn the peer set
// — this is why the channel must be mutable/re-fetchable S3, not immutable
// userdata baked at launch when the peer set was not yet known.
type ManifestPublisher interface {
	// Publish writes the manifest payload for a cohort. key is a stable,
	// content-addressable-ish location derived from the cohort ID; data is the
	// serialized PeerManifest. Returning an error fails the assembly phase.
	Publish(ctx context.Context, key string, data []byte) error
}

// Peer is one rank's entry in the manifest — the per-entity topology fact.
// THIS is the topology cohort refuses to model (ARCHITECTURE §4); it lives here
// in the domain, never in the core.
type Peer struct {
	Rank    int    `json:"rank"`
	Entity  string `json:"entity"`
	Address string `json:"address"`
}

// PeerManifest is the converged collective wire-up: the full rank→address map
// the MPI runtime needs (the hostfile, in PMIx terms). The cohort barrier
// guarantees it is COMPLETE before it is published — the thing the old on-box
// shell-loop barrier could never guarantee.
type PeerManifest struct {
	Cohort string `json:"cohort"`
	Peers  []Peer `json:"peers"`
}

// Assembler implements cohort.Assembler for MPI cohorts.
//
// cohort invokes Assemble exactly once, after the barrier, with the complete set
// of live members, and learns only pass/fail. Assemble assigns ranks, builds the
// peer manifest from the members' observed addresses, and publishes it for the
// ranks to pull. The core never sees the manifest — mechanism is the domain's,
// the phase-slot is the core's.
type Assembler struct {
	publisher ManifestPublisher
}

// NewAssembler constructs an MPI Assembler over a ManifestPublisher.
func NewAssembler(publisher ManifestPublisher) *Assembler {
	return &Assembler{publisher: publisher}
}

// Assemble performs the PMIx wire-up: deterministic rank assignment over the
// complete cohort, then publish the manifest. Determinism (sort by entity ID)
// means a re-issued assembly produces the identical manifest — the same
// idempotency discipline the provider layer applies to mutations.
func (a *Assembler) Assemble(ctx context.Context, members []cohort.Observation) error {
	if len(members) == 0 {
		return errors.New("mpi: Assemble received zero members — barrier should never admit an empty cohort")
	}

	// Deterministic rank order: sort members by entity ID. The reconciler does
	// not promise a stable member order, and rank assignment must be reproducible.
	ordered := make([]cohort.Observation, len(members))
	copy(ordered, members)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })

	// Observation carries no cohort id, so derive a stable group label from the
	// (sorted) membership — deterministic given the same set of members.
	manifest := PeerManifest{
		Cohort: cohortKey(ordered),
		Peers:  make([]Peer, 0, len(ordered)),
	}

	for rank, m := range ordered {
		if m.Address == "" {
			// A member with no address cannot participate in the wire-up. The
			// barrier admitted it as enrolled, so a missing address here is a
			// real domain failure — fail the phase loudly rather than publish a
			// broken hostfile.
			return fmt.Errorf("mpi: member %s enrolled but has no address — cannot assign rank %d", m.ID, rank)
		}
		manifest.Peers = append(manifest.Peers, Peer{
			Rank:    rank,
			Entity:  string(m.ID),
			Address: m.Address,
		})
	}

	data, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("mpi: marshal peer manifest: %w", err)
	}

	key := manifestKey(manifest.Cohort)
	if a.publisher == nil {
		return errors.New("mpi: Assembler has no ManifestPublisher; cannot publish peer manifest")
	}
	if err := a.publisher.Publish(ctx, key, data); err != nil {
		return fmt.Errorf("mpi: publish peer manifest to %s: %w", key, err)
	}
	return nil
}

// cohortKey derives a stable key from the member set. Members carry no cohort
// id on Observation, so we use the lowest entity id as a stable group label —
// deterministic given the same membership.
func cohortKey(ordered []cohort.Observation) string {
	if len(ordered) == 0 {
		return "empty"
	}
	return string(ordered[0].ID)
}

// manifestKey is the S3-style object key the manifest is published under.
func manifestKey(cohort string) string {
	return fmt.Sprintf("manifests/%s/peers.json", cohort)
}
