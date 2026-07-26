package seed

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// scope names the destination root a seeded path resolves against. It is
// recorded in each manifest entry so one manifest covers both roots.
const (
	scopeEthos  = "ethos"
	scopeSkills = "skills"
)

// Result tracks what was seeded.
type Result struct {
	Deployed  []string // new files written (were absent)
	Updated   []string // tracked shipped files upgraded to this release's content
	Unchanged []string // already at this release's content
	Skipped   []string // untracked existing files left as-is (no-clobber)
	Edited    []string // tracked and locally edited — differ from the manifest
	Repaired  []string // zero-byte files overwritten (partial from an interrupted seed)
	Errors    []string // files that failed
}

// Seed deploys embedded sidecar content to the destination root, recording no
// version provenance. It is SeedVersion with an empty version, kept so callers
// without a version string need not supply one.
func Seed(destRoot, skillsRoot string, force bool) (*Result, error) {
	return SeedVersion(destRoot, skillsRoot, "", force)
}

// SeedVersion deploys embedded sidecar content to the destination root.
// destRoot is typically ~/.punt-labs/ethos/.
// skillsRoot is typically ~/.claude/skills/.
// version stamps each manifest entry as provenance.
//
// New upgrade behavior applies only to files the manifest already tracks: a
// tracked file unchanged since seed last wrote it is upgraded when the release
// ships something newer, and a tracked file the user has edited is preserved.
// An untracked existing file keeps the original no-clobber skip — the manifest
// era is entered by the first seed that writes a file (or by a one-time
// `--force`). If force is true, every differing file is overwritten with the
// shipped content.
func SeedVersion(destRoot, skillsRoot, version string, force bool) (*Result, error) {
	mf, err := loadManifest(destRoot)
	if err != nil {
		return nil, err
	}
	s := &seeder{
		destRoot:   destRoot,
		skillsRoot: skillsRoot,
		version:    version,
		force:      force,
		mf:         mf,
		r:          &Result{},
	}

	// Roles (skip README.md — handled separately)
	s.seedFS(Roles, "sidecar/roles", filepath.Join(destRoot, "roles"), ".yaml")

	// Talents (skip README.md — handled separately)
	s.seedFS(Talents, "sidecar/talents", filepath.Join(destRoot, "talents"), ".md")

	// Personalities and writing-styles: the conventional attributes that
	// setup-created identities reference, plus starter sidecar content.
	// A fresh machine resolves these from global when no bundle is active.
	s.seedFS(Personalities, "sidecar/personalities", filepath.Join(destRoot, "personalities"), ".md")
	s.seedFS(WritingStyles, "sidecar/writing-styles", filepath.Join(destRoot, "writing-styles"), ".md")

	// Archetypes
	s.seedFS(Archetypes, "sidecar/archetypes", filepath.Join(destRoot, "archetypes"), ".yaml")

	// Pipelines
	s.seedFS(Pipelines, "sidecar/pipelines", filepath.Join(destRoot, "pipelines"), ".yaml")

	// Skills
	s.seedFile(Skills, "sidecar/skills/baseline-ops/SKILL.md",
		filepath.Join(skillsRoot, "baseline-ops", "SKILL.md"))
	s.seedFile(Skills, "sidecar/skills/mission/SKILL.md",
		filepath.Join(skillsRoot, "mission", "SKILL.md"))
	s.seedFile(Skills, "sidecar/skills/create-from-project/SKILL.md",
		filepath.Join(skillsRoot, "create-from-project", "SKILL.md"))

	// READMEs
	s.seedReadmes(Readmes, destRoot)

	// Bundles (gstack and any other embedded team bundles).
	// Each top-level directory under sidecar/bundles/ deploys to
	// <destRoot>/bundles/<name>/ preserving its internal structure.
	s.seedBundles(Bundles, filepath.Join(destRoot, "bundles"))

	if err := s.mf.save(destRoot); err != nil {
		s.r.Errors = append(s.r.Errors, fmt.Sprintf("saving seed manifest: %v", err))
	}

	if len(s.r.Errors) > 0 {
		return s.r, fmt.Errorf("seed encountered %d errors", len(s.r.Errors))
	}
	return s.r, nil
}

// seeder carries the roots, version, and manifest through one Seed run so the
// per-file decision has everything it needs without long parameter lists.
type seeder struct {
	destRoot   string
	skillsRoot string
	version    string
	force      bool
	mf         *Manifest
	r          *Result
}

func (s *seeder) seedFS(fsys embed.FS, root, destDir, ext string) {
	entries, err := fs.ReadDir(fsys, root)
	if err != nil {
		s.r.Errors = append(s.r.Errors, fmt.Sprintf("reading %s: %v", root, err))
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ext) {
			continue
		}
		if e.Name() == "README.md" {
			continue
		}
		src := root + "/" + e.Name()
		data, err := fs.ReadFile(fsys, src)
		if err != nil {
			s.r.Errors = append(s.r.Errors, fmt.Sprintf("reading %s: %v", e.Name(), err))
			continue
		}
		s.place(scopeEthos, filepath.Join(destDir, e.Name()), data)
	}
}

func (s *seeder) seedFile(fsys embed.FS, src, dest string) {
	data, err := fs.ReadFile(fsys, src)
	if err != nil {
		s.r.Errors = append(s.r.Errors, fmt.Sprintf("reading %s: %v", src, err))
		return
	}
	s.place(scopeSkills, dest, data)
}

func (s *seeder) seedReadmes(fsys embed.FS, destRoot string) {
	err := fs.WalkDir(fsys, "sidecar", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			s.r.Errors = append(s.r.Errors, fmt.Sprintf("walking %s: %v", path, walkErr))
			return nil
		}
		if d.IsDir() || d.Name() != "README.md" {
			return nil
		}
		// path is like "sidecar/roles/README.md"
		// rel becomes "roles/README.md"
		rel, relErr := filepath.Rel("sidecar", path)
		if relErr != nil {
			s.r.Errors = append(s.r.Errors, fmt.Sprintf("computing relative path for %s: %v", path, relErr))
			return nil
		}
		data, readErr := fs.ReadFile(fsys, path)
		if readErr != nil {
			s.r.Errors = append(s.r.Errors, fmt.Sprintf("reading %s: %v", path, readErr))
			return nil
		}
		s.place(scopeEthos, filepath.Join(destRoot, rel), data)
		return nil
	})
	if err != nil {
		s.r.Errors = append(s.r.Errors, fmt.Sprintf("walking sidecar for READMEs: %v", err))
	}
}

// seedBundles walks every file under sidecar/bundles/ and copies it
// to destBundlesRoot, preserving the path below "sidecar/bundles/".
// For example, sidecar/bundles/gstack/teams/gstack.yaml lands at
// <destBundlesRoot>/gstack/teams/gstack.yaml.
func (s *seeder) seedBundles(fsys embed.FS, destBundlesRoot string) {
	const root = "sidecar/bundles"
	err := fs.WalkDir(fsys, root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			s.r.Errors = append(s.r.Errors, fmt.Sprintf("walking %s: %v", path, walkErr))
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			s.r.Errors = append(s.r.Errors, fmt.Sprintf("computing relative path for %s: %v", path, relErr))
			return nil
		}
		data, readErr := fs.ReadFile(fsys, path)
		if readErr != nil {
			s.r.Errors = append(s.r.Errors, fmt.Sprintf("reading %s: %v", path, readErr))
			return nil
		}
		s.place(scopeEthos, filepath.Join(destBundlesRoot, rel), data)
		return nil
	})
	if err != nil {
		s.r.Errors = append(s.r.Errors, fmt.Sprintf("walking bundles: %v", err))
	}
}

// place deploys data to dest under the given scope, deciding by content hash
// and the install manifest whether to write, skip, or leave it unchanged.
func (s *seeder) place(scope, dest string, data []byte) {
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		s.r.Errors = append(s.r.Errors, fmt.Sprintf("mkdir %s: %v", filepath.Dir(dest), err))
		return
	}

	cur := hashBytes(data)
	if handled := s.classifyExisting(scope, dest, data, cur); handled {
		return
	}

	// dest was absent — create it atomically without clobbering a file that
	// races in between the Stat above and this create.
	switch err := linkInstall(dest, data); {
	case err == nil:
		s.record(scope, dest, cur)
		s.r.Deployed = append(s.r.Deployed, dest)
	case errors.Is(err, os.ErrExist):
		// A file appeared in the race window. Re-decide against it once.
		if handled := s.classifyExisting(scope, dest, data, cur); !handled {
			s.r.Errors = append(s.r.Errors,
				fmt.Sprintf("writing %s: file appeared and vanished during install", dest))
		}
	default:
		s.r.Errors = append(s.r.Errors, fmt.Sprintf("writing %s: %v", dest, err))
	}
}

// classifyExisting acts on a dest that already exists: a directory is a hard
// error, a zero-byte file is repaired, a non-empty file is passed to the
// content-hash decision. It returns true when dest existed (action taken or
// error recorded), false when dest is absent and the caller should create it.
func (s *seeder) classifyExisting(scope, dest string, data []byte, cur string) bool {
	info, err := os.Stat(dest)
	switch {
	case err == nil && info.IsDir():
		// A directory where a file belongs is corruption/misplacement: it
		// passes a Size()>0 check but the real file can never deploy, so fail
		// loud rather than silently skip it.
		s.r.Errors = append(s.r.Errors, fmt.Sprintf("%s is a directory, expected a file", dest))
		return true
	case err == nil && info.Size() == 0:
		// A zero-byte file is a partial write left by an interrupted seed.
		// Repair replaces it with the shipped content. The only writers to
		// these paths are seed itself with deterministic embedded content, so
		// a concurrent user hand-edit landing in this window is negligible.
		if werr := atomicWrite(dest, data); werr != nil {
			s.r.Errors = append(s.r.Errors, fmt.Sprintf("repairing %s: %v", dest, werr))
			return true
		}
		s.record(scope, dest, cur)
		s.r.Repaired = append(s.r.Repaired, dest)
		return true
	case err == nil:
		s.decide(scope, dest, data, cur)
		return true
	case !os.IsNotExist(err):
		s.r.Errors = append(s.r.Errors, fmt.Sprintf("stat %s: %v", dest, err))
		return true
	}
	return false // absent
}

// decide handles a non-empty existing file. It reports a file already at the
// current content as unchanged; upgrades a tracked file unchanged since seed
// last wrote it; preserves a tracked file the user has edited; and keeps the
// original no-clobber skip for an untracked file. Under force, every differing
// file is overwritten.
func (s *seeder) decide(scope, dest string, data []byte, cur string) {
	local, err := hashFile(dest)
	if err != nil {
		s.r.Errors = append(s.r.Errors, fmt.Sprintf("hashing %s: %v", dest, err))
		return
	}
	key := s.key(scope, dest)

	if local == cur {
		// Already this release's content. Adopt an untracked file so a later
		// edit becomes detectable.
		if _, ok := s.mf.Entries[key]; !ok {
			s.record(scope, dest, cur)
		}
		s.r.Unchanged = append(s.r.Unchanged, dest)
		return
	}

	if s.force {
		s.write(scope, dest, data, cur, &s.r.Updated)
		return
	}

	entry, tracked := s.mf.Entries[key]
	switch {
	case !tracked:
		// An untracked existing file that differs is left untouched — the
		// original no-clobber contract. It enters the manifest era only when
		// seed writes it (a fresh deploy) or under a one-time `--force`.
		s.r.Skipped = append(s.r.Skipped, dest)
	case local != entry.Hash:
		// Tracked but changed since seed last wrote it — a user edit; preserve.
		s.r.Edited = append(s.r.Edited, dest)
	default:
		// Tracked and unchanged since our last write, but the release ships
		// something newer — upgrade.
		s.write(scope, dest, data, cur, &s.r.Updated)
	}
}

// write overwrites dest with data, records the manifest entry, and appends dest
// to the given Result bucket.
func (s *seeder) write(scope, dest string, data []byte, cur string, bucket *[]string) {
	if err := atomicWrite(dest, data); err != nil {
		s.r.Errors = append(s.r.Errors, fmt.Sprintf("writing %s: %v", dest, err))
		return
	}
	s.record(scope, dest, cur)
	*bucket = append(*bucket, dest)
}

// record sets the manifest entry for dest to the given content hash.
func (s *seeder) record(scope, dest, cur string) {
	s.mf.Entries[s.key(scope, dest)] = Entry{
		Scope:   scope,
		Hash:    cur,
		Version: s.version,
		Written: time.Now().UTC().Format(time.RFC3339),
	}
}

// key returns the manifest key for dest: dest-relative under the ethos root, or
// "skills/…" under the skills root.
func (s *seeder) key(scope, dest string) string {
	if scope == scopeSkills {
		rel, err := filepath.Rel(s.skillsRoot, dest)
		if err != nil {
			rel = filepath.Base(dest)
		}
		return "skills/" + filepath.ToSlash(rel)
	}
	rel, err := filepath.Rel(s.destRoot, dest)
	if err != nil {
		rel = filepath.Base(dest)
	}
	return filepath.ToSlash(rel)
}

// linkInstall writes data to a temp file in dest's directory, then hard-links
// it to dest. os.Link fails with os.ErrExist if dest already exists, giving
// an atomic no-clobber create. The temp file is always removed.
func linkInstall(dest string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(dest), filepath.Base(dest)+".seed.*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return err
	}
	return os.Link(tmpPath, dest)
}

// atomicWrite writes data to a temp file in dest's directory, then renames
// it over dest. A kill at any point leaves either the old file or the new
// complete one — never a partial file at dest.
func atomicWrite(dest string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(dest), filepath.Base(dest)+".seed.*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}
