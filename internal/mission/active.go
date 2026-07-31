//go:build !windows

package mission

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Active-mission sidecar.
//
// The leader's Claude Code process cannot export MISSION_ID into its
// own env from inside an active session, so a Tier B dispatch from an
// in-session Agent() call has no way to discover the mission via the
// environment alone. The sidecar is the smallest-blast-radius bridge:
// one file at a known per-session path, one best-effort read in the
// dispatch hook, no changes to Claude Code's tool surface.
//
// Path: <globalRoot>/sessions/<session-id>/active-mission
// File mode: 0o600. Parent dir mode: 0o700.
// Content: the mission ID as plain text (trailing newline tolerated).
//
// Helpers refuse to operate when sessionID is empty so an unknown
// session cannot accidentally write a sidecar at <globalRoot>/sessions//
// active-mission.

// ActiveMissionPath returns the absolute path to the active-mission
// sidecar for sessionID under globalRoot. Returns an empty string when
// either argument is empty — callers treat that as "no path to act on".
func ActiveMissionPath(globalRoot, sessionID string) string {
	if globalRoot == "" || sessionID == "" {
		return ""
	}
	return filepath.Join(globalRoot, "sessions", filepath.Base(sessionID), "active-mission")
}

// ReadActiveMission reads the active-mission sidecar for sessionID.
// Returns ("", nil) when the file is absent — a missing sidecar is the
// common "no active mission" state, not an error. Returns the raw
// trimmed file contents on success: validation of the missionID shape
// is the caller's responsibility, not this helper's.
func ReadActiveMission(globalRoot, sessionID string) (string, error) {
	path := ActiveMissionPath(globalRoot, sessionID)
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("reading active-mission sidecar %q: %w", path, err)
	}
	return strings.TrimSpace(string(data)), nil
}

// WriteActiveMission writes missionID into the active-mission sidecar
// for sessionID. Creates the per-session directory at 0o700 if needed,
// then writes the file atomically via temp+rename so a partial write
// never leaves a half-formed sidecar. The final file is 0o600.
//
// Refuses missionID == "" — clearing the sidecar is ClearActiveMission's
// job and silently writing an empty file would let a caller "claim
// nothing" by accident.
func WriteActiveMission(globalRoot, sessionID, missionID string) error {
	path := ActiveMissionPath(globalRoot, sessionID)
	if path == "" {
		return fmt.Errorf("writing active-mission: globalRoot and sessionID are required")
	}
	if missionID == "" {
		return fmt.Errorf("writing active-mission: missionID is required (use ClearActiveMission to remove)")
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating session dir %q: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, "active-mission.*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp sidecar in %q: %w", dir, err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }

	if _, err := tmp.WriteString(missionID + "\n"); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("writing temp sidecar %q: %w", tmpPath, err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("chmod temp sidecar %q: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("closing temp sidecar %q: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return fmt.Errorf("renaming sidecar %q to %q: %w", tmpPath, path, err)
	}
	return nil
}

// DelegationBinding is the per-dispatch binding info that bridges the
// PreToolUse (where the delegation_id is allocated) to the PostToolUse
// audit writer (where the delegation_id should tag each tool call).
// additional_env from PreToolUse does NOT persist into hook script
// processes, so the binding sidecar is the bridge.
type DelegationBinding struct {
	DelegationID  string
	MissionID     string
	ParentSession string
}

// DelegationBindingPath returns the path to the delegation-binding
// sidecar for sessionID.
func DelegationBindingPath(globalRoot, sessionID string) string {
	if globalRoot == "" || sessionID == "" {
		return ""
	}
	return filepath.Join(globalRoot, "sessions", filepath.Base(sessionID), "delegation-binding")
}

// WriteDelegationBinding writes the binding info that the PostToolUse
// audit writer reads. Called from the PreToolUse Tier B dispatch after
// the delegation skeleton is written and the delegation_id is known.
func WriteDelegationBinding(globalRoot, sessionID string, b DelegationBinding) error {
	path := DelegationBindingPath(globalRoot, sessionID)
	if path == "" {
		return fmt.Errorf("writing delegation-binding: globalRoot and sessionID are required")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating session dir %q: %w", dir, err)
	}
	content := b.DelegationID + "\n" + b.MissionID + "\n" + b.ParentSession + "\n"
	tmp, err := os.CreateTemp(dir, "delegation-binding.*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp binding in %q: %w", dir, err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// ReadDelegationBinding reads the delegation-binding sidecar.
// Returns a zero-value DelegationBinding and nil when the file is
// absent — missing is the common "no active dispatch" state.
func ReadDelegationBinding(globalRoot, sessionID string) (DelegationBinding, error) {
	path := DelegationBindingPath(globalRoot, sessionID)
	if path == "" {
		return DelegationBinding{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return DelegationBinding{}, nil
		}
		return DelegationBinding{}, fmt.Errorf("reading delegation-binding %q: %w", path, err)
	}
	// Trim each line, not just the file: a sidecar written with CRLF
	// endings (or hand-edited with a trailing space) would otherwise
	// carry the stray byte into the mission ID and fail every
	// comparison against it — the delegation would silently drop off
	// the commit trailer.
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var b DelegationBinding
	if len(lines) > 0 {
		b.DelegationID = strings.TrimSpace(lines[0])
	}
	if len(lines) > 1 {
		b.MissionID = strings.TrimSpace(lines[1])
	}
	if len(lines) > 2 {
		b.ParentSession = strings.TrimSpace(lines[2])
	}
	return b, nil
}

// ClearActiveMission removes the active-mission sidecar for sessionID.
// Missing is not an error — clearing an already-clear slot is a no-op
// so `ethos mission release` is safe to call unconditionally.
func ClearActiveMission(globalRoot, sessionID string) error {
	path := ActiveMissionPath(globalRoot, sessionID)
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("removing active-mission sidecar %q: %w", path, err)
	}
	return nil
}

// ClearMissionBindings removes sessionID's active-mission and
// delegation-binding sidecars when they name missionID. Every surface
// that takes a mission out of the open set calls it: the commit-msg
// trailer fallback gates on the active-mission sidecar, so a close that
// left it behind would keep tagging later missionless commits with the
// closed mission (ethos-jawp).
//
// Each sidecar is cleared only when it names missionID, so a claim or
// dispatch for a different, still-open mission survives. A missing
// sidecar is a silent no-op — that is the ordinary state.
//
// The two sidecars are independent, so a failure on one does not stop
// work on the other; all failures come back joined. Callers treat the
// result as advisory — a mission that closed stays closed — but must
// report it, because each failure leaves a sidecar in place and the
// trailer gate open on a closed mission.
func ClearMissionBindings(globalRoot, sessionID, missionID string) error {
	if globalRoot == "" || sessionID == "" || missionID == "" {
		return nil
	}
	var errs []error

	// ReadActiveMission and ReadDelegationBinding both report a missing
	// sidecar as a zero value with a nil error, so a non-nil error here
	// is always a real read failure — never "no sidecar".
	active, err := ReadActiveMission(globalRoot, sessionID)
	switch {
	case err != nil:
		errs = append(errs, fmt.Errorf("reading active mission: %w", err))
	case active == missionID:
		if err := ClearActiveMission(globalRoot, sessionID); err != nil {
			errs = append(errs, fmt.Errorf("clearing active mission: %w", err))
		}
	}

	b, err := ReadDelegationBinding(globalRoot, sessionID)
	switch {
	case err != nil:
		errs = append(errs, fmt.Errorf("reading delegation binding: %w", err))
	case b.MissionID == missionID:
		if err := ClearDelegationBinding(globalRoot, sessionID); err != nil {
			errs = append(errs, fmt.Errorf("clearing delegation binding: %w", err))
		}
	}
	return errors.Join(errs...)
}

// ClearDelegationBinding removes the delegation-binding sidecar for
// sessionID. Missing is not an error — clearing an already-clear slot
// is a no-op.
//
// The binding is written per-dispatch but was never cleared, so it
// accumulated across sessions and let the commit-msg hook's fallback
// tag unrelated commits with a stale delegation (ethos-jawp). Clearing
// it on `mission release` and on terminal transitions bounds the
// sidecar's lifetime to the dispatch it describes.
func ClearDelegationBinding(globalRoot, sessionID string) error {
	path := DelegationBindingPath(globalRoot, sessionID)
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("removing delegation-binding sidecar %q: %w", path, err)
	}
	return nil
}
