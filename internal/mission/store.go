//go:build !windows

package mission

import (
	"bytes"
	"errors"
	"fmt"
	"io"
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

// ApplyServerFields sets all server-controlled fields on a contract at
// create time. Both the CLI (`ethos mission create --file`) and the
// MCP `mission create` handler call this before Store.Create so the
// two paths stay in lockstep — server-controlled fields are the
// server's responsibility, and any caller-supplied value is overwritten
// without warning.
//
// Fields set (every one is unconditionally overwritten):
//   - MissionID: always generated via NewID. A caller-supplied
//     mission_id would bypass the daily counter, leaving the counter
//     file stale and risking a later collision when NewID catches up.
//     The server owns this field full stop.
//   - Status: forced to StatusOpen — a newly created mission is always open
//   - CreatedAt: set to now (RFC3339, UTC)
//   - UpdatedAt: set equal to CreatedAt
//   - ClosedAt: cleared (terminal-only field; Validate's status↔closed_at
//     invariant would reject a non-empty value on an open contract anyway)
//   - Evaluator.PinnedAt: set equal to CreatedAt — the evaluator is
//     pinned AT mission launch by definition; any caller-supplied
//     timestamp would be incoherent
//
// Returns an error only if NewID fails to allocate a mission ID
// (daily counter exhausted or poisoned counter file).
func (s *Store) ApplyServerFields(c *Contract, now time.Time) error {
	id, err := NewID(s.root, now)
	if err != nil {
		return fmt.Errorf("generating mission ID: %w", err)
	}
	c.MissionID = id
	created := now.UTC().Format(time.RFC3339)
	c.Status = StatusOpen
	c.CreatedAt = created
	c.UpdatedAt = created
	c.ClosedAt = ""
	c.Evaluator.PinnedAt = created
	return nil
}

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
	// Work on a shallow copy so a validation failure never mutates
	// the caller's struct. The UpdatedAt default-fill and Validate
	// both touch only the copy. On success we reflect the new
	// UpdatedAt back to the caller — the one field Create is
	// contracted to set.
	staged := *c
	if staged.UpdatedAt == "" {
		staged.UpdatedAt = staged.CreatedAt
	}
	if err := staged.Validate(); err != nil {
		return fmt.Errorf("invalid contract: %w", err)
	}
	if err := os.MkdirAll(s.missionsDir(), 0o700); err != nil {
		return fmt.Errorf("creating missions directory: %w", err)
	}

	err := s.withLock(staged.MissionID, func() error {
		dest := s.contractPath(staged.MissionID)
		// Refuse to overwrite an existing contract via Create — Update
		// is the explicit mutation path.
		if _, statErr := os.Stat(dest); statErr == nil {
			return fmt.Errorf("mission %q already exists", staged.MissionID)
		} else if !os.IsNotExist(statErr) {
			return fmt.Errorf("checking mission existence: %w", statErr)
		}
		if err := s.writeContract(&staged); err != nil {
			return err
		}
		if err := s.appendEventLocked(staged.MissionID, Event{
			TS:    time.Now().UTC().Format(time.RFC3339),
			Event: "create",
			Actor: staged.Leader,
			Details: map[string]any{
				"worker":    staged.Worker,
				"evaluator": staged.Evaluator.Handle,
				"bead":      staged.Inputs.Bead,
			},
		}); err != nil {
			// Rollback: remove the just-written contract so the
			// operation is atomic from the caller's point of view.
			// Without rollback, a retry after a log-append failure
			// would hit "already exists" and the caller would have
			// no clean recovery path.
			if rbErr := os.Remove(dest); rbErr != nil && !os.IsNotExist(rbErr) {
				return fmt.Errorf("create: event append failed: %w; rollback failed: %v", err, rbErr)
			}
			return fmt.Errorf("create: event append failed, contract rolled back: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	// Success: reflect the new UpdatedAt back to the caller — the
	// one field Create is contracted to set. A failed Create leaves
	// the caller's struct unchanged.
	c.UpdatedAt = staged.UpdatedAt
	return nil
}

// Load reads a mission contract by ID.
//
// Decodes with KnownFields(true) so an attacker who has local write
// access cannot drop extra fields into the on-disk YAML and have them
// silently ignored. Symmetric with the strict create paths.
//
// Distinguishes "not found" from other read errors (permission denied,
// I/O failure) so operators get an accurate diagnostic instead of a
// misleading "not found" for a file that exists but can't be read.
func (s *Store) Load(missionID string) (*Contract, error) {
	if strings.TrimSpace(missionID) == "" {
		return nil, fmt.Errorf("missionID is required")
	}
	data, err := os.ReadFile(s.contractPath(missionID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("mission %q not found", missionID)
		}
		return nil, fmt.Errorf("reading mission %q: %w", missionID, err)
	}
	c, err := DecodeContractStrict(data, missionID)
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
//
// Atomicity: the new contract is written, then the update event is
// appended. If the event append fails, the original contract is
// restored and the caller's struct is NOT mutated — the method's
// failure semantics match "operation did not happen."
func (s *Store) Update(c *Contract) error {
	if c == nil {
		return fmt.Errorf("contract is nil")
	}
	return s.withLock(c.MissionID, func() error {
		dest := s.contractPath(c.MissionID)
		// Read the current bytes for rollback before touching the file.
		oldData, err := os.ReadFile(dest)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("mission %q not found", c.MissionID)
			}
			return fmt.Errorf("reading mission %q: %w", c.MissionID, err)
		}
		updated := *c
		updated.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		if err := updated.Validate(); err != nil {
			return fmt.Errorf("invalid contract: %w", err)
		}
		if err := s.writeContract(&updated); err != nil {
			return err
		}
		if err := s.appendEventLocked(c.MissionID, Event{
			TS:    updated.UpdatedAt,
			Event: "update",
			Actor: updated.Leader,
		}); err != nil {
			if rbErr := s.restoreContract(dest, oldData); rbErr != nil {
				return fmt.Errorf("update: event append failed: %w; rollback failed: %v", err, rbErr)
			}
			return fmt.Errorf("update: event append failed, contract rolled back: %w", err)
		}
		// Success: reflect the new UpdatedAt back to the caller — this
		// mutation happens only after the event log commits, so a
		// failed Update leaves the caller's struct unchanged.
		c.UpdatedAt = updated.UpdatedAt
		return nil
	})
}

// Close transitions a mission to the given terminal status (closed,
// failed, or escalated), sets ClosedAt, and appends a "close" event.
//
// Atomicity: the new closed state is written, then the close event is
// appended. If the event append fails, the original contract bytes
// are restored — a failed Close leaves the on-disk state unchanged.
func (s *Store) Close(missionID, status string) error {
	if !validStatuses[status] || status == StatusOpen {
		return fmt.Errorf("invalid close status %q: must be closed, failed, or escalated", status)
	}
	return s.withLock(missionID, func() error {
		dest := s.contractPath(missionID)
		// Read the current bytes for rollback before loading the
		// contract into a struct, so we can restore the exact on-disk
		// representation on a failed event append.
		oldData, err := os.ReadFile(dest)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("mission %q not found", missionID)
			}
			return fmt.Errorf("reading mission %q: %w", missionID, err)
		}
		c, err := s.loadLocked(missionID)
		if err != nil {
			return err
		}
		// Refuse to re-close a mission that's already in a terminal
		// state. Re-closing would silently overwrite the original
		// closed_at timestamp and append a duplicate "close" event
		// to the JSONL log, which breaks the log's one-transition-
		// per-event invariant.
		if c.Status != StatusOpen {
			return fmt.Errorf("mission %q is already in terminal state %q", missionID, c.Status)
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
		if err := s.appendEventLocked(missionID, Event{
			TS:    now,
			Event: "close",
			Actor: c.Leader,
			Details: map[string]any{
				"status": status,
			},
		}); err != nil {
			if rbErr := s.restoreContract(dest, oldData); rbErr != nil {
				return fmt.Errorf("close: event append failed: %w; rollback failed: %v", err, rbErr)
			}
			return fmt.Errorf("close: event append failed, contract rolled back: %w", err)
		}
		return nil
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
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("mission %q not found", missionID)
		}
		return nil, fmt.Errorf("reading mission %q: %w", missionID, err)
	}
	c, err := DecodeContractStrict(data, missionID)
	if err != nil {
		return nil, err
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("contract %q failed validation on load: %w", missionID, err)
	}
	return c, nil
}

// DecodeContractStrict parses a YAML contract with strict rules: every
// field must be known to the Contract struct, and exactly one YAML
// document must be present. Multi-document YAML or trailing content
// after the first document is rejected — otherwise a caller could
// smuggle extra content past the trust boundary by appending it to
// a legitimate contract.
//
// This helper is the single entry point for YAML → Contract decoding
// in the mission package. Both the CLI (`ethos mission create --file`)
// and the MCP `mission create` handler use it, as do the Store's
// Load/loadLocked paths, so the on-disk trust boundary matches the
// input trust boundary exactly.
//
// The label argument is a human-readable identifier (mission ID or
// file path) used in error messages to help operators locate the
// source of a parse failure.
func DecodeContractStrict(data []byte, label string) (*Contract, error) {
	var c Contract
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("invalid contract %s: %w", label, err)
	}
	// Enforce single-document input: a second Decode must return
	// io.EOF. Anything else means there was more content — either a
	// second YAML document (separated by `---`) or trailing scalars.
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("invalid contract %s: multiple YAML documents are not allowed", label)
		}
		return nil, fmt.Errorf("invalid contract %s: trailing content after first document: %w", label, err)
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

// restoreContract writes oldData back to dest atomically via temp+rename.
// Used by Update and Close to roll back a contract write when the
// follow-on event-log append fails, keeping the caller's view of
// on-disk state consistent with the operation's success/failure.
func (s *Store) restoreContract(dest string, oldData []byte) error {
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, oldData, 0o600); err != nil {
		return fmt.Errorf("writing rollback temp: %w", err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		return fmt.Errorf("renaming rollback temp: %w", err)
	}
	return nil
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
