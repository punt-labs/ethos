package mission

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

// NewRepositoryStore takes a validated, immutable location/config snapshot.
// The global fallback remains a separate layer; it does not own repository
// locks, archives, staging, or live files. Callers must quiesce old writers
// before moving from the historical global/record-tree locks to these locks.
func NewRepositoryStore(p layout.Policy, globalRoot string) *Store {
	if p.RecordRoot() == "" {
		return &Store{root: globalRoot, policyErr: fmt.Errorf("mission: invalid repository policy")}
	}
	return &Store{
		root: globalRoot, repoRoot: filepath.Dir(filepath.Dir(p.RecordRoot())),
		checkoutRoot:   filepath.Dir(filepath.Dir(p.CheckoutRecordRoot())),
		twoTreeStorage: true, policy: p, explicitPolicy: true,
	}
}

func (s *Store) RepositoryPolicy() (layout.Policy, bool) {
	return s.policy, s.twoTreeStorage && s.policyErr == nil && s.policy.LocalRoot() != ""
}

func (s *Store) recordMode() string {
	if s.policy.RecordMode() == layout.RecordFull {
		return "full"
	}
	return "blame"
}

// installedWrite separates the acceptance point from directory durability.
// An error after rename must never be mistaken for a failed installation.
type installedWrite struct{ DurabilityError error }

func localReplace(p layout.Policy, dest string, data []byte) (installedWrite, error) {
	if _, err := ownedSyncRoot(p, dest); err != nil {
		return installedWrite{}, err
	}
	if err := rejectSymlink(dest); err != nil {
		return installedWrite{}, err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return installedWrite{}, err
	}
	f, err := layout.CreateLocalTemp(p, "mission", ".yaml")
	if err != nil {
		return installedWrite{}, err
	}
	defer os.Remove(f.Name())
	n, err := f.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return installedWrite{}, fmt.Errorf("persisting mission staging: %w", err)
	}
	if err := os.Rename(f.Name(), dest); err != nil {
		return installedWrite{}, fmt.Errorf("installing %s: %w", dest, err)
	}
	return installedWrite{DurabilityError: syncOwnedAncestors(p, dest)}, nil
}

// syncOwnedAncestors confirms newly-created ancestor directory entries as well as
// the target's own entry. This is required before an event can authorize an
// authority installation. On platforms without directory fsync syncDir carries
// the repository's established platform behavior.
func syncOwnedAncestors(p layout.Policy, path string) error {
	root, err := ownedSyncRoot(p, path)
	if err != nil {
		return err
	}
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		if err := syncDir(dir); err != nil {
			return err
		}
		if dir == root {
			return nil
		}
	}
}

func ownedSyncRoot(p layout.Policy, path string) (string, error) {
	if p.RecordRoot() == "" || p.CheckoutRecordRoot() == "" {
		return "", fmt.Errorf("mission durability requires a repository policy")
	}
	root := filepath.Dir(filepath.Dir(p.RecordRoot()))
	rel, err := filepath.Rel(root, path)
	if err != nil || !filepath.IsLocal(rel) || rel == "." {
		root = filepath.Dir(filepath.Dir(p.CheckoutRecordRoot()))
		rel, err = filepath.Rel(root, path)
		if err != nil || !filepath.IsLocal(rel) || rel == "." {
			return "", fmt.Errorf("mission durability path is outside the canonical store and checkout")
		}
	}
	if err := validatePhysicalPath(path, false); err != nil {
		return "", err
	}
	return root, nil
}

type missionDestinationOwner int

const (
	destinationGlobal missionDestinationOwner = iota
	destinationRepository
)

// destinationOwner is shared by installation and removal durability. Only the
// recognized flat G artifacts are fallback authority; an outside path is not.
func (s *Store) destinationOwner(dest string) (missionDestinationOwner, error) {
	if s.policyErr != nil {
		return 0, s.policyErr
	}
	if s.globalMissionDestination(dest) {
		if err := rejectSymlink(dest); err != nil {
			return 0, err
		}
		return destinationGlobal, nil
	}
	if _, err := ownedSyncRoot(s.policy, dest); err != nil {
		return 0, err
	}
	return destinationRepository, nil
}

func (s *Store) removeBytes(dest string) error {
	owner, err := s.destinationOwner(dest)
	if err != nil {
		return err
	}
	if err := os.Remove(dest); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing %s: %w", dest, err)
	}
	if owner == destinationGlobal {
		return syncDir(filepath.Dir(dest))
	}
	return syncOwnedAncestors(s.policy, dest)
}

func (s *Store) writeBytes(dest string, data []byte, guarded bool) error {
	return s.writeBytesWithOutcome(dest, data, guarded, nil)
}

func (s *Store) writeBytesWithOutcome(dest string, data []byte, guarded bool, out *MutationOutcome) error {
	owner, err := s.destinationOwner(dest)
	if err != nil {
		return err
	}
	if err := s.persistenceCheckpoint("mission_write"); err != nil {
		return err
	}
	if guarded {
		var err error
		data, err = guardWrite(dest, data)
		if err != nil {
			return err
		}
	}
	if owner == destinationGlobal {
		return writeContractBytesWithOutcome(dest, data, out)
	}
	w, err := localReplace(s.policy, dest, data)
	if err == nil && w.DurabilityError != nil {
		out.warn(fmt.Sprintf("mission: %s installed; crash durability unconfirmed: %v", dest, w.DurabilityError))
	}
	return err
}

// Only the established flat G mission artifacts use standalone staging and
// destination-directory sync. An arbitrary path outside S/C is not a global
// fallback merely because this Store has a global root configured.
func (s *Store) globalMissionDestination(dest string) bool {
	if dest != filepath.Clean(dest) || filepath.Dir(dest) != s.globalMissionsDir() {
		return false
	}
	name := filepath.Base(dest)
	for _, suffix := range []string{".results.yaml", ".reflections.yaml", ".yaml"} {
		if id, ok := strings.CutSuffix(name, suffix); ok && missionIDPattern.MatchString(id) {
			return true
		}
	}
	return false
}

// Restoring an earlier state is part of a failed transaction. Its durability
// failure belongs in the returned rollback error, never in success diagnostics.
func (s *Store) restoreBytes(dest string, data []byte) error {
	var out MutationOutcome
	if err := s.writeBytesWithOutcome(dest, data, false, &out); err != nil {
		return err
	}
	if len(out.Warnings) > 0 {
		return errors.New(strings.Join(out.Warnings, "; "))
	}
	return nil
}
