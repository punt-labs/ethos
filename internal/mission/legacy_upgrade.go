package mission

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

// ErrLockBusy is returned by WithRecordLockTry when another process holds the
// lock. The caller retries later; it is not a failure.
var ErrLockBusy = errors.New("mission lock is busy")

// LegacyHistoryError reports a mission whose legacy round or correction
// history cannot be imported as complete shared authority: the history is
// incomplete, malformed, or refused by redaction. The mission stays as it is.
type LegacyHistoryError struct{ Err error }

func (e *LegacyHistoryError) Error() string { return e.Err.Error() }
func (e *LegacyHistoryError) Unwrap() error { return e.Err }

// WithRecordLockTry runs fn holding the per-mission state lock that every
// mission writer takes, or returns ErrLockBusy at once when another holder has
// it. The lock path is the one Store.withMissionLock uses, so repair
// serializes with live writers.
func WithRecordLockTry(p layout.Policy, id string, fn func() error) error {
	if !missionIDPattern.MatchString(id) {
		return fmt.Errorf("invalid mission id %q", id)
	}
	path, err := p.LockPath("mission-state", id)
	if err != nil {
		return err
	}
	if err := layout.EnsureLocalDir(p, filepath.Dir(path)); err != nil {
		return err
	}
	if err := rejectSymlink(path); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("opening lock file: %w", err)
	}
	defer f.Close()
	busy, err := flockTry(f, lockExclusive)
	if err != nil {
		return fmt.Errorf("acquiring lock: %w", err)
	}
	if busy {
		return ErrLockBusy
	}
	defer func() { _ = funlock(f) }()
	return fn()
}

// UpgradeLegacyAuthority gives a repository mission that predates DES-079
// shared round decisions and corrections authority, computed eagerly from its
// retained legacy evidence by the same strict importers the lifecycle writers
// use. It reports whether it wrote anything. A mission that is not
// repository-owned, or already has decisions, is left alone.
//
// Nothing is fabricated: when the crossed-round reflection history or the
// event history is incomplete, or redaction refuses the evidence, the mission
// is untouched and the error is a *LegacyHistoryError. Corrections are written
// before the contract, so a failure between the two leaves the mission legacy
// and importable again, never half upgraded. The caller holds the mission lock.
func (s *Store) UpgradeLegacyAuthority(id string) (bool, error) {
	if !s.twoTreeStorage {
		return false, fmt.Errorf("UpgradeLegacyAuthority requires repository storage")
	}
	layer, err := s.resolveLayer(id)
	if err != nil {
		return false, err
	}
	if layer != layerRepo {
		return false, nil
	}
	c, _, err := s.loadLocked(id)
	if err != nil {
		return false, err
	}
	if c.RoundDecisions != nil {
		a, err := s.readCorrections(c)
		if err != nil {
			return false, &LegacyHistoryError{fmt.Errorf("corrections authority unreadable: %w", err)}
		}
		if a == nil {
			return false, &LegacyHistoryError{fmt.Errorf("round decisions exist but corrections authority is missing; not inferring empty history")}
		}
		return false, nil
	}
	if a, err := s.readCorrections(c); err != nil || a != nil {
		if err != nil {
			return false, &LegacyHistoryError{fmt.Errorf("corrections authority unreadable: %w", err)}
		}
		return false, &LegacyHistoryError{fmt.Errorf("corrections authority exists without round decisions")}
	}
	upgraded := *c
	if err := s.importLegacyDecisions(&upgraded); err != nil {
		return false, &LegacyHistoryError{err}
	}
	corrections, err := s.importLegacyCorrections(&upgraded)
	if err != nil {
		return false, &LegacyHistoryError{err}
	}
	cb, _, err := RedactedYAMLNotify(s.repoRoot, *corrections)
	if err != nil {
		return false, &LegacyHistoryError{err}
	}
	cb, err = guardWrite(s.correctionsPath(id), cb)
	if err != nil {
		return false, &LegacyHistoryError{err}
	}
	contractBytes, _, err := RedactedYAMLNotify(s.repoRoot, upgraded, func(red Contract) error {
		if !reflect.DeepEqual(red.RoundDecisions, upgraded.RoundDecisions) || red.Budget != upgraded.Budget || red.Evaluator != upgraded.Evaluator {
			return fmt.Errorf("redaction changed frozen authority")
		}
		return s.validateContract(&red)
	})
	if err != nil {
		return false, &LegacyHistoryError{err}
	}
	path, err := s.contractPath(id)
	if err != nil {
		return false, err
	}
	if err := s.writeBytes(s.correctionsPath(id), cb, false); err != nil {
		return false, err
	}
	if err := s.writeBytes(path, contractBytes, true); err != nil {
		if rbErr := s.removeBytes(s.correctionsPath(id)); rbErr != nil {
			return false, fmt.Errorf("installing upgraded contract: %w; removing new corrections: %v", err, rbErr)
		}
		return false, fmt.Errorf("installing upgraded contract: %w", err)
	}
	return true, nil
}

// IsMissionID reports whether name is a well-formed mission ID, so a caller
// walking the record tree can skip directories that are not missions.
func IsMissionID(name string) bool { return missionIDPattern.MatchString(name) }

// ReplaceRecordFile atomically replaces dest, a file in the canonical store or
// checkout of p, with data, staging in the policy's local temp directory. The
// bytes pass guardWrite like every other record writer: they are canonicalized,
// and refused when they still leak a path or credential.
func ReplaceRecordFile(p layout.Policy, dest string, data []byte) error {
	data, err := guardWrite(dest, data)
	if err != nil {
		return err
	}
	w, err := localReplace(p, dest, data)
	return errors.Join(err, w.DurabilityError)
}
