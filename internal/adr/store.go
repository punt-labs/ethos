package adr

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

// Store provides CRUD operations for ADRs on the filesystem.
type Store struct {
	dir        string
	policy     layout.Policy
	legacy     *Store // read-only compatibility source; never a mutation target
	repository bool
	// Test seams for commit and post-commit sync failures. Nil uses the OS.
	rename  func(string, string) error
	link    func(string, string) error
	syncDir func(string) error
}

// NewStore creates a Store that reads and writes ADRs in dir.
func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

// Dir returns the store's directory.
func (s *Store) Dir() string { return s.dir }

// NotFoundError distinguishes an absent ADR from an I/O failure after a file
// was observed. Only absence authorizes a compatibility fallback.
type NotFoundError struct {
	ID string
}

func (e *NotFoundError) Error() string { return fmt.Sprintf("ADR %q not found", e.ID) }
func (e *NotFoundError) Unwrap() error { return fs.ErrNotExist }

func isAbsent(err error) bool {
	var absent *NotFoundError
	return errors.As(err, &absent)
}

// Create persists a new ADR. It assigns the next available DES-NNN ID,
// sets CreatedAt and UpdatedAt to now, and defaults Status to proposed
// if empty. Validation runs before any disk write. A *DurabilityError means the
// ADR was installed (and a receives its stored values), but crash durability is
// unconfirmed; other errors leave a unchanged.
func (s *Store) Create(a *ADR) error {
	if a == nil {
		return fmt.Errorf("ADR is nil")
	}
	candidate := clone(a)
	now := time.Now().UTC().Format(time.RFC3339)
	candidate.ID = "DES-001" // provisional until allocation under the store lock
	candidate.CreatedAt = now
	candidate.UpdatedAt = now
	if candidate.Status == "" {
		candidate.Status = StatusProposed
	}
	prepared, say, err := s.prepare(candidate)
	if err != nil {
		return err
	}
	err = s.withMutationLock(func() error {
		id, err := s.nextID()
		if err != nil {
			return fmt.Errorf("generating ADR ID: %w", err)
		}
		prepared.ID = id
		return s.write(prepared, true)
	})
	if !installed(err) {
		return err
	}
	*a = *prepared
	say.Say()
	return err
}

// Load reads an ADR by ID. Decodes with KnownFields(true) so unknown
// fields are rejected.
func (s *Store) Load(id string) (*ADR, error) {
	a, err := s.loadOwn(id)
	if isAbsent(err) && s.legacy != nil {
		return s.legacy.Load(id)
	}
	return a, err
}

// loadOwn never falls back: only a missing repository file permits Load to
// consult legacy storage. Corruption, symlinks and permission failures refuse.
func (s *Store) loadOwn(id string) (*ADR, error) {
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("ADR id is required")
	}
	if !idPattern.MatchString(id) {
		return nil, fmt.Errorf("invalid ADR id: must match DES-NNN")
	}
	if err := s.recordDir(false); err != nil {
		return nil, err
	}
	path := s.path(id)

	// Reject symlinks before reading. Same inline pattern as
	// subagent_start.go; see ethos-jjm for rationale.
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("refusing ADR symlink or non-regular file: %s", path)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("lstat %s: %w", path, err)
	} else {
		return nil, &NotFoundError{ID: id}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading ADR %q: %w", id, err)
	}
	a, err := decodeStrict(data, id)
	if err != nil {
		return nil, err
	}
	if a.ID != id {
		return nil, fmt.Errorf("ADR file %s contains mismatched ID %q", id, a.ID)
	}
	return a, nil
}

// List returns all ADR IDs in sorted order.
func (s *Store) List() ([]string, error) {
	ids, err := s.listOwn()
	if err != nil {
		return nil, err
	}
	if s.legacy != nil {
		legacy, err := s.legacy.List()
		if err != nil {
			return nil, err
		}
		seen := make(map[string]bool, len(ids))
		for _, id := range ids {
			seen[id] = true
		}
		for _, id := range legacy {
			if !seen[id] {
				ids = append(ids, id)
			}
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func (s *Store) listOwn() ([]string, error) {
	if err := s.recordDir(false); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading ADR directory: %w", err)
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".yaml") {
			continue
		}
		if strings.HasPrefix(name, ".") {
			continue
		}
		ids = append(ids, strings.TrimSuffix(name, ".yaml"))
	}
	sort.Strings(ids)
	return ids, nil
}

// Update loads an ADR, applies fn, bumps UpdatedAt, validates, and saves.
// A *DurabilityError means the new ADR is installed; it must not be rolled back.
func (s *Store) Update(id string, fn func(*ADR)) error {
	if fn == nil {
		return fmt.Errorf("ADR update function is nil")
	}
	// An existing repository record is authoritative and needs no global I/O.
	// If it is absent, release this lock before taking the legacy-first pair;
	// then recheck, since a concurrent copy-on-write may have published it.
	err := s.withLock(func() error {
		a, err := s.loadOwn(id)
		if err != nil {
			return err
		}
		return s.updateRecord(id, a, false, fn)
	})
	if !isAbsent(err) || s.legacy == nil {
		return err
	}
	return s.withMutationLock(func() error {
		a, err := s.loadOwn(id)
		create := isAbsent(err)
		if create && s.legacy != nil {
			a, err = s.legacy.Load(id)
		}
		if err != nil {
			return err
		}
		return s.updateRecord(id, a, create, fn)
	})
}

// updateRecord runs with the repository/standalone store lock held. a is a
// decoded snapshot, never a caller-owned candidate or the global source file.
func (s *Store) updateRecord(id string, a *ADR, create bool, fn func(*ADR)) error {
	candidate := clone(a)
	fn(candidate)
	if candidate.ID != id || candidate.CreatedAt != a.CreatedAt {
		return fmt.Errorf("ADR update must preserve ID and created_at")
	}
	candidate.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	prepared, say, err := s.prepare(candidate)
	if err != nil {
		return err
	}
	err = s.write(prepared, create)
	if installed(err) {
		say.Say()
	}
	return err
}

// path returns the filesystem path for an ADR.
func (s *Store) path(id string) string {
	return filepath.Join(s.dir, filepath.Base(id)+".yaml")
}

// write publishes only prepared, validated ADRs. Callers hold the store lock;
// create uses a no-clobber link, replacement uses an atomic rename.
func (s *Store) write(a *ADR, create bool) error {
	rewritten, err := s.writeQuiet(a, create)
	if installed(err) && rewritten {
		announcement(func() { fmt.Fprintln(os.Stderr, "ethos: redact: "+rewriteNotice) }).Say()
	}
	return err
}

// writeQuiet is write without the stderr notice. It reports whether the
// redactor changed the record on the way to disk so the caller can say so
// where it chooses: stderr for a command, a log line for the repair engine.
func (s *Store) writeQuiet(a *ADR, create bool) (bool, error) {
	// Keep redaction at the sole ADR sink as well as candidate preparation.
	// New mutation paths cannot accidentally marshal an unredacted record.
	prepared, say, err := s.prepare(a)
	if err != nil {
		return false, err
	}
	if err := s.recordDir(true); err != nil {
		return false, err
	}
	data, err := yaml.Marshal(prepared)
	if err != nil {
		return false, fmt.Errorf("marshaling ADR: %w", err)
	}
	err = s.publish(s.path(prepared.ID), data, create)
	return say != nil, err
}

// nextID scans existing ADRs and returns the next DES-NNN ID.
func (s *Store) nextID() (string, error) {
	ids, err := s.List()
	if err != nil {
		return "", err
	}
	highest := 0
	for _, id := range ids {
		n, err := parseIDNumber(id)
		if err != nil {
			continue
		}
		if n > highest {
			highest = n
		}
	}
	next := highest + 1
	if next > 999 {
		return "", fmt.Errorf("ADR ID counter exhausted (max 999)")
	}
	return fmt.Sprintf("DES-%03d", next), nil
}

// parseIDNumber extracts the numeric part from a DES-NNN ID.
func parseIDNumber(id string) (int, error) {
	if !strings.HasPrefix(id, "DES-") {
		return 0, fmt.Errorf("not a DES ID: %s", id)
	}
	return strconv.Atoi(strings.TrimPrefix(id, "DES-"))
}

// decodeStrict parses YAML with KnownFields(true) and validates.
func decodeStrict(data []byte, label string) (*ADR, error) {
	var a ADR
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&a); err != nil {
		return nil, fmt.Errorf("invalid ADR %s: %w", label, err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("invalid ADR %s: multiple YAML documents are not allowed", label)
		}
		return nil, fmt.Errorf("invalid ADR %s: trailing content: %w", label, err)
	}
	if err := a.Validate(); err != nil {
		return nil, fmt.Errorf("ADR %s failed validation: %w", label, err)
	}
	return &a, nil
}
