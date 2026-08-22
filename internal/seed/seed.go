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
	return SeedVersion(destRoot, skillsRoot, "", "", force)
}

// SeedVersion deploys embedded sidecar content with no active bundle. It is
// SeedVersionWithBundle with an empty activeBundle, kept so existing callers
// (and every test built against this signature) need not name a bundle.
func SeedVersion(destRoot, skillsRoot, agentsRoot, version string, force bool) (*Result, error) {
	return SeedVersionWithBundle(destRoot, skillsRoot, agentsRoot, "", version, force)
}

// SeedVersionWithBundle deploys embedded sidecar content to the destination
// root.
// destRoot is typically ~/.punt-labs/ethos/.
// skillsRoot is typically ~/.claude/skills/.
// agentsRoot is typically <repo>/.claude/agents/ — a per-repo destination,
// not under destRoot, since the seeded review-checklist agents (DES-070) are
// Claude Code subagent definitions local to a repo's own checkout. An empty
// agentsRoot skips this category entirely — a caller with no repo in scope
// (e.g. a bare global seed) leaves it unseeded rather than guessing a path.
// activeBundle names the repo's active_bundle (DES-073); when non-empty and
// the bundle ships a sidecar/bundles/<activeBundle>/skills/ tree, each skill
// there is deployed to skillsRoot alongside the sidecar top-level skills.
// version stamps each manifest entry as provenance.
//
// New upgrade behavior applies only to files the manifest already tracks: a
// tracked file unchanged since seed last wrote it is upgraded when the release
// ships something newer, and a tracked file the user has edited is preserved.
// An untracked existing file keeps the original no-clobber skip — the manifest
// era is entered by the first seed that writes a file (or by a one-time
// `--force`). If force is true, every differing file is overwritten with the
// shipped content.
func SeedVersionWithBundle(destRoot, skillsRoot, agentsRoot, activeBundle, version string, force bool) (*Result, error) {
	mf, err := loadManifest(destRoot)
	if err != nil {
		// A present-but-unreadable or corrupt manifest must not be treated as a
		// fresh machine: seeding would drop tracked-file upgrades and save()
		// would overwrite the manifest, making the loss durable. Fail before any
		// write, surfacing the cause in Result.Errors.
		return &Result{Errors: []string{err.Error()}}, err
	}
	s := &seeder{
		destRoot:     destRoot,
		skillsRoot:   skillsRoot,
		agentsRoot:   agentsRoot,
		activeBundle: activeBundle,
		version:      version,
		force:        force,
		mf:           mf,
		r:            &Result{},
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

	// Review-checklist agents (DES-070): personaless Claude Code subagent
	// definitions, deployed to the caller's repo-local .claude/agents/
	// rather than under destRoot — see SeedVersion's agentsRoot doc.
	if s.agentsRoot != "" {
		s.seedFS(Agents, "sidecar/agents", s.agentsRoot, ".md")
	}

	// Skills
	s.seedFile(Skills, "sidecar/skills/baseline-ops/SKILL.md",
		filepath.Join(skillsRoot, "baseline-ops", "SKILL.md"))
	s.seedFile(Skills, "sidecar/skills/mission/SKILL.md",
		filepath.Join(skillsRoot, "mission", "SKILL.md"))
	s.seedFile(Skills, "sidecar/skills/create-from-project/SKILL.md",
		filepath.Join(skillsRoot, "create-from-project", "SKILL.md"))

	// Bundle-scoped skills (DES-073): deployed after the sidecar top-level
	// skills above, so a slug collision resolves bundle-wins per the ADR.
	s.seedBundleSkills(Bundles, s.activeBundle)

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
	destRoot     string
	skillsRoot   string
	agentsRoot   string
	activeBundle string
	version      string
	force        bool
	mf           *Manifest
	r            *Result
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

// topLevelSkillSlugs returns every sidecar top-level skill directory name,
// enumerated from the embedded Skills FS rather than hardcoded — a new
// skill added under sidecar/skills/ is picked up with no change here.
// seedBundleSkills checks a bundle-scoped slug against this set to detect
// a collision. A read error yields an empty set: seedBundleSkills then
// treats every bundle slug as non-colliding, which is the same behavior
// (deploy via the normal no-clobber path) as before this set existed.
func topLevelSkillSlugs() map[string]bool {
	entries, err := fs.ReadDir(Skills, "sidecar/skills")
	if err != nil {
		return nil
	}
	slugs := make(map[string]bool, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			slugs[e.Name()] = true
		}
	}
	return slugs
}

// seedBundleSkills deploys bundleName's skills/<slug>/SKILL.md tree (if
// any) to skillsRoot, alongside the sidecar top-level skills (DES-073).
// bundleName empty is a no-op — no active bundle, nothing to deploy. A
// bundle with no skills/ subtree is also a silent no-op: not every
// bundle ships skills.
//
// Namespacing: a bundle-scoped slug keeps its name as-is. When it
// collides with a sidecar top-level slug, the bundle wins — this method
// runs after the sidecar skills are seeded, and s.place's content-hash
// decision would otherwise treat a genuine bundle-vs-sidecar difference
// as a "local edit" and preserve the sidecar copy. A logged warning
// plus a direct write (bypassing the no-clobber/edited-preserve path)
// makes the ADR's ruling ("bundle scope wins, log a warning") actually
// take effect instead of silently losing to the preserve-edit rule.
func (s *seeder) seedBundleSkills(fsys embed.FS, bundleName string) {
	if bundleName == "" {
		return
	}
	root := "sidecar/bundles/" + bundleName + "/skills"
	entries, err := fs.ReadDir(fsys, root)
	if err != nil {
		if os.IsNotExist(err) {
			return // this bundle ships no skills — not every bundle does
		}
		s.r.Errors = append(s.r.Errors, fmt.Sprintf("reading %s: %v", root, err))
		return
	}
	topLevel := topLevelSkillSlugs()
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		slug := e.Name()
		src := root + "/" + slug + "/SKILL.md"
		data, readErr := fs.ReadFile(fsys, src)
		if readErr != nil {
			s.r.Errors = append(s.r.Errors, fmt.Sprintf("reading %s: %v", src, readErr))
			continue
		}
		dest := filepath.Join(s.skillsRoot, slug, "SKILL.md")
		if topLevel[slug] {
			fmt.Fprintf(os.Stderr,
				"ethos: seed: bundle %q skill %q collides with a sidecar skill; bundle wins\n",
				bundleName, slug)
			if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
				s.r.Errors = append(s.r.Errors, fmt.Sprintf("mkdir %s: %v", filepath.Dir(dest), err))
				continue
			}
			s.write(scopeSkills, dest, data, hashBytes(data), &s.r.Updated)
			continue
		}
		s.place(scopeSkills, dest, data)
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
		// The file already holds this release's content, so its entry must be
		// cur. Always record — this adopts an untracked file and self-corrects a
		// stale entry left when a prior seed wrote the file but crashed before
		// saving the manifest. Without the refresh, the next release would see
		// local(unchanged shipped) != cur and entry != local, misread it as a
		// user edit, and silently drop the upgrade.
		s.record(scope, dest, cur)
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
// "skills/…" under the skills root. If dest cannot be made relative to its root
// (it should always be — every dest is built by joining onto the root), the
// full cleaned path is used rather than a basename, which two different dests
// could share and silently collide on.
func (s *seeder) key(scope, dest string) string {
	if scope == scopeSkills {
		if rel, err := filepath.Rel(s.skillsRoot, dest); err == nil && !escapes(rel) {
			return "skills/" + filepath.ToSlash(rel)
		}
		return filepath.ToSlash(filepath.Clean(dest))
	}
	if rel, err := filepath.Rel(s.destRoot, dest); err == nil && !escapes(rel) {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(filepath.Clean(dest))
}

// escapes reports whether a relative path climbs out of its base ("..") — a
// dest not actually under the root, whose relative key could collide with a
// genuine key. Such a dest uses the full cleaned path instead.
func escapes(rel string) bool {
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
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
