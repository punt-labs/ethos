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
func (s *Store) Create(sessionID string, root, primary Participant) error {
	if err := os.MkdirAll(s.sessionsDir(), 0o700); err != nil {
		return fmt.Errorf("creating sessions directory: %w", err)
	}

	roster := &Roster{
		Session:      sessionID,
		Started:      time.Now().UTC().Format(time.RFC3339),
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
		if !roster.RemoveParticipant(agentID) {
			return fmt.Errorf("participant %q not found in session %q", agentID, sessionID)
		}
		return s.writeRoster(sessionID, roster)
	})
}

// Delete removes a session roster and its lock file.
func (s *Store) Delete(sessionID string) error {
	os.Remove(s.lockPath(sessionID))
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
		shouldPurge := false
		_ = s.withLock(id, func() error {
			roster, err := s.Load(id)
			if err != nil {
				shouldPurge = true
				return nil
			}
			if isStale(roster) {
				shouldPurge = true
			}
			return nil
		})
		if shouldPurge {
			if delErr := s.Delete(id); delErr == nil {
				purged = append(purged, id)
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

// writeRoster marshals and writes a roster atomically via temp file + rename.
func (s *Store) writeRoster(sessionID string, roster *Roster) error {
	data, err := yaml.Marshal(roster)
	if err != nil {
		return fmt.Errorf("marshaling roster: %w", err)
	}
	dest := s.rosterPath(sessionID)
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("writing temp roster: %w", err)
	}
	return os.Rename(tmp, dest)
}

// withLock executes fn while holding an exclusive flock on the session's
// lock file.
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

// isProcessAlive checks if a process with the given PID exists.
// Returns true for EPERM (process exists but not signalable).
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
