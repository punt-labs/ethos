package identity

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// OverlayMove records one legacy overlay RelocateLegacyOverlays dealt with.
// Kept is true when the local destination already existed, so the legacy copy
// was deleted instead of moved. Neither path's contents are ever reported.
type OverlayMove struct {
	Legacy string
	Local  string
	Kept   bool
}

// OverlayResidual names an overlay that cannot be relocated and was left in
// place. ID is "<handle>/<namespace>"; Reason never contains a value.
type OverlayResidual struct {
	ID     string
	Reason string
}

// RelocateLegacyOverlays moves every repository overlay still in its pre-DES-079
// sibling location (<handle>.ext/<ns>.local.yaml beside the identity) to the
// local zone. When the local destination exists and parses it wins and the
// legacy copy is deleted; when it does not parse both files stay and the pair
// is reported as a residual. A refused (symlinked) legacy overlay is a residual
// too. Each handle is handled under its identity lock; a handle whose lock
// is held elsewhere is skipped and reported through busy. The merged identity is
// never read or written: file bytes move as they are.
//
// It returns the moves applied so far together with the first error.
func (s *Store) RelocateLegacyOverlays() (moves []OverlayMove, residuals []OverlayResidual, busy bool, err error) {
	if !s.config.repository {
		return nil, nil, false, nil
	}
	if err := s.validatePolicy(); err != nil {
		return nil, nil, false, err
	}
	entries, err := s.readDir(s.identitiesDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, false, nil
	}
	if err != nil {
		return nil, nil, false, fmt.Errorf("listing identities for overlay relocation: %w", err)
	}
	var handles []string
	for _, e := range entries {
		// A symlinked .ext entry is listed so it is reported, not silently skipped.
		if (e.IsDir() || e.Type()&os.ModeSymlink != 0) && strings.HasSuffix(e.Name(), ".ext") {
			handles = append(handles, strings.TrimSuffix(e.Name(), ".ext"))
		}
	}
	sort.Strings(handles)
	for _, h := range handles {
		legacy, err := s.legacyOverlays(h)
		var rp *repositoryPathError
		if errors.As(err, &rp) {
			residuals = append(residuals, OverlayResidual{ID: h, Reason: "extension directory is refused: " + rp.reason + " at " + rp.path})
			continue
		}
		if err != nil {
			return moves, residuals, busy, err
		}
		if len(legacy) == 0 {
			continue
		}
		err = s.withTryLock(h, func() error {
			for _, ns := range legacy {
				m, reason, err := s.relocateOverlay(h, ns)
				if m != nil {
					moves = append(moves, *m)
				}
				if reason != "" {
					residuals = append(residuals, OverlayResidual{ID: h + "/" + ns, Reason: reason})
				}
				if err != nil {
					return err
				}
			}
			return nil
		})
		if errors.Is(err, ErrLockBusy) {
			busy = true
			continue
		}
		if err != nil {
			return moves, residuals, busy, err
		}
	}
	return moves, residuals, busy, nil
}

// legacyOverlays lists the namespaces of handle with a legacy overlay file.
func (s *Store) legacyOverlays(handle string) ([]string, error) {
	files, err := s.readDir(s.ExtDir(handle))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("listing extensions of %q: %w", handle, err)
	}
	var out []string
	for _, f := range files {
		if n, ok := strings.CutSuffix(f.Name(), ExtLocalSuffix); ok && !f.IsDir() && n != "" {
			out = append(out, n)
		}
	}
	return out, nil
}

// relocateOverlay deals with one namespace; the caller holds the handle lock.
// It returns a non-nil move once the legacy file has been moved or deleted,
// even when making that change durable then fails. A non-empty reason means
// the namespace was left as it is.
func (s *Store) relocateOverlay(handle, namespace string) (m *OverlayMove, reason string, err error) {
	legacy := s.legacyExtLocalPath(handle, namespace)
	local := s.extLocalPath(handle, namespace)
	var rp *repositoryPathError
	if err := s.checkPath(legacy, false); errors.As(err, &rp) {
		return nil, "legacy overlay is refused: " + rp.reason + " at " + rp.path + "; left in place", nil
	} else if err != nil {
		return nil, "", err
	}
	if _, err := os.Lstat(legacy); errors.Is(err, os.ErrNotExist) {
		return nil, "", nil
	} else if err != nil {
		return nil, "", fmt.Errorf("examining legacy overlay %s: %w", legacy, err)
	}
	m = &OverlayMove{Legacy: legacy, Local: local}
	if err := s.checkPath(local, false); errors.As(err, &rp) {
		return nil, "local overlay is refused: " + rp.reason + " at " + rp.path + "; legacy copy kept", nil
	} else if err != nil {
		return nil, "", err
	}
	_, err = os.Lstat(local)
	switch {
	case err == nil:
		// The legacy copy may be the only readable one: delete it only after the
		// kept copy parses.
		if _, err := s.readExtFile(local); err != nil {
			return nil, "local overlay unreadable; legacy copy kept", nil
		}
		m.Kept = true
		if err := s.removeFile(legacy); err != nil {
			return nil, "", fmt.Errorf("deleting legacy overlay %s: %w", legacy, err)
		}
		return m, "", nil
	case !errors.Is(err, os.ErrNotExist):
		return nil, "", fmt.Errorf("examining overlay %s: %w", local, err)
	}
	if err := s.ensureDir(filepath.Dir(local)); err != nil {
		return nil, "", err
	}
	if err := os.Rename(legacy, local); err != nil {
		return nil, "", fmt.Errorf("moving overlay %s to %s: %w", legacy, local, err)
	}
	// The local-zone writer creates overlays 0600; a legacy file may be wider.
	if err := os.Chmod(local, 0o600); err != nil {
		return m, "", fmt.Errorf("restricting overlay %s: %w", local, err)
	}
	if err := s.confirmPublication(local); err != nil {
		return m, "", err
	}
	if err := s.confirmPublication(legacy); err != nil {
		return m, "", err
	}
	return m, "", nil
}
