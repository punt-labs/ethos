//go:build !windows

package mission

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

// ErrEvaluatorNotFound wraps the underlying identity-store "not found"
// error so ApplyServerFields can return a single-line operator-facing
// message instead of the deeply-wrapped error chain the identity,
// mission, and hash-sources layers would otherwise produce.
//
// Sentinel error so callers can check via errors.Is; the concrete
// error carries the handle in its message for diagnostics.
var ErrEvaluatorNotFound = errors.New("evaluator not found")

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
	root  string     // e.g. ~/.punt-labs/ethos
	roles RoleLister // optional; wires the Phase 3.5 role-overlap check
}

// NewStore creates a Store rooted at the given directory.
func NewStore(root string) *Store {
	return &Store{root: root}
}

// WithRoleLister wires a RoleLister for the Phase 3.5 role-overlap
// check. When set, Store.Create refuses a contract whose worker and
// evaluator share a team-scoped role binding or a role slug after
// canonicalization — see checkRoleOverlap.
//
// The method is opt-in so existing unit tests that build a bare Store
// keep working; CLI and MCP construction wires the lister via the
// live identity, role, and team stores. A nil lister disables the
// check entirely (the worker != evaluator handle guard in
// checkRoleConflicts still runs).
//
// Returns the receiver so construction stays compact:
//
//	ms := mission.NewStore(root).WithRoleLister(rl)
func (s *Store) WithRoleLister(r RoleLister) *Store {
	s.roles = r
	return s
}

// Root returns the store's root directory.
func (s *Store) Root() string { return s.root }

// ContractPath returns the absolute path to a mission contract file
// on disk. Exposed so the Phase 3.5 verifier context-isolation path
// can read the contract byte-for-byte before injecting it into the
// verifier subagent — the invariant is "the contract the verifier
// sees is the contract pinned on disk, no re-serialization allowed".
//
// Mission ID is run through filepath.Base as defense in depth, the
// same way contractPath does internally. A caller passing a relative
// or traversal-laced ID will only ever get a path under missionsDir.
func (s *Store) ContractPath(missionID string) string {
	return s.contractPath(missionID)
}

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
//   - Evaluator.Hash: computed via ComputeEvaluatorHash from the live
//     identity, attribute, and role stores (DES-033). An unresolvable
//     evaluator handle is fatal — Phase 3.1 left this field empty as
//     a placeholder; Phase 3.3 fills it. The hash is the trust anchor
//     the verifier subagent (3.5) compares against on every spawn.
//
// Returns an error if NewID fails to allocate a mission ID (daily
// counter exhausted or poisoned counter file) or if the evaluator
// handle cannot be resolved to identity content.
func (s *Store) ApplyServerFields(c *Contract, now time.Time, sources HashSources) error {
	if c == nil {
		return fmt.Errorf("contract is nil")
	}
	if err := sources.Validate(); err != nil {
		return fmt.Errorf("apply server fields: %w", err)
	}
	if strings.TrimSpace(c.Evaluator.Handle) == "" {
		return fmt.Errorf("apply server fields: evaluator.handle is required before hashing")
	}
	hash, err := ComputeEvaluatorHash(c.Evaluator.Handle, sources)
	if err != nil {
		// Detect the specific "identity YAML file does not exist" case
		// and collapse the 6-level wrapped chain into a single clean
		// operator message. All other hash errors (permission denied,
		// partial talent content, role store corruption) keep their
		// wrapped chain because the chain carries diagnostic value.
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf(
				"apply server fields: %w %q (use `ethos identity list` to see available handles)",
				ErrEvaluatorNotFound, c.Evaluator.Handle,
			)
		}
		return fmt.Errorf("apply server fields: %w", err)
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
	c.Evaluator.Hash = hash
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

// reflectionsPath returns the sibling YAML file that holds the
// round-by-round reflections for a mission. Reflections live next to
// the contract, not inside it: the contract is the trust boundary
// pinned at launch, and a growing array of reflections would force
// every Update to rewrite an unbounded slice. The sibling file grows
// as rounds happen and is the single source of truth for the
// round-advance gate.
func (s *Store) reflectionsPath(missionID string) string {
	return filepath.Join(s.missionsDir(), filepath.Base(missionID)+".reflections.yaml")
}

// resultsPath returns the sibling YAML file that holds the
// round-by-round worker results for a mission. Results live next to
// the contract for the same reasons reflections do: the contract is
// the trust boundary pinned at launch, and the growing list of
// results would force every Update to rewrite an unbounded slice.
// Phase 3.6's Close gate reads this file to decide whether a
// terminal transition is allowed.
//
// The filename MUST be filtered out by isContractFile. Phase 3.4's
// round-2 BLOCKER was caused by Store.List treating a sibling file
// as a contract; adding a second sibling without teaching List about
// it would reproduce the same regression for anyone with a result
// file on disk.
func (s *Store) resultsPath(missionID string) string {
	return filepath.Join(s.missionsDir(), filepath.Base(missionID)+".results.yaml")
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
	// 3.4: a freshly created mission begins at round 1. The caller
	// may leave CurrentRound at its zero value; Validate would
	// otherwise reject the staged contract for being out of [1, N].
	// Default-filling here keeps the caller's struct unchanged on
	// failure (the shallow copy is what Validate sees) and lets a
	// pre-3.4 client that doesn't know about the field still produce
	// a well-formed contract.
	if staged.CurrentRound == 0 {
		staged.CurrentRound = 1
	}
	if err := staged.Validate(); err != nil {
		return fmt.Errorf("invalid contract: %w", err)
	}

	// Phase 3.5: worker-verifier role distinction.
	//
	// The worker != evaluator handle guard is a cheap structural
	// check that runs before any lock is taken — a contract that
	// names the same handle for both slots can never be repaired,
	// and the caller deserves a fast error. The role-overlap check
	// runs inside the create lock so it sees the same store state
	// as checkWriteSetConflicts.
	if err := checkSelfVerification(&staged); err != nil {
		return err
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
			// Phase 3.5: role-overlap check. Runs only when a RoleLister
			// is wired via WithRoleLister — tests that build a bare
			// Store skip the check. CLI and MCP construction always
			// wires the lister so production callers cannot opt out.
			if s.roles != nil {
				if err := checkRoleOverlap(s.roles, &staged); err != nil {
					return err
				}
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
	// Success: reflect the new UpdatedAt and the (possibly defaulted)
	// CurrentRound back to the caller. These are the two fields Create
	// is contracted to set. A failed Create leaves the caller's struct
	// unchanged.
	c.UpdatedAt = staged.UpdatedAt
	c.CurrentRound = staged.CurrentRound
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
	// 3.4: pre-3.4 contracts on disk have no current_round line and
	// decode to CurrentRound == 0. Default-fill on read keeps the
	// upgrade path clean — a mission created by 3.3 still loads in
	// 3.4 — and the in-memory invariant (1 ≤ CurrentRound ≤
	// Budget.Rounds) is enforced by the Validate call below for
	// every other failure mode.
	if c.CurrentRound == 0 {
		c.CurrentRound = 1
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
// Phase 3.6: Close is gated on a result artifact for the mission's
// current round. The gate fires unconditionally at the store
// boundary — neither the CLI nor MCP can bypass it, because the
// refusal lives here, not in the entry-point code. The refusal
// message names the mission, the missing round, and the submission
// command the operator should run. There is no override flag: the
// point of the gate is the invariant.
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
		// Phase 3.6: result gate. A mission cannot transition to a
		// terminal status unless the current round has a valid result
		// artifact on disk. The gate is the whole point of the phase:
		// the leader's final verdict must be backed by the worker's
		// structured output, not prose left in chat.
		//
		// The gate returns the satisfying result so the close event
		// can record the round number and verdict — round 2 of Phase
		// 3.6 added this so an auditor reading the JSONL does not
		// have to scan back across round_advanced events to
		// reconstruct which result authorized the terminal transition.
		satisfying, err := s.checkResultGateLocked(c)
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
		closeDetails := map[string]any{
			"status":  status,
			"round":   satisfying.Round,
			"verdict": satisfying.Verdict,
		}
		if err := s.appendEventLocked(missionID, Event{
			TS:      now,
			Event:   "close",
			Actor:   c.Leader,
			Details: closeDetails,
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
		if !isContractFile(name) {
			continue
		}
		ids = append(ids, strings.TrimSuffix(name, ".yaml"))
	}
	return ids, nil
}

// isContractFile reports whether a missions-directory entry name is a
// mission contract YAML file. A contract file ends in ".yaml" but is
// not a sibling file (".reflections.yaml", ".results.yaml", and any
// future ".annotations.yaml" / ".notes.yaml" the package grows) and
// is not a dotfile such as ".counter-YYYY-MM-DD" or ".create.lock".
//
// Centralizes the decision so future sibling file layouts add one
// case here rather than re-finding the same filtering bug in every
// reader. The catastrophic Phase 3.4 regression was a List() that
// did not exclude ".reflections.yaml", which made every mission with
// a reflection look like two missions — breaking Create's cross-
// mission conflict scan, Show's prefix match, and List's decode.
// Phase 3.6 adds ".results.yaml" to the same filter so a result file
// on disk cannot reproduce that failure mode.
func isContractFile(name string) bool {
	if strings.HasPrefix(name, ".") {
		return false
	}
	if !strings.HasSuffix(name, ".yaml") {
		return false
	}
	if strings.HasSuffix(name, ".reflections.yaml") {
		return false
	}
	if strings.HasSuffix(name, ".results.yaml") {
		return false
	}
	return true
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

// --- 3.4: reflections and round advance ---

// reflectionsFile is the on-disk schema for the sibling
// .reflections.yaml file. The single Round-keyed sequence is the
// shape callers see; the wrapper struct exists so the file format
// can grow new top-level metadata without breaking decode.
//
// Two ordering invariants hold for the on-disk slice (and the helper
// methods enforce them on every write):
//
//  1. Reflections are sorted by Round ascending. The store rewrites
//     the file on every Append, and the rewrite preserves order.
//  2. Each Round value appears at most once. AppendReflection
//     refuses to add a duplicate, so the slice is dense.
type reflectionsFile struct {
	Reflections []Reflection `yaml:"reflections"`
}

// LoadReflections returns the reflections recorded for a mission, in
// round order. Missing file → empty slice; the absence of any
// reflection is the normal state for a brand-new round 1 mission.
//
// Decodes with KnownFields(true) so a hand-edited reflections file
// cannot smuggle extra keys past the trust boundary, symmetric with
// the contract decode path.
func (s *Store) LoadReflections(missionID string) ([]Reflection, error) {
	if strings.TrimSpace(missionID) == "" {
		return nil, fmt.Errorf("missionID is required")
	}
	data, err := os.ReadFile(s.reflectionsPath(missionID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading reflections for %q: %w", missionID, err)
	}
	parsed, err := decodeReflectionsFile(data, missionID)
	if err != nil {
		return nil, err
	}
	return parsed, nil
}

// decodeReflectionsFile parses a reflections.yaml body, runs Validate
// on every entry, and asserts the round-monotone invariant. Returns
// the decoded slice (nil if the file is empty/blank).
func decodeReflectionsFile(data []byte, missionID string) ([]Reflection, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	var wrapper reflectionsFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&wrapper); err != nil {
		return nil, fmt.Errorf("invalid reflections file %q: %w", missionID, err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("invalid reflections file %q: multiple YAML documents are not allowed", missionID)
		}
		return nil, fmt.Errorf("invalid reflections file %q: trailing content: %w", missionID, err)
	}
	for i := range wrapper.Reflections {
		r := &wrapper.Reflections[i]
		if err := r.Validate(); err != nil {
			return nil, fmt.Errorf("reflections[%d] for %q: %w", i, missionID, err)
		}
		if i > 0 && wrapper.Reflections[i-1].Round >= r.Round {
			return nil, fmt.Errorf(
				"reflections file %q is out of order or has duplicate round: %d after %d",
				missionID, r.Round, wrapper.Reflections[i-1].Round,
			)
		}
	}
	return wrapper.Reflections, nil
}

// AppendReflection records a reflection for a mission's current round.
// The append is append-only by construction: a duplicate Round is
// refused, and the file is rewritten via temp+rename so a partial
// write cannot leave a half-encoded YAML doc on disk.
//
// The caller-provided Reflection.Round must equal Contract.CurrentRound
// (the round the worker is currently in). Submitting a reflection for
// any other round is a programming error and is refused — the gate
// would otherwise have to chase out-of-order reflections at advance
// time, where the operator-facing error is much further from the bug.
//
// Validate runs before any disk I/O. CreatedAt is set to now if the
// caller left it blank. The reflect event is appended to the JSONL
// log inside the per-mission flock so concurrent advance/reflect
// attempts on the same mission serialize cleanly.
//
// Atomic from the caller's view: a write failure leaves the on-disk
// reflections file unchanged.
func (s *Store) AppendReflection(missionID string, r *Reflection) error {
	if r == nil {
		return fmt.Errorf("reflection is nil")
	}
	staged := *r
	// Normalize Author before persisting so whitespace around the
	// handle does not pollute the audit trail or the event log.
	// Parity with AppendResult — Phase 3.6 round 2 widened the class
	// fix to both sibling stores so the two surfaces stay symmetric.
	staged.Author = strings.TrimSpace(staged.Author)
	if strings.TrimSpace(staged.CreatedAt) == "" {
		staged.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if err := staged.Validate(); err != nil {
		return fmt.Errorf("invalid reflection: %w", err)
	}
	return s.withLock(missionID, func() error {
		c, _, err := s.loadLocked(missionID)
		if err != nil {
			return err
		}
		// Refuse to record a reflection on a closed mission. The
		// round-advance gate is also closed-status-aware, but rejecting
		// here gives a clearer diagnostic than "advance refused" later.
		if c.Status != StatusOpen {
			return fmt.Errorf("mission %q is in terminal state %q; reflections are accepted only on open missions", missionID, c.Status)
		}
		if staged.Round != c.CurrentRound {
			return fmt.Errorf(
				"reflection round %d does not match mission %q current round %d",
				staged.Round, missionID, c.CurrentRound,
			)
		}
		existing, err := s.loadReflectionsLocked(missionID)
		if err != nil {
			return err
		}
		for _, e := range existing {
			if e.Round == staged.Round {
				return fmt.Errorf(
					"reflection for round %d of mission %q already exists; reflections are append-only",
					staged.Round, missionID,
				)
			}
		}
		updated := append(existing, staged)
		if err := s.writeReflectionsLocked(missionID, updated); err != nil {
			return err
		}
		if err := s.appendEventLocked(missionID, Event{
			TS:    staged.CreatedAt,
			Event: "reflect",
			Actor: staged.Author,
			Details: map[string]any{
				"round":          staged.Round,
				"recommendation": staged.Recommendation,
				"converging":     staged.Converging,
				"signal_count":   len(staged.Signals),
			},
		}); err != nil {
			// Roll back the reflections file so the mission's on-disk
			// state matches the operation's failure: if the event log
			// rejects the reflect record, the reflection itself must
			// not be observable to a later read.
			if rbErr := s.writeReflectionsLocked(missionID, existing); rbErr != nil {
				return fmt.Errorf("reflect: event append failed: %w; rollback failed: %v", err, rbErr)
			}
			return fmt.Errorf("reflect: event append failed, reflection rolled back: %w", err)
		}
		// Reflect CreatedAt back to the caller — this is the one field
		// AppendReflection is contracted to set. Author is always
		// caller-supplied; the default-fill is restricted to CreatedAt
		// so the caller's Reflection.Author never changes behind its
		// back.
		r.CreatedAt = staged.CreatedAt
		return nil
	})
}

// loadReflectionsLocked is the lock-respecting twin of LoadReflections.
// The caller must already hold the per-mission flock; AppendReflection
// and AdvanceRound use it to read the existing slice without
// re-acquiring the lock and deadlocking.
func (s *Store) loadReflectionsLocked(missionID string) ([]Reflection, error) {
	data, err := os.ReadFile(s.reflectionsPath(missionID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading reflections for %q: %w", missionID, err)
	}
	return decodeReflectionsFile(data, missionID)
}

// writeReflectionsLocked rewrites the reflections file via temp+rename.
// Caller must hold the per-mission flock. The file is wrapped in a
// reflectionsFile struct so future top-level metadata (e.g. a
// schema_version key) can be added without breaking decode of older
// files.
func (s *Store) writeReflectionsLocked(missionID string, rs []Reflection) error {
	wrapper := reflectionsFile{Reflections: rs}
	data, err := yaml.Marshal(&wrapper)
	if err != nil {
		return fmt.Errorf("marshaling reflections: %w", err)
	}
	dest := s.reflectionsPath(missionID)
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("writing temp reflections: %w", err)
	}
	return os.Rename(tmp, dest)
}

// AdvanceRound is the round-advance gate. It moves a mission from
// round N to round N+1, refusing the move if any of the 3.4
// invariants is violated:
//
//  1. The mission is not open. Closed/failed/escalated missions are
//     out of the gate's purview.
//  2. The current round (N) has no reflection on disk. Reflection is
//     mandatory between rounds — that is the whole point of 3.4.
//  3. The current round's reflection recommended `stop` or
//     `escalate`. The gate surfaces the leader's reason verbatim so
//     the operator sees the leader's own words.
//  4. Advancing would exceed Budget.Rounds. The contract is the
//     trust boundary; the budget is load-bearing.
//
// On success, Contract.CurrentRound is bumped, the contract is
// rewritten, and a `round_advanced` event is appended to the log.
// The transition is atomic with respect to other operations on the
// same mission via the per-mission flock.
//
// Returns the new round number on success.
func (s *Store) AdvanceRound(missionID, actor string) (int, error) {
	if strings.TrimSpace(missionID) == "" {
		return 0, fmt.Errorf("missionID is required")
	}
	if strings.TrimSpace(actor) == "" {
		return 0, fmt.Errorf("actor is required")
	}
	var newRound int
	err := s.withLock(missionID, func() error {
		c, oldData, err := s.loadLocked(missionID)
		if err != nil {
			return err
		}
		if c.Status != StatusOpen {
			return fmt.Errorf("mission %q is in terminal state %q; cannot advance round", missionID, c.Status)
		}
		// Budget exhaustion check happens before the reflection check
		// so the operator sees the right diagnostic when they have
		// reflected on the final round and then tried to push past
		// the budget anyway. The right next step there is to close
		// the mission, not to record one more reflection.
		if c.CurrentRound >= c.Budget.Rounds {
			return fmt.Errorf(
				"mission %q has exhausted its round budget (%d/%d); close or re-scope",
				missionID, c.CurrentRound, c.Budget.Rounds,
			)
		}
		reflections, err := s.loadReflectionsLocked(missionID)
		if err != nil {
			return err
		}
		var current *Reflection
		for i := range reflections {
			if reflections[i].Round == c.CurrentRound {
				current = &reflections[i]
				break
			}
		}
		if current == nil {
			return fmt.Errorf(
				"mission %q has no reflection for round %d; submit one before advancing",
				missionID, c.CurrentRound,
			)
		}
		if IsTerminalRecommendation(current.Recommendation) {
			return fmt.Errorf(
				"mission %q round %d reflection recommends %q: %s",
				missionID, c.CurrentRound, current.Recommendation, current.Reason,
			)
		}
		// All gates passed; commit the bump.
		dest := s.contractPath(missionID)
		c.CurrentRound++
		c.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		if err := c.Validate(); err != nil {
			return fmt.Errorf("invalid contract after advance: %w", err)
		}
		if err := s.writeContract(c); err != nil {
			return err
		}
		if err := s.appendEventLocked(missionID, Event{
			TS:    c.UpdatedAt,
			Event: "round_advanced",
			Actor: actor,
			Details: map[string]any{
				"from_round":     c.CurrentRound - 1,
				"to_round":       c.CurrentRound,
				"recommendation": current.Recommendation,
			},
		}); err != nil {
			if rbErr := s.restoreContract(dest, oldData); rbErr != nil {
				return fmt.Errorf("advance: event append failed: %w; rollback failed: %v", err, rbErr)
			}
			return fmt.Errorf("advance: event append failed, contract rolled back: %w", err)
		}
		newRound = c.CurrentRound
		return nil
	})
	if err != nil {
		return 0, err
	}
	return newRound, nil
}

// checkSelfVerification refuses a contract that names the same handle
// as both worker and evaluator. This is Phase 3.5's weakest role
// invariant — a pure field comparison, no lookups — and runs before
// any lock is taken so the caller sees a fast failure.
//
// The caller can always rename one side; the error names both slots
// so the fix is obvious.
func checkSelfVerification(c *Contract) error {
	worker := strings.TrimSpace(c.Worker)
	evaluator := strings.TrimSpace(c.Evaluator.Handle)
	if worker == "" || evaluator == "" {
		// Empty fields are a Validate concern, not a role concern.
		return nil
	}
	if worker == evaluator {
		return fmt.Errorf(
			"mission %q: worker %q cannot also be evaluator; assign a distinct reviewer (the verifier must not review its own work)",
			c.MissionID, worker,
		)
	}
	return nil
}

// checkRoleOverlap refuses a contract whose worker and evaluator share
// a team-scoped role binding OR a role slug after canonicalization.
//
// The invariant is Phase 3.5's load-bearing distinction: roles are
// interfaces, and two identities bound to the same role on the same
// team have identical responsibilities — they cannot verify each
// other's work any more meaningfully than one identity can verify
// itself.
//
// Canonicalization rules (see canonicalRoleSlug):
//   - `engineering/go-specialist` and `engineering/go-specialist`
//     overlap (same team-scoped binding).
//   - `engineering/go-specialist` and `security/go-specialist`
//     overlap (same role slug regardless of team).
//   - `engineering/go-specialist` and `engineering/security-reviewer`
//     do NOT overlap (same team, different role).
//   - Identity on no teams → no overlap. An empty binding set is a
//     pass; the check is for ACTIVE role coincidence, not for
//     missing metadata.
//
// Errors name both handles and every overlapping binding so the
// operator can edit the team membership or rename one side.
func checkRoleOverlap(roles RoleLister, c *Contract) error {
	if roles == nil {
		return nil
	}
	worker := strings.TrimSpace(c.Worker)
	evaluator := strings.TrimSpace(c.Evaluator.Handle)
	if worker == "" || evaluator == "" {
		return nil
	}
	// checkSelfVerification already caught worker == evaluator; this
	// helper is only ever called after that gate.

	workerRoles, err := roles.ListRoles(worker)
	if err != nil {
		return fmt.Errorf("role overlap check: listing roles for worker %q: %w", worker, err)
	}
	evaluatorRoles, err := roles.ListRoles(evaluator)
	if err != nil {
		return fmt.Errorf("role overlap check: listing roles for evaluator %q: %w", evaluator, err)
	}
	if len(workerRoles) == 0 || len(evaluatorRoles) == 0 {
		return nil
	}

	// Build the worker's full binding set and its canonical role-slug
	// set. Two passes over the evaluator's roles check both overlap
	// flavors and collect every offending binding so the error lists
	// them all, not just the first one found.
	workerBindings := make(map[string]struct{}, len(workerRoles))
	workerSlugs := make(map[string]string, len(workerRoles))
	for _, r := range workerRoles {
		workerBindings[r.Name] = struct{}{}
		slug := canonicalRoleSlug(r.Name)
		if slug != "" {
			workerSlugs[slug] = r.Name
		}
	}

	type overlap struct {
		workerBinding    string
		evaluatorBinding string
	}
	var overlaps []overlap
	for _, r := range evaluatorRoles {
		if _, ok := workerBindings[r.Name]; ok {
			// Same team/role exactly: the stronger of the two
			// collision flavors. Record with the worker and
			// evaluator both naming the same binding.
			overlaps = append(overlaps, overlap{workerBinding: r.Name, evaluatorBinding: r.Name})
			continue
		}
		slug := canonicalRoleSlug(r.Name)
		if slug == "" {
			continue
		}
		if workerBinding, ok := workerSlugs[slug]; ok {
			overlaps = append(overlaps, overlap{workerBinding: workerBinding, evaluatorBinding: r.Name})
		}
	}
	if len(overlaps) == 0 {
		return nil
	}
	sort.Slice(overlaps, func(i, j int) bool {
		if overlaps[i].workerBinding != overlaps[j].workerBinding {
			return overlaps[i].workerBinding < overlaps[j].workerBinding
		}
		return overlaps[i].evaluatorBinding < overlaps[j].evaluatorBinding
	})
	var lines []string
	// Singular/plural split: "1 overlapping role assignment" vs
	// "N overlapping role assignments". The bare "(s)" reads awkwardly
	// in operator output — render the correct word for the count.
	noun := "assignments"
	if len(overlaps) == 1 {
		noun = "assignment"
	}
	lines = append(lines, fmt.Sprintf(
		"mission %q: worker %q and evaluator %q share %d overlapping role %s; the verifier must not share a role with the worker",
		c.MissionID, worker, evaluator, len(overlaps), noun,
	))
	for _, o := range overlaps {
		if o.workerBinding == o.evaluatorBinding {
			lines = append(lines, fmt.Sprintf(
				"  both bound to %q (same team, same role)",
				o.workerBinding,
			))
		} else {
			lines = append(lines, fmt.Sprintf(
				"  worker bound to %q, evaluator bound to %q (same role slug after canonicalization)",
				o.workerBinding, o.evaluatorBinding,
			))
		}
	}
	lines = append(lines, "  recovery: assign the evaluator to a distinct role, or name a different evaluator")
	return errors.New(strings.Join(lines, "\n"))
}

// canonicalRoleSlug extracts the role slug from a RoleLister binding
// name of the form "team/role". The team prefix is stripped so two
// identities bound to the same role on different teams still compare
// equal. A name with no slash (legacy or hand-built) is returned
// as-is. An empty name returns "".
//
// Stripping uses the LAST slash so future multi-level team paths
// (e.g. `engineering/subgroup/go-specialist`) still yield the right
// slug. The existing liveRoleLister emits `team/role`, single-slash;
// this helper accepts both shapes.
func canonicalRoleSlug(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[i+1:]
	}
	return name
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

// --- 3.6: result artifacts and close gate ---

// resultsFile is the on-disk schema for the sibling .results.yaml
// file. The single Round-keyed sequence mirrors reflectionsFile so
// the two sibling layouts stay symmetric: one file per mission,
// round-sorted slice, append-only discipline enforced at write time.
//
// Two ordering invariants hold for the on-disk slice (and the helper
// methods enforce them on every write):
//
//  1. Results are sorted by Round ascending.
//  2. Each Round value appears at most once — AppendResult refuses
//     to add a duplicate, so the slice is dense.
type resultsFile struct {
	Results []Result `yaml:"results"`
}

// LoadResults returns every result recorded for a mission, in round
// order. Missing file → empty slice; the absence of any result is
// the normal state for a freshly created mission.
//
// Decodes with KnownFields(true) so a hand-edited results file
// cannot smuggle extra keys past the trust boundary, symmetric with
// the contract and reflection decode paths.
func (s *Store) LoadResults(missionID string) ([]Result, error) {
	if strings.TrimSpace(missionID) == "" {
		return nil, fmt.Errorf("missionID is required")
	}
	data, err := os.ReadFile(s.resultsPath(missionID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading results for %q: %w", missionID, err)
	}
	return decodeResultsFile(data, missionID)
}

// LoadResult returns the result for a specific round of a mission,
// or (nil, nil) if no result has been submitted for that round. The
// Phase 3.6 close gate uses this method to decide whether a terminal
// transition is permitted.
//
// A nil return with nil error means "no result on file for this
// round"; the caller interprets that as "gate refuses". Any other
// error — decode failure, I/O failure — is propagated so a corrupt
// results file produces a loud diagnostic rather than a silent gate
// bypass.
func (s *Store) LoadResult(missionID string, round int) (*Result, error) {
	results, err := s.LoadResults(missionID)
	if err != nil {
		return nil, err
	}
	for i := range results {
		if results[i].Round == round {
			return &results[i], nil
		}
	}
	return nil, nil
}

// decodeResultsFile parses a results.yaml body, runs Validate on
// every entry, and asserts the round-monotone invariant. Returns
// the decoded slice (nil if the file is empty/blank).
func decodeResultsFile(data []byte, missionID string) ([]Result, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	var wrapper resultsFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&wrapper); err != nil {
		return nil, fmt.Errorf("invalid results file %q: %w", missionID, err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("invalid results file %q: multiple YAML documents are not allowed", missionID)
		}
		return nil, fmt.Errorf("invalid results file %q: trailing content: %w", missionID, err)
	}
	for i := range wrapper.Results {
		r := &wrapper.Results[i]
		if err := r.Validate(); err != nil {
			return nil, fmt.Errorf("results[%d] for %q: %w", i, missionID, err)
		}
		// On-disk trust symmetry with AppendResult: the write path
		// refuses a result whose self-declared Mission does not match
		// the target mission, but until Phase 3.6 round 5 the read
		// path did not. An attacker with local write access could
		// hand-edit <mission-A>.results.yaml to contain a result
		// claiming mission-B, and the close gate would accept it as
		// long as the round number matched. Reject the mismatch here
		// so the two surfaces enforce the same invariant.
		if r.Mission != missionID {
			return nil, fmt.Errorf(
				"results[%d].mission: expected %q, got %q",
				i, missionID, r.Mission,
			)
		}
		if i > 0 && wrapper.Results[i-1].Round >= r.Round {
			return nil, fmt.Errorf(
				"results file %q is out of order or has duplicate round: %d after %d",
				missionID, r.Round, wrapper.Results[i-1].Round,
			)
		}
	}
	return wrapper.Results, nil
}

// AppendResult records a worker result for a mission's current round.
// The append is append-only by construction: a duplicate Round is
// refused, and the file is rewritten via temp+rename so a partial
// write cannot leave a half-encoded YAML doc on disk.
//
// The caller-provided Result.Round must equal Contract.CurrentRound
// (the round the worker is currently in). Submitting a result for
// any other round is a programming error and is refused — the close
// gate would otherwise have to chase out-of-order results at close
// time, where the operator-facing error is much further from the bug.
//
// Result.Mission must match the caller-supplied missionID; the
// cross-check exists so a file cut loose from its mission cannot
// slip past the gate by claiming the wrong parent.
//
// files_changed paths are cross-checked against the contract's
// write_set using pathContainedBy (asymmetric segment-prefix). A
// path outside the allowlist is a fatal error; the error names every
// offending path so the operator sees the full picture in one pass.
// Phase 3.2's pathsOverlap is deliberately NOT used here — the two
// primitives answer different questions, and symmetric overlap would
// accept a parent-prefix of a file entry.
//
// Validate runs before any disk I/O. CreatedAt is set to now if the
// caller left it blank. The result event is appended to the JSONL
// log inside the per-mission flock so concurrent submit/close
// attempts serialize cleanly.
//
// Atomic from the caller's view: a write failure leaves the on-disk
// results file unchanged.
func (s *Store) AppendResult(missionID string, r *Result) error {
	if r == nil {
		return fmt.Errorf("result is nil")
	}
	staged := *r
	// Normalize Author before persisting so whitespace around the
	// handle does not pollute the audit trail or the event log. The
	// Validate call only checks that the trimmed form is non-empty;
	// it does not reject surrounding whitespace, which would break
	// backwards compatibility with round 1 files that stored an
	// untrimmed author. Normalizing in Append is purely additive.
	staged.Author = strings.TrimSpace(staged.Author)
	if strings.TrimSpace(staged.CreatedAt) == "" {
		staged.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if err := staged.Validate(); err != nil {
		return fmt.Errorf("invalid result: %w", err)
	}
	return s.withLock(missionID, func() error {
		c, _, err := s.loadLocked(missionID)
		if err != nil {
			return err
		}
		// Refuse to record a result on a closed mission. A result is
		// the worker's handoff for a live round; a closed mission has
		// no live round to hand off from.
		if c.Status != StatusOpen {
			return fmt.Errorf("mission %q is in terminal state %q; results are accepted only on open missions", missionID, c.Status)
		}
		// Mission ID cross-check: the result's self-declared Mission
		// must match the caller's target. A file renamed between
		// missions is the kind of silent data corruption that would
		// otherwise slip past every other check.
		if staged.Mission != missionID {
			return fmt.Errorf(
				"result mission %q does not match target mission %q",
				staged.Mission, missionID,
			)
		}
		if staged.Round != c.CurrentRound {
			return fmt.Errorf(
				"result round %d does not match mission %q current round %d",
				staged.Round, missionID, c.CurrentRound,
			)
		}
		// files_changed containment: every declared path must live
		// inside at least one entry of the contract's write_set.
		// This is the third use of the write_set cross-check pattern
		// (Phase 3.2 admission, Phase 3.5 verifier allowlist, now
		// Phase 3.6 result containment). Uses pathContainedBy, the
		// asymmetric segment-prefix helper — a parent-prefix of a
		// write_set file entry must NOT be admitted.
		if err := checkFilesChangedContainment(c, &staged); err != nil {
			return err
		}
		existing, err := s.loadResultsLocked(missionID)
		if err != nil {
			return err
		}
		for _, e := range existing {
			if e.Round == staged.Round {
				return fmt.Errorf(
					"result for round %d of mission %q already exists; results are append-only",
					staged.Round, missionID,
				)
			}
		}
		updated := append(existing, staged)
		if err := s.writeResultsLocked(missionID, updated); err != nil {
			return err
		}
		if err := s.appendEventLocked(missionID, Event{
			TS:    staged.CreatedAt,
			Event: "result",
			Actor: staged.Author,
			Details: map[string]any{
				"round":            staged.Round,
				"verdict":          staged.Verdict,
				"confidence":       staged.Confidence,
				"files_changed":    len(staged.FilesChanged),
				"evidence_entries": len(staged.Evidence),
			},
		}); err != nil {
			// Roll back the results file so the mission's on-disk
			// state matches the operation's failure: if the event log
			// rejects the result record, the result itself must not
			// be observable to a later read or to the close gate.
			if rbErr := s.writeResultsLocked(missionID, existing); rbErr != nil {
				return fmt.Errorf("result: event append failed: %w; rollback failed: %v", err, rbErr)
			}
			return fmt.Errorf("result: event append failed, result rolled back: %w", err)
		}
		// Reflect CreatedAt back to the caller — the one field
		// AppendResult is contracted to set. Every other field came
		// from the caller and is preserved as-is.
		r.CreatedAt = staged.CreatedAt
		return nil
	})
}

// loadResultsLocked is the lock-respecting twin of LoadResults. The
// caller must already hold the per-mission flock; AppendResult and
// checkResultGateLocked use it to read the existing slice without
// re-acquiring the lock and deadlocking.
func (s *Store) loadResultsLocked(missionID string) ([]Result, error) {
	data, err := os.ReadFile(s.resultsPath(missionID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading results for %q: %w", missionID, err)
	}
	return decodeResultsFile(data, missionID)
}

// writeResultsLocked rewrites the results file via temp+rename.
// Caller must hold the per-mission flock. The file is wrapped in a
// resultsFile struct so future top-level metadata (e.g. a
// schema_version key) can be added without breaking decode of older
// files.
func (s *Store) writeResultsLocked(missionID string, rs []Result) error {
	wrapper := resultsFile{Results: rs}
	data, err := yaml.Marshal(&wrapper)
	if err != nil {
		return fmt.Errorf("marshaling results: %w", err)
	}
	dest := s.resultsPath(missionID)
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("writing temp results: %w", err)
	}
	return os.Rename(tmp, dest)
}

// checkResultGateLocked is the Phase 3.6 close gate. It runs inside
// Close's per-mission flock and refuses the terminal transition
// unless a valid result artifact exists for the mission's current
// round.
//
// On success it returns the satisfying result so the caller can
// record the round number and verdict on the close event's
// Details map. This was added in round 2 so the audit trail
// directly links the close transition to the result that authorized
// it, instead of forcing an auditor to scan back across
// round_advanced events.
//
// The refusal message names the mission, the missing round, and
// the submission command so the operator sees the recovery path in
// the error itself, not in separate documentation.
func (s *Store) checkResultGateLocked(c *Contract) (*Result, error) {
	results, err := s.loadResultsLocked(c.MissionID)
	if err != nil {
		return nil, fmt.Errorf("close: loading results for gate: %w", err)
	}
	for i := range results {
		if results[i].Round == c.CurrentRound {
			// Return a pointer into a local copy so the caller
			// cannot mutate the on-disk cache by accident.
			r := results[i]
			return &r, nil
		}
	}
	return nil, fmt.Errorf(
		"mission %q cannot close: no result artifact for round %d; run `ethos mission result %s --file <path>` to submit one",
		c.MissionID, c.CurrentRound, c.MissionID,
	)
}

// checkFilesChangedContainment verifies every FilesChanged entry in
// r lives under at least one entry of c.WriteSet. Uses
// pathContainedBy (asymmetric) so a result cannot quietly claim
// authority over a parent directory of a write_set entry.
//
// Phase 3.6 round 1 used the symmetric pathsOverlap helper; all four
// reviewers flagged the bug independently. A contract declaring
// `cmd/ethos/serve.go` with a result claiming `cmd` overlaps in one
// direction only — the file `cmd` has fewer segments than the entry
// `cmd/ethos/serve.go` — and the symmetric check accepted it. The
// asymmetric check correctly refuses: the entry's segment list must
// be a prefix of the file's segment list.
//
// The helper collects every out-of-bounds path before returning, so
// the operator sees the complete fix list in a single error rather
// than one path per retry. Empty FilesChanged is allowed — a round
// that only inspected code without writing is a legitimate outcome.
func checkFilesChangedContainment(c *Contract, r *Result) error {
	if len(r.FilesChanged) == 0 {
		return nil
	}
	var outOfBounds []string
	for _, fc := range r.FilesChanged {
		contained := false
		for _, entry := range c.WriteSet {
			if pathContainedBy(fc.Path, entry) {
				contained = true
				break
			}
		}
		if !contained {
			outOfBounds = append(outOfBounds, fc.Path)
		}
	}
	if len(outOfBounds) == 0 {
		return nil
	}
	return fmt.Errorf(
		"result files_changed contains %d path(s) outside mission %q write_set: %s",
		len(outOfBounds), c.MissionID, strings.Join(outOfBounds, ", "),
	)
}
