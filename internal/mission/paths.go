package mission

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Symlink policy: every mission and delegation loader, lock acquirer,
// and writer in this package REFUSES to follow a symlink at any
// resolved per-mission, per-delegation, or per-artifact path. The
// "resolve and validate" alternative (follow then check enclosing
// layer root) would have to track the layer root for every loader
// path and re-validate after every Readlink — more surface area, more
// branches, larger blast radius for a future bug. Repository authority also
// validates every existing ancestor against its captured policy before use.
//
// The check is mechanical (Lstat + ModeSymlink) and fires before any
// follow-on open or stat that would dereference the link. Callers see
// a single error string ("refusing to follow symlink: <path>") so the
// operator can locate the offending path immediately.
//
// A missing path is NOT a symlink and is allowed through — the
// follow-on open is responsible for distinguishing "does not exist"
// from a real I/O error.

// rejectSymlink returns an error if path is a symbolic link. Symlinks
// inside the missions or delegations trees are a local-attacker
// vector: a symlink pointing outside the store can redirect a read
// (to an arbitrary file the ethos process can open) or a write (via
// temp+rename or O_APPEND on the link target) past the write_set
// boundary. Lstat + ModeSymlink is the standard non-following check.
//
// Returns nil for a missing path so the caller's follow-on open can
// produce the natural fs.ErrNotExist diagnostic — distinguishing
// "missing" from "is a symlink" is the caller's responsibility, not
// this helper's.
func rejectSymlink(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("lstat %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to follow symlink: %s", path)
	}
	return nil
}

// validatePhysicalPath refuses stable redirects, including dangling symlinks,
// before a read, mkdir or install. Check from the filesystem root downward:
// Lstat of only the leaf would follow an ancestor, and ENOENT there could hide
// a dangling redirect. A genuinely absent suffix is allowed. This is a placement
// check for cooperative paths, not protection against concurrent replacement.
func validatePhysicalPath(path string, directory bool) error {
	if !filepath.IsAbs(path) || path != filepath.Clean(path) {
		return fmt.Errorf("mission path must be clean and absolute: %s", path)
	}
	var chain []string
	for current := path; ; current = filepath.Dir(current) {
		chain = append(chain, current)
		if filepath.Dir(current) == current {
			break
		}
	}
	for i := len(chain) - 1; i >= 0; i-- {
		info, err := os.Lstat(chain[i])
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lstat %s: %w", chain[i], err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to follow symlink: %s", chain[i])
		}
		if (i > 0 || directory) && !info.IsDir() {
			return fmt.Errorf("mission path ancestor is not a directory: %s", chain[i])
		}
	}
	return nil
}

// validateRepositoryRecordPath uses the caller's named repository, never a root
// rediscovered from a redirected target or the first textual .punt-labs marker.
func validateRepositoryRecordPath(repoRoot, path string) error {
	if repoRoot == "" || !filepath.IsAbs(repoRoot) {
		return fmt.Errorf("mission record requires an absolute repository root")
	}
	rel, err := filepath.Rel(RepoStatePath(repoRoot), path)
	if err != nil || !filepath.IsLocal(rel) || rel == "." {
		return fmt.Errorf("mission record path is outside the named repository: %s", path)
	}
	return validatePhysicalPath(path, false)
}

// validateRepositoryDelegationPath covers path-only convenience loaders too.
// Recognize the exact repository suffix, not the first .punt-labs segment.
// Other standalone record locations retain their existing loader contract.
// Abs is lexical only: it must not resolve/adopt a symlink target as authority.
func validateRepositoryDelegationPath(path string) (string, error) {
	if filepath.Base(path) != "record.yaml" {
		return path, nil
	}
	isRepositoryDelegationDir := func(dir string) bool {
		missionDir := filepath.Dir(filepath.Dir(filepath.Dir(dir)))
		recordRoot := filepath.Dir(missionDir)
		return filepath.Base(filepath.Dir(dir)) == "delegations" &&
			filepath.Base(missionDir) == "missions" && filepath.Base(recordRoot) == "ethos" &&
			filepath.Base(filepath.Dir(recordRoot)) == ".punt-labs"
	}
	if path != filepath.Clean(path) {
		// Split, unlike Dir, preserves the raw prefixes. Inspect them before
		// '..' can erase either a traversed link or the repository markers.
		// This recognizes named layout only; it does not discover a new root.
		for raw := path; raw != ""; {
			dir, _ := filepath.Split(raw)
			if dir == "" {
				break
			}
			raw = dir[:len(dir)-1] // Remove just the trailing native separator.
			if isRepositoryDelegationDir(raw) {
				return "", fmt.Errorf("repository delegation path must be clean: %s", path)
			}
		}
	}
	if !isRepositoryDelegationDir(filepath.Dir(path)) {
		return path, nil
	}
	abs := path
	if !filepath.IsAbs(path) {
		// Do not guess a separate Windows drive/root-relative base.
		if filepath.VolumeName(path) != "" || os.IsPathSeparator(path[0]) {
			return "", fmt.Errorf("repository delegation path requires an absolute path or an ordinary relative path: %s", path)
		}
		base, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("locating delegation working directory: %w", err)
		}
		// Getwd can return a valid logical PWD containing symlinks or dots.
		// Check that exact base before Join can erase its components with '..'.
		if err := validatePhysicalPath(base, true); err != nil {
			return "", fmt.Errorf("delegation working directory: %w", err)
		}
		abs = filepath.Join(base, path)
	}
	if err := validatePhysicalPath(abs, false); err != nil {
		return "", err
	}
	return abs, nil // The loader must read the same pathname that was validated.
}

// EthosRepoStateRoot is the path, relative to a repository root,
// under which every per-repo file ethos writes lives. The .punt-labs/
// prefix is the shared Punt Labs cross-tool namespace — sibling tools
// (biff, vox, beadle, lux, quarry) read and write alongside ethos at
// <repo>/.punt-labs/<tool>/. Exported so external integrations
// (sibling tools, audit consumers) can construct paths without
// hardcoding the literal.
//
// Callers compose paths via RepoStatePath. A future relocation
// changes one constant instead of every filepath.Join site.
const EthosRepoStateRoot = ".punt-labs/ethos"

// RepoStatePath joins repoRoot with the ethos per-repo state root and
// any trailing path segments. Returns just the state root when no
// trailing parts are given. Exported so external consumers can locate
// ethos state without depending on the internal layout constants.
func RepoStatePath(repoRoot string, parts ...string) string {
	all := append([]string{repoRoot, ".punt-labs", "ethos"}, parts...)
	return filepath.Join(all...)
}

// missionLayer identifies which storage tree a mission lives in.
// DES-054 phase 1 introduces a per-repo tree under
// <repoRoot>/.punt-labs/ethos/ alongside the legacy global tree under
// <globalRoot>/missions/. A given mission lives in exactly one layer
// at a time; the migration command (phase 3) is the only path that
// copies between layers.
type missionLayer int

const (
	// layerUnset is the default. A Store with repoRoot == "" only
	// ever sees this value — there is no per-repo tree to dispatch
	// to, so every read and write goes through the legacy
	// global-rooted paths.
	layerUnset missionLayer = iota

	// layerRepo means the mission lives at
	// <repoRoot>/.punt-labs/ethos/missions/<missionID>/. Create
	// writes here when repoRoot is set; Load reads here first.
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
	lock        string // layer owner's state lock (selection also needs S's fence)
}

// repoMissionsDir returns the per-repo missions root —
// <repoRoot>/.punt-labs/ethos/missions. Empty when the two-tree
// storage mode is not active (legacy WithRepoRoot trace-only setups
// stay empty here so List and resolveLayer do not pick up a partial
// layout).
func (s *Store) repoMissionsDir() string {
	if !s.twoTreeStorage || s.repoRoot == "" {
		return ""
	}
	return RepoStatePath(s.repoRoot, "missions")
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
			lock:        s.lockPath(missionID),
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
	if s.policyErr != nil {
		return layerUnset, s.policyErr
	}
	if s.twoTreeStorage && s.repoRoot != "" {
		repoSet := s.pathSetFor(missionID, layerRepo)
		if err := validateRepositoryRecordPath(s.repoRoot, repoSet.contract); err != nil {
			return layerUnset, err
		}
		switch _, err := os.Stat(repoSet.contract); {
		case err == nil:
			return layerRepo, nil
		case !errors.Is(err, fs.ErrNotExist):
			return layerUnset, err
		}
	}
	globalSet := s.pathSetFor(missionID, layerGlobal)
	if err := rejectSymlink(globalSet.contract); err != nil {
		return layerUnset, err
	}
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
