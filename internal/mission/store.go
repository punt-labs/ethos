//go:build !windows

package mission

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

// Store provides CRUD operations for mission contracts on the filesystem.
//
// Mirrors internal/session/store.go: contracts are stored as YAML files in
// the missions subdirectory; write operations use flock for concurrency
// safety; writes are atomic via temp file + rename.
//
// Unlike the session store, mission Store is intentionally global-only —
// contracts are not git-tracked and not layered. Phase 3.2+ may revisit if
// repo-scoped missions become necessary.
type Store struct {
	root string // e.g. ~/.punt-labs/ethos
}

// NewStore creates a Store rooted at the given directory.
func NewStore(root string) *Store {
	return &Store{root: root}
}

// Root returns the store's root directory.
func (s *Store) Root() string { return s.root }

func (s *Store) missionsDir() string {
	return filepath.Join(s.root, "missions")
}

// contractPath builds the YAML path for a mission. The mission ID is run
// through filepath.Base as a defense-in-depth measure: even if a caller
// somehow passed an absolute or traversal-laced ID, only the final
// element survives.
func (s *Store) contractPath(missionID string) string {
	return filepath.Join(s.missionsDir(), filepath.Base(missionID)+".yaml")
}

func (s *Store) lockPath(missionID string) string {
	return filepath.Join(s.missionsDir(), filepath.Base(missionID)+".lock")
}

func (s *Store) logPath(missionID string) string {
	return filepath.Join(s.missionsDir(), filepath.Base(missionID)+".jsonl")
}

// Create persists a new mission contract. The caller must populate
// MissionID and CreatedAt; UpdatedAt is set to CreatedAt on first write
// if empty. A "create" event is appended to the JSONL log.
//
// Validation runs before any disk write. If validation fails, no files
// are touched.
func (s *Store) Create(c *Contract) error {
	if c == nil {
		return fmt.Errorf("contract is nil")
	}
	// Default UpdatedAt to CreatedAt for first write so the field is
	// always populated even when the caller omits it.
	if c.UpdatedAt == "" {
		c.UpdatedAt = c.CreatedAt
	}
	if err := c.Validate(); err != nil {
		return fmt.Errorf("invalid contract: %w", err)
	}
	if err := os.MkdirAll(s.missionsDir(), 0o700); err != nil {
		return fmt.Errorf("creating missions directory: %w", err)
	}

	return s.withLock(c.MissionID, func() error {
		// Refuse to overwrite an existing contract via Create — Update
		// is the explicit mutation path.
		if _, err := os.Stat(s.contractPath(c.MissionID)); err == nil {
			return fmt.Errorf("mission %q already exists", c.MissionID)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("checking mission existence: %w", err)
		}
		if err := s.writeContract(c); err != nil {
			return err
		}
		return s.appendEventLocked(c.MissionID, Event{
			TS:    time.Now().UTC().Format(time.RFC3339),
			Event: "create",
			Actor: c.Leader,
			Details: map[string]any{
				"worker":    c.Worker,
				"evaluator": c.Evaluator.Handle,
				"bead":      c.Bead,
			},
		})
	})
}

// Load reads a mission contract by ID.
//
// Decodes with KnownFields(true) so an attacker who has local write
// access cannot drop extra fields into the on-disk YAML and have them
// silently ignored. Symmetric with the strict create paths.
func (s *Store) Load(missionID string) (*Contract, error) {
	if strings.TrimSpace(missionID) == "" {
		return nil, fmt.Errorf("missionID is required")
	}
	data, err := os.ReadFile(s.contractPath(missionID))
	if err != nil {
		return nil, fmt.Errorf("mission %q not found: %w", missionID, err)
	}
	c, err := decodeContractStrict(data, missionID)
	if err != nil {
		return nil, err
	}
	// Defense in depth: even on read, run Validate. A corrupt or
	// hand-edited contract should be flagged before callers act on it.
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("contract %q failed validation on load: %w", missionID, err)
	}
	return c, nil
}

// Update writes a mutated contract back to disk under flock. The caller
// is responsible for any field mutation; Update bumps UpdatedAt and
// validates before writing.
//
// Update works on a shallow copy of the caller's contract inside the
// lock so that a mid-method failure (stat, validate, write) leaves the
// caller's struct unchanged. On success, UpdatedAt is reflected back
// to the caller — that is the one field Update is contracted to
// mutate. The shallow copy is safe because Validate and writeContract
// never modify any slice or nested struct; value-type sub-structs
// (Evaluator, Inputs, Budget) are deep-copied by the shallow copy
// itself.
func (s *Store) Update(c *Contract) error {
	if c == nil {
		return fmt.Errorf("contract is nil")
	}
	return s.withLock(c.MissionID, func() error {
		if _, err := os.Stat(s.contractPath(c.MissionID)); err != nil {
			return fmt.Errorf("mission %q not found: %w", c.MissionID, err)
		}
		updated := *c
		updated.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		if err := updated.Validate(); err != nil {
			return fmt.Errorf("invalid contract: %w", err)
		}
		if err := s.writeContract(&updated); err != nil {
			return err
		}
		// Success: reflect the new UpdatedAt back to the caller.
		c.UpdatedAt = updated.UpdatedAt
		return s.appendEventLocked(c.MissionID, Event{
			TS:    updated.UpdatedAt,
			Event: "update",
			Actor: updated.Leader,
		})
	})
}

// Close transitions a mission to the given terminal status (closed,
// failed, or escalated), sets ClosedAt, and appends a "close" event.
func (s *Store) Close(missionID, status string) error {
	if !validStatuses[status] || status == StatusOpen {
		return fmt.Errorf("invalid close status %q: must be closed, failed, or escalated", status)
	}
	return s.withLock(missionID, func() error {
		c, err := s.loadLocked(missionID)
		if err != nil {
			return err
		}
		now := time.Now().UTC().Format(time.RFC3339)
		c.Status = status
		c.ClosedAt = now
		c.UpdatedAt = now
		if err := c.Validate(); err != nil {
			return fmt.Errorf("invalid contract after close: %w", err)
		}
		if err := s.writeContract(c); err != nil {
			return err
		}
		return s.appendEventLocked(missionID, Event{
			TS:    now,
			Event: "close",
			Actor: c.Leader,
			Details: map[string]any{
				"status": status,
			},
		})
	})
}

// loadLocked reads a contract without acquiring the flock. Callers must
// already hold the lock for the given missionID.
//
// Decodes with KnownFields(true) and runs Validate() for symmetry with
// the public Load() — a corrupt or hand-edited contract must be
// rejected before Close (or any future locked caller) mutates it.
// Otherwise an invalid on-disk state could slip through Close's
// post-mutation Validate because the mutation fixed the field under
// inspection.
func (s *Store) loadLocked(missionID string) (*Contract, error) {
	data, err := os.ReadFile(s.contractPath(missionID))
	if err != nil {
		return nil, fmt.Errorf("mission %q not found: %w", missionID, err)
	}
	c, err := decodeContractStrict(data, missionID)
	if err != nil {
		return nil, err
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("contract %q failed validation on load: %w", missionID, err)
	}
	return c, nil
}

// decodeContractStrict parses a YAML contract with KnownFields(true).
// Used by Load and loadLocked so the on-disk trust boundary matches
// the strict-decode behavior of the CLI and MCP create paths.
func decodeContractStrict(data []byte, missionID string) (*Contract, error) {
	var c Contract
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("invalid contract file %s: %w", missionID, err)
	}
	return &c, nil
}

// List returns all mission IDs known to the store.
func (s *Store) List() ([]string, error) {
	entries, err := os.ReadDir(s.missionsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading missions directory: %w", err)
	}
	var ids []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".yaml") {
			continue
		}
		// Skip dotfiles like .counter-YYYY-MM-DD.
		if strings.HasPrefix(name, ".") {
			continue
		}
		ids = append(ids, strings.TrimSuffix(name, ".yaml"))
	}
	return ids, nil
}

// MatchByPrefix finds a mission ID from a prefix string. Mirrors
// session.Store.MatchByPrefix: an exact match wins; otherwise the prefix
// must match exactly one ID. Zero or multiple matches are an error.
func (s *Store) MatchByPrefix(prefix string) (string, error) {
	ids, err := s.List()
	if err != nil {
		return "", fmt.Errorf("listing missions: %w", err)
	}
	var matches []string
	for _, id := range ids {
		if id == prefix {
			return id, nil // exact match wins
		}
		if len(id) >= len(prefix) && id[:len(prefix)] == prefix {
			matches = append(matches, id)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no mission matching prefix %q", prefix)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("ambiguous prefix %q: matches %d missions", prefix, len(matches))
	}
}

// writeContract marshals and writes a contract atomically via temp file
// plus rename. The caller must hold the contract's flock.
func (s *Store) writeContract(c *Contract) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshaling contract: %w", err)
	}
	dest := s.contractPath(c.MissionID)
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("writing temp contract: %w", err)
	}
	return os.Rename(tmp, dest)
}

// withLock executes fn while holding an exclusive flock on the mission's
// lock file. Mirrors session.Store.withLock.
func (s *Store) withLock(missionID string, fn func() error) error {
	if strings.TrimSpace(missionID) == "" {
		return fmt.Errorf("missionID is required")
	}
	if err := os.MkdirAll(s.missionsDir(), 0o700); err != nil {
		return fmt.Errorf("creating missions directory: %w", err)
	}
	lockFile := s.lockPath(missionID)
	f, err := os.OpenFile(lockFile, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("opening lock file: %w", err)
	}
	defer f.Close()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("acquiring lock: %w", err)
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()

	return fn()
}
