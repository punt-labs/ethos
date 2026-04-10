package adr

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Store provides CRUD operations for ADRs on the filesystem.
type Store struct {
	dir string
}

// NewStore creates a Store that reads and writes ADRs in dir.
func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

// Dir returns the store's directory.
func (s *Store) Dir() string { return s.dir }

// Create persists a new ADR. It assigns the next available DES-NNN ID,
// sets CreatedAt and UpdatedAt to now, and defaults Status to proposed
// if empty. Validate runs before any disk write.
func (s *Store) Create(a *ADR) error {
	if a == nil {
		return fmt.Errorf("ADR is nil")
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("creating ADR directory: %w", err)
	}
	id, err := s.nextID()
	if err != nil {
		return fmt.Errorf("generating ADR ID: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	a.ID = id
	a.CreatedAt = now
	a.UpdatedAt = now
	if a.Status == "" {
		a.Status = StatusProposed
	}
	if err := a.Validate(); err != nil {
		return fmt.Errorf("invalid ADR: %w", err)
	}
	dest := s.path(id)
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("ADR %q already exists", id)
	}
	return s.write(a)
}

// Load reads an ADR by ID. Decodes with KnownFields(true) so unknown
// fields are rejected.
func (s *Store) Load(id string) (*ADR, error) {
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("ADR id is required")
	}
	path := s.path(id)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("ADR %q not found", id)
		}
		return nil, fmt.Errorf("reading ADR %q: %w", id, err)
	}
	a, err := decodeStrict(data, id)
	if err != nil {
		return nil, err
	}
	return a, nil
}

// List returns all ADR IDs in sorted order.
func (s *Store) List() ([]string, error) {
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
func (s *Store) Update(id string, fn func(*ADR)) error {
	a, err := s.Load(id)
	if err != nil {
		return err
	}
	fn(a)
	a.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := a.Validate(); err != nil {
		return fmt.Errorf("invalid ADR after update: %w", err)
	}
	return s.write(a)
}

// path returns the filesystem path for an ADR.
func (s *Store) path(id string) string {
	return filepath.Join(s.dir, filepath.Base(id)+".yaml")
}

// write marshals an ADR to disk via temp file + rename.
func (s *Store) write(a *ADR) error {
	data, err := yaml.Marshal(a)
	if err != nil {
		return fmt.Errorf("marshaling ADR: %w", err)
	}
	dest := s.path(a.ID)
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("writing temp ADR: %w", err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("renaming ADR file: %w", err)
	}
	return nil
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
