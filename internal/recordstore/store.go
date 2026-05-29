// Package recordstore persists cohort.Record artifacts so `q0 explain <entity>`
// can render a reconciliation's outcome after the reconciler process has exited.
//
// Slurm forks ResumeProgram per call (ARCHITECTURE §11): the process that
// reconciles a cohort is short-lived and gone by the time an operator runs
// `q0 explain`. The Record — the legibility artifact every entity carries
// (ARCHITECTURE §10) — must therefore outlive the process. This package is that
// durable seam.
//
// It is a FILE store under the cluster state dir (spec.ControllerSpec.StateDir,
// on durable shared storage — the same place slurmctld save-state lives), one
// JSON file per entity, latest-write-wins. A later phase may back this with the
// accounting DB; the interface (Store) is what callers depend on, not the files.
//
// DISCIPLINE: imports internal/cohort + stdlib ONLY. It is a consumer of the
// core's public types, never a modifier of the core.
package recordstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/queuezero/queuezero/internal/cohort"
)

// Store reads and writes cohort.Records by entity. Both the reconciler (write
// path) and `q0 explain` (read path) depend on this interface, not the files.
type Store interface {
	// Put persists one entity's Record, overwriting any prior record for that
	// entity (latest reconcile wins — an entity reconciled twice shows its most
	// recent outcome).
	Put(rec cohort.Record) error
	// Get returns the persisted Record for an entity. Returns ErrNotFound if no
	// record exists.
	Get(entity cohort.EntityID) (cohort.Record, error)
	// PutOutcome persists every Record in an Outcome in one call.
	PutOutcome(out cohort.Outcome) error
	// List returns all persisted entity IDs, sorted, for `q0 explain` with no arg.
	List() ([]cohort.EntityID, error)
}

// ErrNotFound is returned by Get when no record exists for an entity.
var ErrNotFound = errors.New("recordstore: no record for entity")

// FileStore is the on-disk Store: one JSON file per entity under Dir.
type FileStore struct {
	Dir string
}

// NewFileStore returns a FileStore rooted at dir, creating dir if needed.
// dir is typically <StateDir>/records.
func NewFileStore(dir string) (*FileStore, error) {
	if dir == "" {
		return nil, errors.New("recordstore: dir must not be empty")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("recordstore: create dir %s: %w", dir, err)
	}
	return &FileStore{Dir: dir}, nil
}

// Put writes one record atomically (write-temp-then-rename), so a concurrent
// reader never sees a half-written file.
func (s *FileStore) Put(rec cohort.Record) error {
	if rec.Entity == "" {
		return errors.New("recordstore: Record.Entity must not be empty")
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("recordstore: marshal record for %s: %w", rec.Entity, err)
	}
	final := s.path(rec.Entity)
	tmp, err := os.CreateTemp(s.Dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("recordstore: temp file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("recordstore: write record for %s: %w", rec.Entity, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("recordstore: close temp for %s: %w", rec.Entity, err)
	}
	if err := os.Rename(tmpName, final); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("recordstore: rename record for %s: %w", rec.Entity, err)
	}
	return nil
}

// PutOutcome persists every Record in an Outcome, returning the first error.
func (s *FileStore) PutOutcome(out cohort.Outcome) error {
	for _, rec := range out.Records {
		if err := s.Put(rec); err != nil {
			return err
		}
	}
	return nil
}

// Get reads one entity's Record.
func (s *FileStore) Get(entity cohort.EntityID) (cohort.Record, error) {
	data, err := os.ReadFile(s.path(entity))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cohort.Record{}, fmt.Errorf("%w: %s", ErrNotFound, entity)
		}
		return cohort.Record{}, fmt.Errorf("recordstore: read record for %s: %w", entity, err)
	}
	var rec cohort.Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return cohort.Record{}, fmt.Errorf("recordstore: unmarshal record for %s: %w", entity, err)
	}
	return rec, nil
}

// List returns all persisted entity IDs, sorted.
func (s *FileStore) List() ([]cohort.EntityID, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return nil, fmt.Errorf("recordstore: list %s: %w", s.Dir, err)
	}
	var ids []cohort.EntityID
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") || strings.HasPrefix(name, ".") {
			continue
		}
		ids = append(ids, cohort.EntityID(decodeEntity(strings.TrimSuffix(name, ".json"))))
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, nil
}

// path is the file path for an entity's record. Entity IDs are filesystem-safe
// in practice (Slurm node names, MPI rank labels), but we encode separators
// defensively so an exotic ID cannot escape Dir.
func (s *FileStore) path(entity cohort.EntityID) string {
	return filepath.Join(s.Dir, encodeEntity(string(entity))+".json")
}

// encodeEntity/decodeEntity make an entity ID safe as a single filename
// component. Only '/' is realistically problematic; we percent-style escape it.
func encodeEntity(id string) string {
	return strings.ReplaceAll(id, "/", "%2F")
}

func decodeEntity(name string) string {
	return strings.ReplaceAll(name, "%2F", "/")
}
