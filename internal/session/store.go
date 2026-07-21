//go:build !windows

package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/punt-labs/ethos/internal/audit"
	"gopkg.in/yaml.v3"
)

// Store provides CRUD operations for session rosters on the filesystem.
// Rosters are stored as YAML files in the sessions subdirectory.
// Write operations use flock for concurrency safety.
type Store struct {
	root string // e.g. ~/.punt-labs/ethos
}

// NewStore creates a Store rooted at the given directory.
func NewStore(root string) *Store {
	return &Store{root: root}
}

func (s *Store) sessionsDir() string {
	return filepath.Join(s.root, "sessions")
}

func (s *Store) rosterPath(sessionID string) string {
	return filepath.Join(s.sessionsDir(), filepath.Base(sessionID)+".yaml")
}

func (s *Store) lockPath(sessionID string) string {
	return filepath.Join(s.sessionsDir(), filepath.Base(sessionID)+".lock")
}

func (s *Store) currentDir() string {
	return filepath.Join(s.sessionsDir(), "current")
}

// Create creates a new session roster with root and primary participants.
func (s *Store) Create(sessionID string, root, primary Participant, repo, host string) error {
	if err := os.MkdirAll(s.sessionsDir(), 0o700); err != nil {
		return fmt.Errorf("creating sessions directory: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if root.Joined == "" {
		root.Joined = now
	}
	if primary.Joined == "" {
		primary.Joined = now
	}

	roster := &Roster{
		Session:      sessionID,
		Started:      now,
		Repo:         repo,
		Host:         host,
		Participants: []Participant{root, primary},
	}

	return s.withLock(sessionID, func() error {
		return s.writeRoster(sessionID, roster)
	})
}

// Load reads a session roster by ID.
func (s *Store) Load(sessionID string) (*Roster, error) {
	data, err := os.ReadFile(s.rosterPath(sessionID))
	if err != nil {
		return nil, fmt.Errorf("session %q not found: %w", sessionID, err)
	}
	var roster Roster
	if err := yaml.Unmarshal(data, &roster); err != nil {
		return nil, fmt.Errorf("invalid roster file %s: %w", sessionID, err)
	}
	return &roster, nil
}

// Join adds a participant to a session roster.
func (s *Store) Join(sessionID string, p Participant) error {
	return s.withLock(sessionID, func() error {
		roster, err := s.Load(sessionID)
		if err != nil {
			return err
		}
		if existing := roster.FindParticipant(p.AgentID); existing != nil {
			if p.Persona != "" {
				existing.Persona = p.Persona
			}
			if p.AgentType != "" {
				existing.AgentType = p.AgentType
			}
			if p.Parent != "" {
				existing.Parent = p.Parent
			}
			if p.Ext != nil {
				existing.Ext = p.Ext
			}
		} else {
			if p.Joined == "" {
				p.Joined = time.Now().UTC().Format(time.RFC3339)
			}
			roster.Participants = append(roster.Participants, p)
		}
		return s.writeRoster(sessionID, roster)
	})
}

// Leave removes a participant from a session roster.
func (s *Store) Leave(sessionID string, agentID string) error {
	return s.withLock(sessionID, func() error {
		roster, err := s.Load(sessionID)
		if err != nil {
			return err
		}
		// Idempotent: leaving a session you were never in is a no-op.
		// SubagentStop fires regardless of whether SubagentStart succeeded.
		if !roster.RemoveParticipant(agentID) {
			return nil // Nothing to remove — skip the write.
		}
		return s.writeRoster(sessionID, roster)
	})
}

// Delete removes a session roster and its lock file.
// Acquires the flock to coordinate with concurrent writers.
func (s *Store) Delete(sessionID string) error {
	err := s.withLock(sessionID, func() error {
		return s.deleteFiles(sessionID)
	})
	if err != nil {
		return err
	}
	os.Remove(s.lockPath(sessionID))
	return nil
}

// deleteFiles removes the roster file only (no lock file cleanup).
// Used inside withLock where the lock file must remain.
func (s *Store) deleteFiles(sessionID string) error {
	if err := os.Remove(s.rosterPath(sessionID)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("deleting session %q: %w", sessionID, err)
	}
	return nil
}

// List returns all session IDs.
func (s *Store) List() ([]string, error) {
	entries, err := os.ReadDir(s.sessionsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading sessions directory: %w", err)
	}
	var ids []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		ids = append(ids, strings.TrimSuffix(entry.Name(), ".yaml"))
	}
	return ids, nil
}

// Purge removes stale session rosters by checking whether the primary
// agent's PID is still alive. The primary agent is the second participant
// (index 1) — the first participant with a numeric agent_id whose
// process is no longer running.
func (s *Store) Purge() ([]string, error) {
	ids, err := s.List()
	if err != nil {
		return nil, err
	}
	var purged []string
	for _, id := range ids {
		didPurge := false
		if lockErr := s.withLock(id, func() error {
			roster, err := s.Load(id)
			if err != nil {
				// Corrupt roster — delete under lock.
				if s.deleteFiles(id) == nil {
					didPurge = true
				}
				return nil
			}
			if isStale(roster) {
				if s.deleteFiles(id) == nil {
					didPurge = true
				}
			}
			return nil
		}); lockErr != nil {
			fmt.Fprintf(os.Stderr, "ethos: purge: failed to lock session %s: %v\n", id, lockErr)
		}
		if didPurge {
			os.Remove(s.lockPath(id))
			purged = append(purged, id)
		}
	}

	return purged, nil
}

// PurgeTombstoned is Purge with the DES-058 unsealed-lines guard. Before
// removing a stale session bound to a repo, it checks the session's live
// audit file: if it still holds lines above the sealed watermark, purge
// refuses (the id is returned in refused) unless force is set, in which case
// it leaves a flagged tombstone and proceeds. An already-absent live file
// (a checkout deleted before its lines sealed) also leaves a flagged
// tombstone. The tombstone lets the seal's vacuum cross-check keep looking at
// a session whose roster entry is gone.
//
// Sessions with no repo binding, or whose live file is clean, purge silently
// as before. Returns the purged and refused session ids.
func (s *Store) PurgeTombstoned(force bool) (purged, refused []string, err error) {
	ids, err := s.List()
	if err != nil {
		return nil, nil, err
	}
	for _, id := range ids {
		var didPurge, didRefuse bool
		if lockErr := s.withLock(id, func() error {
			roster, lErr := s.Load(id)
			if lErr != nil {
				if s.deleteFiles(id) == nil {
					didPurge = true
				}
				return nil
			}
			if !isStale(roster) {
				return nil
			}
			didPurge, didRefuse = s.purgeOneTombstoned(roster, force)
			return nil
		}); lockErr != nil {
			fmt.Fprintf(os.Stderr, "ethos: purge: failed to lock session %s: %v\n", id, lockErr)
			continue
		}
		if didPurge {
			os.Remove(s.lockPath(id))
			purged = append(purged, id)
		}
		if didRefuse {
			refused = append(refused, id)
		}
	}
	return purged, refused, nil
}

// purgeOneTombstoned applies the unsealed-lines guard to one stale roster
// under the caller's lock, writing a flagged tombstone when it proceeds past
// pending or already-gone lines. Returns (didPurge, didRefuse).
func (s *Store) purgeOneTombstoned(roster *Roster, force bool) (bool, bool) {
	repo := roster.Repo
	unsealed := 0
	liveGone := false
	if repo != "" {
		if n, cErr := audit.SessionUnsealedCount(repo, roster.Session); cErr == nil {
			unsealed = n
		}
		liveGone = !audit.SessionLiveFileExists(repo, roster.Session)
	}
	if unsealed > 0 && !force {
		fmt.Fprintf(os.Stderr,
			"ethos: purge: refusing to purge %s: %d unsealed audit line(s); commit to seal them or re-run with --force\n",
			roster.Session, unsealed)
		return false, true
	}
	if repo != "" && (unsealed > 0 || liveGone) {
		t := audit.Tombstone{
			Session:       roster.Session,
			StartDate:     rosterStartDate(roster),
			Repo:          repo,
			Checkout:      repo,
			UnsealedLines: unsealed > 0,
			LiveFileGone:  liveGone,
		}
		if tErr := audit.WriteTombstone(s.sessionsDir(), t); tErr != nil {
			fmt.Fprintf(os.Stderr, "ethos: purge: writing tombstone for %s: %v\n", roster.Session, tErr)
		}
	}
	if s.deleteFiles(roster.Session) != nil {
		return false, false
	}
	return true, false
}

// rosterStartDate returns the YYYY-MM-DD start date from a roster's Started
// timestamp, or "" when it cannot be parsed.
func rosterStartDate(roster *Roster) string {
	if len(roster.Started) >= 10 {
		return roster.Started[:10]
	}
	return roster.Started
}

// PurgeCurrent removes PID files from sessions/current/ where the
// process is no longer alive. Returns the list of removed PIDs.
func (s *Store) PurgeCurrent() ([]string, error) {
	dir := s.currentDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading current directory: %w", err)
	}
	var purged []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		pid, err := strconv.Atoi(name)
		if err != nil {
			continue // not a PID file
		}
		if pid <= 0 {
			continue // invalid PID
		}
		if !isProcessAlive(pid) {
			path := filepath.Join(dir, name)
			if removeErr := os.Remove(path); removeErr != nil {
				if !os.IsNotExist(removeErr) {
					fmt.Fprintf(os.Stderr, "ethos: failed to remove PID file %s: %v\n", path, removeErr)
				}
			} else {
				purged = append(purged, name)
			}
		}
	}
	return purged, nil
}

// WriteCurrentSession writes the session ID to a PID-keyed file so
// descendant processes can discover the session.
func (s *Store) WriteCurrentSession(claudePID, sessionID string) error {
	dir := s.currentDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating current directory: %w", err)
	}
	path := filepath.Join(dir, filepath.Base(claudePID))
	return os.WriteFile(path, []byte(sessionID+"\n"), 0o600)
}

// ReadCurrentSession reads the session ID from a PID-keyed file.
func (s *Store) ReadCurrentSession(claudePID string) (string, error) {
	path := filepath.Join(s.currentDir(), filepath.Base(claudePID))
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("no current session for PID %s: %w", claudePID, err)
	}
	return strings.TrimSpace(string(data)), nil
}

// DeleteCurrentSession removes the PID-keyed session file.
func (s *Store) DeleteCurrentSession(claudePID string) error {
	path := filepath.Join(s.currentDir(), filepath.Base(claudePID))
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("deleting current session for PID %s: %w", claudePID, err)
	}
	return nil
}

// writeRoster marshals and writes a roster atomically via
// os.CreateTemp + Chmod(0o600) + Sync + Close + Rename in the
// sessions directory. Sync errors propagate so a failed fsync
// surfaces — djb evaluator gate: a half-written roster is
// unacceptable. The temp file is removed on every error path. A
// random suffix (via os.CreateTemp) avoids the predictable ".tmp"
// suffix that two concurrent writers could trample.
func (s *Store) writeRoster(sessionID string, roster *Roster) error {
	data, err := yaml.Marshal(roster)
	if err != nil {
		return fmt.Errorf("marshaling roster: %w", err)
	}
	dest := s.rosterPath(sessionID)
	dir := filepath.Dir(dest)
	tmp, err := os.CreateTemp(dir, "roster-*.yaml.tmp")
	if err != nil {
		return fmt.Errorf("creating temp roster in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	if n, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("writing temp roster %s: %w", tmpPath, err)
	} else if n < len(data) {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("short write to temp roster %s: %d of %d bytes", tmpPath, n, len(data))
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("chmod temp roster %s: %w", tmpPath, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("syncing temp roster %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("closing temp roster %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("renaming temp roster %s -> %s: %w", tmpPath, dest, err)
	}
	return nil
}

// WithSessionLock executes fn while holding an exclusive flock on the
// session's lock file. The lock covers both the session roster YAML and
// the session audit log (DES-054 v5 unification): one flock per session
// serializes every per-session write, eliminating the two-lock
// acquisition order the v4 design carried. Audit log appends and roster
// mutations therefore use the same lock; a writer must not acquire two
// locks for one logical operation.
//
// Concurrency ordering when this lock is nested inside others (DES-054
// phase 2): global mission create lock → repo mission create lock →
// per-mission flock (shared) → per-delegation flock (exclusive) →
// per-session flock. Release is reverse via defer LIFO.
//
// Exported so the audit-log entry point in cmd/ethos/hook.go can wrap
// its write in the unified lock without re-implementing the
// open/flock/close dance.
func (s *Store) WithSessionLock(sessionID string, fn func() error) error {
	return s.withLock(sessionID, fn)
}

// withLock executes fn while holding an exclusive flock on the
// session's lock file. The public WithSessionLock wraps this helper
// and shares no fd state with it.
//
// withLock is NOT re-entrant. Each call opens a fresh file descriptor
// on the per-session lock path and acquires LOCK_EX on it; a nested
// call from within fn (or from any path WithSessionLock already
// holds) opens a second fd against the same inode and blocks waiting
// for the first holder to release. The same goroutine then deadlocks
// against itself. Callers must structure their work so the lock is
// acquired exactly once at the top of any session-write path.
func (s *Store) withLock(sessionID string, fn func() error) error {
	if err := os.MkdirAll(s.sessionsDir(), 0o700); err != nil {
		return fmt.Errorf("creating sessions directory: %w", err)
	}
	lockFile := s.lockPath(sessionID)
	f, err := os.OpenFile(lockFile, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("opening lock file: %w", err)
	}
	defer f.Close()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("acquiring lock: %w", err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)

	return fn()
}

// staleTTL is the maximum age of a session with an uncheckable primary PID.
const staleTTL = 24 * time.Hour

// isStale checks if a roster's primary agent PID is no longer alive.
// Falls back to age-based staleness for non-numeric agent IDs.
func isStale(roster *Roster) bool {
	if len(roster.Participants) < 2 {
		return true
	}
	// Primary agent is the second participant (index 1).
	primaryID := roster.Participants[1].AgentID
	pid, err := strconv.Atoi(primaryID)
	if err != nil {
		// Non-numeric agent_id — fall back to age check.
		return isOlderThan(roster, staleTTL)
	}
	return !isProcessAlive(pid)
}

// isOlderThan checks if a roster's started timestamp exceeds the given TTL.
func isOlderThan(roster *Roster, ttl time.Duration) bool {
	started, err := time.Parse(time.RFC3339, roster.Started)
	if err != nil {
		return true // Unparseable timestamp — treat as stale.
	}
	return time.Since(started) > ttl
}

// isProcessAlive checks whether a process with the given PID is running.
//
// Limitations:
//   - Zombie processes (exited but not yet waited on) still respond to
//     signal 0, so this function returns true for zombies.
//   - PID reuse is not addressed. If the original process exits and the OS
//     assigns the same PID to an unrelated process, this returns a false
//     positive. On modern Linux/macOS the PID space is large enough that
//     reuse within a single session lifetime is unlikely but not impossible.
func isProcessAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// On Unix, FindProcess always succeeds. Send signal 0 to check.
	err = p.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	// EPERM means the process exists but we can't signal it.
	return err == syscall.EPERM
}
