package seed

import (
	"embed"
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

	// Skills
	seedFile(Skills, "sidecar/skills/baseline-ops/SKILL.md",
		filepath.Join(skillsRoot, "baseline-ops", "SKILL.md"), force, r)

	// READMEs
	seedReadmes(Readmes, destRoot, force, r)

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
	_ = fs.WalkDir(fsys, "sidecar", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "README.md" {
			return nil
		}
		// path is like "sidecar/roles/README.md"
		// rel becomes "roles/README.md"
		rel := strings.TrimPrefix(path, "sidecar/")
		dest := filepath.Join(destRoot, rel)
		data, readErr := fs.ReadFile(fsys, path)
		if readErr != nil {
			r.Errors = append(r.Errors, fmt.Sprintf("reading %s: %v", path, readErr))
			return nil
		}
		writeFile(dest, data, force, r)
		return nil
	})
}

func writeFile(dest string, data []byte, force bool, r *Result) {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		r.Errors = append(r.Errors, fmt.Sprintf("mkdir %s: %v", filepath.Dir(dest), err))
		return
	}

	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if force {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}

	f, err := os.OpenFile(dest, flags, 0o644)
	if err != nil {
		if os.IsExist(err) {
			r.Skipped = append(r.Skipped, dest)
			return
		}
		r.Errors = append(r.Errors, fmt.Sprintf("writing %s: %v", dest, err))
		return
	}
	defer f.Close()

	if _, err := f.Write(data); err != nil {
		r.Errors = append(r.Errors, fmt.Sprintf("writing %s: %v", dest, err))
		return
	}
	r.Deployed = append(r.Deployed, dest)
}
