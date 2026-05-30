package slurm

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/queuezero/queuezero/internal/cohort"
)

// nowFunc is the clock used when stamping/reconciling holds. It is a package
// var so tests can install a deterministic clock; production uses time.Now.
var nowFunc = time.Now

// Hold is the resume-time budget hold for one node, persisted so the matching
// SuspendProgram — a separate, stateless invocation — can reconcile it against
// actuals at teardown (the other half of the spend-rate loop, ARCHITECTURE §12).
//
// This is a Slurm/ASBB-domain record, deliberately NOT part of cohort.Record:
// TransactionID/account are budget concepts and must not leak into the
// domain-neutral cohort core (non-negotiable #3). It lives beside the cohort
// record store, keyed the same way (by node name), in its own directory.
type Hold struct {
	Entity        cohort.EntityID `json:"entity"`
	TransactionID string          `json:"transaction_id"`
	Account       string          `json:"account"`
	Partition     string          `json:"partition"`
	// HourlyRate is the per-node $/hr the gate held against. Actual cost at
	// reconcile is HourlyRate × (now − StartedAt), matching the rate-reservation
	// hold semantics (ASBB issue #10 model (a)).
	HourlyRate float64   `json:"hourly_rate"`
	StartedAt  time.Time `json:"started_at"`
}

// HoldStore persists per-node holds. The FileStore implementation mirrors
// recordstore.FileStore (atomic write-then-rename, one JSON file per entity).
type HoldStore interface {
	Put(h Hold) error
	Get(entity cohort.EntityID) (Hold, error)
	Delete(entity cohort.EntityID) error
}

// ErrNoHold is returned by Get when no hold is persisted for an entity (e.g. the
// node was launched before admission was enabled, or its hold already
// reconciled). Suspend treats this as "nothing to reconcile", not an error.
var ErrNoHold = errors.New("slurm: no hold for entity")

// FileHoldStore is a filesystem-backed HoldStore, typically rooted at
// <StateDir>/holds.
type FileHoldStore struct {
	Dir string
}

// NewFileHoldStore returns a FileHoldStore rooted at dir, creating it if needed.
func NewFileHoldStore(dir string) (*FileHoldStore, error) {
	if dir == "" {
		return nil, errors.New("slurm holdstore: dir must not be empty")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("slurm holdstore: create dir %s: %w", dir, err)
	}
	return &FileHoldStore{Dir: dir}, nil
}

// Put writes one hold atomically (write-temp-then-rename).
func (s *FileHoldStore) Put(h Hold) error {
	if h.Entity == "" {
		return errors.New("slurm holdstore: Hold.Entity must not be empty")
	}
	data, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return fmt.Errorf("slurm holdstore: marshal hold for %s: %w", h.Entity, err)
	}
	tmp, err := os.CreateTemp(s.Dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("slurm holdstore: temp file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("slurm holdstore: write hold for %s: %w", h.Entity, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("slurm holdstore: close temp for %s: %w", h.Entity, err)
	}
	if err := os.Rename(tmpName, s.path(h.Entity)); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("slurm holdstore: rename hold for %s: %w", h.Entity, err)
	}
	return nil
}

// Get reads one entity's hold, returning ErrNoHold when absent.
func (s *FileHoldStore) Get(entity cohort.EntityID) (Hold, error) {
	data, err := os.ReadFile(s.path(entity))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Hold{}, fmt.Errorf("%w: %s", ErrNoHold, entity)
		}
		return Hold{}, fmt.Errorf("slurm holdstore: read hold for %s: %w", entity, err)
	}
	var h Hold
	if err := json.Unmarshal(data, &h); err != nil {
		return Hold{}, fmt.Errorf("slurm holdstore: unmarshal hold for %s: %w", entity, err)
	}
	return h, nil
}

// Delete removes an entity's hold. Removing an absent hold is not an error
// (reconcile is best-effort and may race a prior cleanup).
func (s *FileHoldStore) Delete(entity cohort.EntityID) error {
	if err := os.Remove(s.path(entity)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("slurm holdstore: delete hold for %s: %w", entity, err)
	}
	return nil
}

func (s *FileHoldStore) path(entity cohort.EntityID) string {
	return filepath.Join(s.Dir, encodeHoldEntity(string(entity))+".json")
}

// encodeHoldEntity makes an entity ID safe as a single filename (percent-encode
// path separators), matching recordstore's defensive encoding.
func encodeHoldEntity(id string) string {
	r := strings.NewReplacer("%", "%25", "/", "%2F", `\`, "%5C")
	return r.Replace(id)
}
