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

// RelocateLegacyOverlays moves every repository overlay still in its pre-DES-079
// sibling location (<handle>.ext/<ns>.local.yaml beside the identity) to the
// local zone. When the local destination exists it wins and the legacy copy is
// deleted. Each handle is handled under its identity lock; a handle whose lock
// is held elsewhere is skipped and reported through busy. The merged identity is
// never read or written: file bytes move as they are.
//
// It returns the moves applied so far together with the first error.
func (s *Store) RelocateLegacyOverlays() (moves []OverlayMove, busy bool, err error) {
	if !s.config.repository {
		return nil, false, nil
	}
	if err := s.validatePolicy(); err != nil {
		return nil, false, err
	}
	entries, err := s.readDir(s.identitiesDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("listing identities for overlay relocation: %w", err)
	}
	var handles []string
	for _, e := range entries {
		if e.IsDir() && strings.HasSuffix(e.Name(), ".ext") {
			handles = append(handles, strings.TrimSuffix(e.Name(), ".ext"))
		}
	}
	sort.Strings(handles)
	for _, h := range handles {
		legacy, err := s.legacyOverlays(h)
		if err != nil {
			return moves, busy, err
		}
		if len(legacy) == 0 {
			continue
		}
		err = s.withTryLock(h, func() error {
			for _, ns := range legacy {
				m, err := s.relocateOverlay(h, ns)
				if m != nil {
					moves = append(moves, *m)
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
			return moves, busy, err
		}
	}
	return moves, busy, nil
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
// even when making that change durable then fails.
func (s *Store) relocateOverlay(handle, namespace string) (*OverlayMove, error) {
	legacy := s.legacyExtLocalPath(handle, namespace)
	local := s.extLocalPath(handle, namespace)
	if err := s.checkPath(legacy, false); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(legacy); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("examining legacy overlay %s: %w", legacy, err)
	}
	m := &OverlayMove{Legacy: legacy, Local: local}
	if err := s.checkPath(local, false); err != nil {
		return nil, err
	}
	_, err := os.Lstat(local)
	switch {
	case err == nil:
		m.Kept = true
		if err := s.removeFile(legacy); err != nil {
			return nil, fmt.Errorf("deleting legacy overlay %s: %w", legacy, err)
		}
		return m, nil
	case !errors.Is(err, os.ErrNotExist):
		return nil, fmt.Errorf("examining overlay %s: %w", local, err)
	}
	if err := s.ensureDir(filepath.Dir(local)); err != nil {
		return nil, err
	}
	if err := os.Rename(legacy, local); err != nil {
		return nil, fmt.Errorf("moving overlay %s to %s: %w", legacy, local, err)
	}
	if err := s.confirmPublication(local); err != nil {
		return m, err
	}
	if err := s.confirmPublication(legacy); err != nil {
		return m, err
	}
	return m, nil
}
