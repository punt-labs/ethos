package seed

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Result tracks what was seeded.
type Result struct {
	Deployed []string // files written
	Skipped  []string // files that already existed (no-clobber)
	Repaired []string // zero-byte files overwritten (partial from an interrupted seed)
	Errors   []string // files that failed
}

// Seed deploys embedded sidecar content to the destination root.
// destRoot is typically ~/.punt-labs/ethos/.
// skillsRoot is typically ~/.claude/skills/.
// If force is true, existing files are overwritten.
func Seed(destRoot, skillsRoot string, force bool) (*Result, error) {
	r := &Result{}

	// Roles (skip README.md — handled separately)
	seedFS(Roles, "sidecar/roles", filepath.Join(destRoot, "roles"), ".yaml", force, r)

	// Talents (skip README.md — handled separately)
	seedFS(Talents, "sidecar/talents", filepath.Join(destRoot, "talents"), ".md", force, r)

	// Personalities and writing-styles: the conventional attributes that
	// setup-created identities reference, plus starter sidecar content.
	// A fresh machine resolves these from global when no bundle is active.
	seedFS(Personalities, "sidecar/personalities", filepath.Join(destRoot, "personalities"), ".md", force, r)
	seedFS(WritingStyles, "sidecar/writing-styles", filepath.Join(destRoot, "writing-styles"), ".md", force, r)

	// Archetypes
	seedFS(Archetypes, "sidecar/archetypes", filepath.Join(destRoot, "archetypes"), ".yaml", force, r)

	// Pipelines
	seedFS(Pipelines, "sidecar/pipelines", filepath.Join(destRoot, "pipelines"), ".yaml", force, r)

	// Skills
	seedFile(Skills, "sidecar/skills/baseline-ops/SKILL.md",
		filepath.Join(skillsRoot, "baseline-ops", "SKILL.md"), force, r)
	seedFile(Skills, "sidecar/skills/mission/SKILL.md",
		filepath.Join(skillsRoot, "mission", "SKILL.md"), force, r)
	seedFile(Skills, "sidecar/skills/create-from-project/SKILL.md",
		filepath.Join(skillsRoot, "create-from-project", "SKILL.md"), force, r)

	// READMEs
	seedReadmes(Readmes, destRoot, force, r)

	// Bundles (gstack and any other embedded team bundles).
	// Each top-level directory under sidecar/bundles/ deploys to
	// <destRoot>/bundles/<name>/ preserving its internal structure.
	seedBundles(Bundles, filepath.Join(destRoot, "bundles"), force, r)

	if len(r.Errors) > 0 {
		return r, fmt.Errorf("seed encountered %d errors", len(r.Errors))
	}
	return r, nil
}

func seedFS(fsys embed.FS, root, destDir, ext string, force bool, r *Result) {
	entries, err := fs.ReadDir(fsys, root)
	if err != nil {
		r.Errors = append(r.Errors, fmt.Sprintf("reading %s: %v", root, err))
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
			r.Errors = append(r.Errors, fmt.Sprintf("reading %s: %v", e.Name(), err))
			continue
		}
		dest := filepath.Join(destDir, e.Name())
		writeFile(dest, data, force, r)
	}
}

func seedFile(fsys embed.FS, src, dest string, force bool, r *Result) {
	data, err := fs.ReadFile(fsys, src)
	if err != nil {
		r.Errors = append(r.Errors, fmt.Sprintf("reading %s: %v", src, err))
		return
	}
	writeFile(dest, data, force, r)
}

func seedReadmes(fsys embed.FS, destRoot string, force bool, r *Result) {
	err := fs.WalkDir(fsys, "sidecar", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			r.Errors = append(r.Errors, fmt.Sprintf("walking %s: %v", path, walkErr))
			return nil
		}
		if d.IsDir() || d.Name() != "README.md" {
			return nil
		}
		// path is like "sidecar/roles/README.md"
		// rel becomes "roles/README.md"
		rel, relErr := filepath.Rel("sidecar", path)
		if relErr != nil {
			r.Errors = append(r.Errors, fmt.Sprintf("computing relative path for %s: %v", path, relErr))
			return nil
		}
		dest := filepath.Join(destRoot, rel)
		data, readErr := fs.ReadFile(fsys, path)
		if readErr != nil {
			r.Errors = append(r.Errors, fmt.Sprintf("reading %s: %v", path, readErr))
			return nil
		}
		writeFile(dest, data, force, r)
		return nil
	})
	if err != nil {
		r.Errors = append(r.Errors, fmt.Sprintf("walking sidecar for READMEs: %v", err))
	}
}

// seedBundles walks every file under sidecar/bundles/ and copies it
// to destBundlesRoot, preserving the path below "sidecar/bundles/".
// For example, sidecar/bundles/gstack/teams/gstack.yaml lands at
// <destBundlesRoot>/gstack/teams/gstack.yaml.
func seedBundles(fsys embed.FS, destBundlesRoot string, force bool, r *Result) {
	const root = "sidecar/bundles"
	err := fs.WalkDir(fsys, root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			r.Errors = append(r.Errors, fmt.Sprintf("walking %s: %v", path, walkErr))
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			r.Errors = append(r.Errors, fmt.Sprintf("computing relative path for %s: %v", path, relErr))
			return nil
		}
		data, readErr := fs.ReadFile(fsys, path)
		if readErr != nil {
			r.Errors = append(r.Errors, fmt.Sprintf("reading %s: %v", path, readErr))
			return nil
		}
		dest := filepath.Join(destBundlesRoot, rel)
		writeFile(dest, data, force, r)
		return nil
	})
	if err != nil {
		r.Errors = append(r.Errors, fmt.Sprintf("walking bundles: %v", err))
	}
}

func writeFile(dest string, data []byte, force bool, r *Result) {
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		r.Errors = append(r.Errors, fmt.Sprintf("mkdir %s: %v", filepath.Dir(dest), err))
		return
	}

	if force {
		if err := atomicWrite(dest, data); err != nil {
			r.Errors = append(r.Errors, fmt.Sprintf("writing %s: %v", dest, err))
			return
		}
		r.Deployed = append(r.Deployed, dest)
		return
	}

	installNoClobber(dest, data, r)
}

// installNoClobber writes dest without clobbering existing user content. A
// non-empty file is kept; a zero-byte file is a partial from an interrupted
// seed and is repaired. Creation uses os.Link, which fails atomically if
// dest already exists — closing the Stat→create race a plain
// stat-then-rename leaves open.
func installNoClobber(dest string, data []byte, r *Result) {
	if handled := classifyExisting(dest, data, r); handled {
		return
	}
	// dest was absent — create it atomically without clobbering a file that
	// races in between the Stat above and this create.
	err := linkInstall(dest, data)
	switch {
	case err == nil:
		r.Deployed = append(r.Deployed, dest)
	case errors.Is(err, os.ErrExist):
		// A file appeared in the race window. Re-decide against it once.
		if handled := classifyExisting(dest, data, r); !handled {
			r.Errors = append(r.Errors,
				fmt.Sprintf("writing %s: file appeared and vanished during install", dest))
		}
	default:
		r.Errors = append(r.Errors, fmt.Sprintf("writing %s: %v", dest, err))
	}
}

// classifyExisting acts on a dest that already exists: a non-empty file is
// skipped (no-clobber), a zero-byte file is repaired. It returns true when
// dest existed (action taken or error recorded), false when dest is absent
// and the caller should create it.
func classifyExisting(dest string, data []byte, r *Result) bool {
	info, err := os.Stat(dest)
	switch {
	case err == nil && info.Size() > 0:
		// A non-empty existing file is user content — never clobber it.
		r.Skipped = append(r.Skipped, dest)
		return true
	case err == nil:
		// A zero-byte file is a partial write left by an interrupted seed.
		// Repair replaces it with os.Rename. The only writers to these paths
		// are seed itself with deterministic embedded content (a racing
		// seeder writes identical bytes); a concurrent user hand-edit landing
		// in this window is accepted as negligible — stdlib offers no
		// portable exclusive-replace.
		if werr := atomicWrite(dest, data); werr != nil {
			r.Errors = append(r.Errors, fmt.Sprintf("repairing %s: %v", dest, werr))
		} else {
			r.Repaired = append(r.Repaired, dest)
		}
		return true
	case !os.IsNotExist(err):
		r.Errors = append(r.Errors, fmt.Sprintf("stat %s: %v", dest, err))
		return true
	}
	return false // absent
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
