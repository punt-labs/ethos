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
	if c == nil {
		return fmt.Errorf("contract is nil")
	}
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

// createLockPath returns the directory-level lock file used by
// Store.Create to serialize cross-mission write_set conflict scans.
// Stable filename, never renamed or unlinked, so the flock inode does
// not race with concurrent acquirers.
func (s *Store) createLockPath() string {
	return filepath.Join(s.missionsDir(), ".create.lock")
}

func (s *Store) logPath(missionID string) string {
	return filepath.Join(s.missionsDir(), filepath.Base(missionID)+".jsonl")
}

// Create persists a new mission contract. The caller must supply a
// fully-populated Contract (the server-controlled fields — MissionID,
// Status, CreatedAt, UpdatedAt, ClosedAt, Evaluator.PinnedAt — can be
// left empty and set via ApplyServerFields before Create, which is
// what the CLI and MCP entry points do). Validate() runs before any
// disk write, so missing required fields (leader, worker, evaluator,
// write_set, success_criteria, budget) produce an error and touch no
// files. UpdatedAt defaults to CreatedAt on first write if empty —
// the one field Create may fill in.
//
// A "create" event is appended to the JSONL log. On event append
// failure the contract file is rolled back so the operation is
// atomic from the caller's point of view.
//
// Works on a shallow copy of c so a validation failure never mutates
// the caller's struct. On success, UpdatedAt is reflected back to
// the caller.
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

	err := s.withCreateLock(func() error {
		return s.withLock(staged.MissionID, func() error {
			dest := s.contractPath(staged.MissionID)
			// Refuse to overwrite an existing contract via Create — Update
			// is the explicit mutation path.
			if _, statErr := os.Stat(dest); statErr == nil {
				return fmt.Errorf("mission %q already exists", staged.MissionID)
			} else if !os.IsNotExist(statErr) {
				return fmt.Errorf("checking mission existence: %w", statErr)
			}
			// Cross-mission write_set conflict check (Phase 3.2). The
			// directory-level create lock above ensures the scan and
			// the subsequent writeContract are atomic with respect to
			// other concurrent Creates: no other Create can pass its
			// own scan after this Create writes its file but before
			// the create lock is released.
			if err := s.checkWriteSetConflicts(&staged); err != nil {
				return err
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
	c, err := decodeAndValidate(data, missionID)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// decodeAndValidate is the shared strict-decode + validate + filename-
// match check used by Load and loadLocked. Factored out so the two
// read paths stay in lockstep. The filename check prevents a
// hand-edited foo.yaml file containing mission_id: m-... from
// being silently accepted — a later Close would write the mutated
// contract to m-....yaml (because writeContract uses c.MissionID),
// producing a second file and leaving foo.yaml stale.
func decodeAndValidate(data []byte, missionID string) (*Contract, error) {
	c, err := DecodeContractStrict(data, missionID)
	if err != nil {
		return nil, err
	}
	// Defense in depth: even on read, run Validate. A corrupt or
	// hand-edited contract should be flagged before callers act on it.
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("contract %q failed validation on load: %w", missionID, err)
	}
	// The on-disk filename must match the contract's own mission_id.
	// Rejects the scenario where a caller passes a filename that
	// doesn't match the contract content (typically a hand-edited
	// file, or a rename that forgot to update the payload).
	if c.MissionID != missionID {
		return nil, fmt.Errorf(
			"contract filename %q does not match mission_id %q in the file",
			missionID, c.MissionID,
		)
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
		// loadLocked returns both the parsed contract and the raw
		// bytes, so Close reads the file only once and keeps the
		// original bytes for rollback if the event append fails.
		c, oldData, err := s.loadLocked(missionID)
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
// already hold the lock for the given missionID. Returns both the parsed
// contract and the raw bytes so callers that need the bytes for rollback
// (Close, Update) don't have to read the file twice.
//
// Decodes with KnownFields(true) and runs Validate() for symmetry with
// the public Load() — a corrupt or hand-edited contract must be
// rejected before Close (or any future locked caller) mutates it.
// Otherwise an invalid on-disk state could slip through Close's
// post-mutation Validate because the mutation fixed the field under
// inspection.
func (s *Store) loadLocked(missionID string) (*Contract, []byte, error) {
	data, err := os.ReadFile(s.contractPath(missionID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, fmt.Errorf("mission %q not found", missionID)
		}
		return nil, nil, fmt.Errorf("reading mission %q: %w", missionID, err)
	}
	c, err := decodeAndValidate(data, missionID)
	if err != nil {
		return nil, nil, err
	}
	return c, data, nil
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

// withCreateLock executes fn while holding an exclusive flock on the
// missions directory's create lock file. Used by Store.Create to
// serialize Create attempts across cooperating processes so that the
// cross-mission write_set conflict scan and the new mission's write
// happen atomically with respect to other concurrent Creates.
//
// Update and Close do NOT acquire this lock — they mutate an existing
// mission's status, which is unrelated to Create-vs-Create
// serialization. The lock file is a stable filename that is never
// renamed or unlinked, so concurrent acquirers always lock the same
// inode.
func (s *Store) withCreateLock(fn func() error) error {
	if err := os.MkdirAll(s.missionsDir(), 0o700); err != nil {
		return fmt.Errorf("creating missions directory: %w", err)
	}
	f, err := os.OpenFile(s.createLockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("opening create lock file: %w", err)
	}
	defer f.Close()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("acquiring create lock: %w", err)
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()

	return fn()
}

// checkWriteSetConflicts loads every existing mission, filters to
// open ones, and asks findWriteSetConflicts whether the new contract's
// write_set overlaps any of them. Returns a non-nil error iff there
// is at least one conflict.
//
// The caller must hold the directory-level create lock so that the
// scan-then-write transition is atomic with respect to other Creates.
//
// A Load failure on any existing mission is fatal — silently skipping
// a corrupt mission would defeat the conflict check.
func (s *Store) checkWriteSetConflicts(c *Contract) error {
	ids, err := s.List()
	if err != nil {
		return fmt.Errorf("create: listing existing missions: %w", err)
	}
	var openContracts []*Contract
	for _, id := range ids {
		// Skip self defensively. The Create caller has already
		// verified the destination file does not exist, so this
		// should never trigger pre-create — but if Create is ever
		// reused for a re-validation path the self-skip prevents a
		// false positive.
		if id == c.MissionID {
			continue
		}
		existing, err := s.Load(id)
		if err != nil {
			return fmt.Errorf("create: failed to load existing mission %q: %w", id, err)
		}
		if existing.Status == StatusOpen {
			openContracts = append(openContracts, existing)
		}
	}
	conflicts := findWriteSetConflicts(c.WriteSet, openContracts)
	if len(conflicts) == 0 {
		return nil
	}
	return formatConflictError(conflicts)
}
