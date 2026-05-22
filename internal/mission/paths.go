//go:build !windows

package mission

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// missionLayer identifies which storage tree a mission lives in.
// DES-054 phase 1 introduces a per-repo tree under <repoRoot>/.ethos/
// alongside the legacy global tree under <globalRoot>/missions/. A
// given mission lives in exactly one layer at a time; the migration
// command (phase 3) is the only path that copies between layers.
type missionLayer int

const (
	// layerUnset is the default. A Store with repoRoot == "" only
	// ever sees this value — there is no per-repo tree to dispatch
	// to, so every read and write goes through the legacy
	// global-rooted paths.
	layerUnset missionLayer = iota

	// layerRepo means the mission lives at
	// <repoRoot>/.ethos/missions/<missionID>/. Create writes here
	// when repoRoot is set; Load reads here first.
	layerRepo

	// layerGlobal means the mission lives at the legacy global
	// path <globalRoot>/missions/<missionID>.yaml. Reads fall back
	// here when the per-repo tree does not carry the mission.
	layerGlobal
)

// pathSet is the bundle of on-disk paths for a single mission. Each
// field is the absolute path to the artifact in the layer the set
// was constructed for. Holding them together avoids re-computing the
// layer on every helper call inside a locked section.
type pathSet struct {
	layer       missionLayer
	dir         string // for layerRepo: the per-mission directory
	contract    string
	log         string
	results     string
	reflections string
	lock        string // always under the global tree per DES-054 concurrency model
}

// repoMissionsDir returns the per-repo missions root —
// <repoRoot>/.ethos/missions. Empty when the two-tree storage mode
// is not active (legacy WithRepoRoot trace-only setups stay empty
// here so List and resolveLayer do not pick up a partial layout).
func (s *Store) repoMissionsDir() string {
	if !s.twoTreeStorage || s.repoRoot == "" {
		return ""
	}
	return filepath.Join(s.repoRoot, ".ethos", "missions")
}

// globalMissionsDir returns the legacy single-root path —
// <root>/missions. Always present even when repoRoot is set; the
// lock files and the fallback read path live here.
func (s *Store) globalMissionsDir() string {
	return filepath.Join(s.root, "missions")
}

// pathSetFor returns the pathSet for a mission in the named layer.
// The layer argument is explicit so a locked caller that has
// already decided where the mission lives does not pay a second
// stat. Pass layerUnset to get the legacy single-root layout.
func (s *Store) pathSetFor(missionID string, layer missionLayer) pathSet {
	id := filepath.Base(missionID)
	switch layer {
	case layerRepo:
		dir := filepath.Join(s.repoMissionsDir(), id)
		return pathSet{
			layer:       layerRepo,
			dir:         dir,
			contract:    filepath.Join(dir, "contract.yaml"),
			log:         filepath.Join(dir, "log.jsonl"),
			results:     filepath.Join(dir, "results.yaml"),
			reflections: filepath.Join(dir, "reflections.yaml"),
			lock:        filepath.Join(s.globalMissionsDir(), id+".lock"),
		}
	default:
		// layerGlobal and layerUnset share the legacy layout.
		dir := s.globalMissionsDir()
		return pathSet{
			layer:       layer,
			dir:         dir,
			contract:    filepath.Join(dir, id+".yaml"),
			log:         filepath.Join(dir, id+".jsonl"),
			results:     filepath.Join(dir, id+".results.yaml"),
			reflections: filepath.Join(dir, id+".reflections.yaml"),
			lock:        filepath.Join(dir, id+".lock"),
		}
	}
}

// resolveLayer reports where missionID lives on disk. The order is
// repo-first (when configured) then global; a mission seen only in
// the global tree is operated on in place, never silently migrated.
// Returns layerUnset when neither tree carries the mission, which
// the caller interprets as "not found".
func (s *Store) resolveLayer(missionID string) (missionLayer, error) {
	if s.twoTreeStorage && s.repoRoot != "" {
		repoSet := s.pathSetFor(missionID, layerRepo)
		switch _, err := os.Stat(repoSet.contract); {
		case err == nil:
			return layerRepo, nil
		case !errors.Is(err, fs.ErrNotExist):
			return layerUnset, err
		}
	}
	globalSet := s.pathSetFor(missionID, layerGlobal)
	switch _, err := os.Stat(globalSet.contract); {
	case err == nil:
		return layerGlobal, nil
	case errors.Is(err, fs.ErrNotExist):
		return layerUnset, nil
	default:
		return layerUnset, err
	}
}

// writeLayer reports which layer a new mission should be written
// to. When repoRoot is set, all new missions land in the per-repo
// tree (DES-054 phase 1: the global tree is read-only for v3.12.0
// new creates). When repoRoot is empty, the legacy global tree
// stays the write target.
func (s *Store) writeLayer() missionLayer {
	if s.twoTreeStorage && s.repoRoot != "" {
		return layerRepo
	}
	return layerGlobal
}

// pathSetForExisting returns the pathSet for a mission's existing
// on-disk location. Callers that need the path for a mission they
// have just loaded use this so the read and any follow-on write
// hit the same files.
func (s *Store) pathSetForExisting(missionID string) (pathSet, error) {
	layer, err := s.resolveLayer(missionID)
	if err != nil {
		return pathSet{}, err
	}
	if layer == layerUnset {
		// Caller sees the same shape as a missing file from a
		// downstream os.Open — the wrapping error message names
		// the mission so the operator sees what was missed.
		return pathSet{}, fs.ErrNotExist
	}
	return s.pathSetFor(missionID, layer), nil
}
