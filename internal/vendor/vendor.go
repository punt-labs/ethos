// Package vendor snapshots a complete, self-standing identity set into a
// repo's .punt-labs/ethos/ (DES-057 Part B).
//
// It differs from `ethos export`, which converts one identity into a
// foreign format and loses roles, teams, and extensions by contract.
// Vendor copies native files and follows references to a fixed point, so
// the result resolves on a machine with no global ethos store at all.
//
// The command plans by default. It writes into git-tracked space, and
// the closure can be much larger than the handles named — see closure().
package vendor

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/punt-labs/ethos/internal/attribute"
	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/role"
	"github.com/punt-labs/ethos/internal/team"
)

// Sources is the read side of a vendor run.
//
// Roots is the layered read chain in precedence order (repo, bundle,
// global) — the same order the layered stores consult. Vendor resolves
// the FILE behind each reference by probing these roots itself, because
// a layered store reports the content it resolved, not which layer the
// bytes came from, and vendor copies bytes.
type Sources struct {
	Roots      []string
	Identities identity.IdentityStore
	Roles      *role.LayeredStore
	Teams      *team.LayeredStore
}

// Options are the command-line surface of a vendor run.
type Options struct {
	Handles      []string // explicit seeds
	Team         string   // seed from a team's members
	All          bool     // seed from every readable identity
	Dest         string   // --to; the vendored set's root
	Prune        bool     // remove managed files the closure does not contain
	Apply        bool     // write; without it the run only plans
	AllowExtKeys []string // per-key credential-guard overrides, "<ns>/<key>"
}

// ExtFile is one extension base file in the closure.
type ExtFile struct {
	Namespace string
	File      string // base file name, e.g. "quarry.yaml"
	Src       string // absolute source path
}

// Plan is what a vendor run would write (or wrote, under --apply). It is
// the whole user-facing result: the closure, the blast radius, the
// credential findings, and what --prune would remove.
type Plan struct {
	Dest          string   `json:"dest"`
	Seeds         []string `json:"seeds"`
	Identities    []string `json:"identities"`
	Personalities []string `json:"personalities,omitempty"`
	WritingStyles []string `json:"writing_styles,omitempty"`
	Talents       []string `json:"talents,omitempty"`
	Roles         []string `json:"roles,omitempty"`
	Teams         []string `json:"teams,omitempty"`
	// Ext is keyed by handle. It stays out of the wire shape: consumers
	// need the count, and the authoritative per-file record is the
	// manifest, not a command response.
	Ext          map[string][]ExtFile `json:"-"`
	ExtFiles     int                  `json:"ext_files"`
	Pruned       []string             `json:"pruned,omitempty"`
	Warnings     []Finding            `json:"warnings,omitempty"`
	Applied      bool                 `json:"applied"`
	FilesWritten int                  `json:"files_written"`
}

// ExtCount returns the number of extension base files in the closure.
func (p *Plan) ExtCount() int {
	n := 0
	for _, files := range p.Ext {
		n += len(files)
	}
	return n
}

// Vendorer runs the closure walk and the copy.
type Vendorer struct {
	src   Sources
	opts  Options
	allow allowSet
}

// New validates options and returns a Vendorer.
//
// --no-teams and --from are rejected at the flag layer rather than here;
// both would let a user produce an incomplete set that the command still
// calls complete, which is the one guarantee vendor makes.
func New(src Sources, opts Options) (*Vendorer, error) {
	if src.Identities == nil || src.Teams == nil || src.Roles == nil {
		return nil, fmt.Errorf("vendor: identity, team, and role stores are required")
	}
	if len(src.Roots) == 0 {
		return nil, fmt.Errorf("vendor: at least one source root is required")
	}
	if opts.Dest == "" {
		return nil, fmt.Errorf("vendor: a destination is required")
	}
	allow, err := parseAllowExtKeys(opts.AllowExtKeys)
	if err != nil {
		return nil, err
	}
	return &Vendorer{src: src, opts: opts, allow: allow}, nil
}

// Run computes the closure, enforces the credential guard, and — under
// --apply — writes the snapshot, its manifest, and verifies the result
// before reporting success.
func (v *Vendorer) Run() (*Plan, error) {
	seeds, err := v.seeds()
	if err != nil {
		return nil, err
	}
	if len(seeds) == 0 {
		return nil, fmt.Errorf("vendor: no identities selected — name one or more handles, or use --team or --all")
	}

	p, err := v.closure(seeds)
	if err != nil {
		return nil, err
	}
	p.Dest = v.opts.Dest
	p.ExtFiles = p.ExtCount()

	// The guard runs before anything is written, on the plan, so a
	// blocked key stops a dry run too. A warning that fires only after
	// the bytes are in the working tree is one `git add` from being
	// committed and is invisible in CI.
	warnings, err := v.guard(p)
	if err != nil {
		return nil, err
	}
	p.Warnings = warnings

	pruned, err := v.prunable(p)
	if err != nil {
		return nil, err
	}
	p.Pruned = pruned

	if !v.opts.Apply {
		return p, nil
	}

	if err := v.write(p); err != nil {
		return nil, err
	}
	if err := writeManifest(v.opts.Dest, buildManifest(p, time.Now())); err != nil {
		return nil, err
	}
	if v.opts.Prune {
		if err := v.prune(p); err != nil {
			return nil, err
		}
	}
	p.Applied = true

	// Verify what was actually written, not what was planned. The
	// postcondition here IS repo-only's precondition, so the producing
	// half and the consuming half cannot drift.
	if err := Verify(v.opts.Dest); err != nil {
		return nil, fmt.Errorf("vendor wrote an incomplete set: %w", err)
	}
	return p, nil
}

// seeds resolves the starting handles. --all is mutually exclusive with
// --team and explicit positional handles, so it is the sole path when
// used. Otherwise --team and explicit positional handles combine: the
// result is their union, deduplicated.
func (v *Vendorer) seeds() ([]string, error) {
	if v.opts.All {
		result, err := v.src.Identities.List()
		if err != nil {
			return nil, fmt.Errorf("listing identities: %w", err)
		}
		// An unreadable identity means --all would silently vendor a
		// subset. Refuse rather than produce a set that claims to be
		// everything.
		if len(result.Warnings) > 0 {
			return nil, fmt.Errorf("vendor --all: some identities are unreadable: %s",
				strings.Join(result.Warnings, "; "))
		}
		out := make([]string, 0, len(result.Identities))
		for _, id := range result.Identities {
			out = append(out, id.Handle)
		}
		sort.Strings(out)
		return out, nil
	}

	var out []string
	if v.opts.Team != "" {
		t, err := v.src.Teams.Load(v.opts.Team)
		if err != nil {
			return nil, fmt.Errorf("loading team %q: %w", v.opts.Team, err)
		}
		for _, m := range t.Members {
			out = appendUnique(out, m.Identity)
		}
	}
	for _, h := range v.opts.Handles {
		out = appendUnique(out, h)
	}
	sort.Strings(out)
	return out, nil
}

// extFiles lists an identity's extension BASE files in its source layer.
//
// `<ns>.local.yaml` is skipped by name, and only regular files are
// accepted. Both matter: a symlink named `quarry.yaml` pointing at
// `quarry.local.yaml` — or at ~/.ssh/id_rsa — would smuggle content past
// the name skip if vendor followed it, so the check is lstat and the
// answer for a non-regular file is a refusal, not a silent skip.
func (v *Vendorer) extFiles(handle string) ([]ExtFile, error) {
	dir, ok := v.find(filepath.Join("identities", handle+".ext"))
	if !ok {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading extensions for %q: %w", handle, err)
	}
	var out []ExtFile
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, identity.ExtLocalSuffix) || !strings.HasSuffix(name, ".yaml") {
			continue
		}
		src := filepath.Join(dir, name)
		info, err := os.Lstat(src)
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", src, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf(
				"refusing to vendor %s: not a regular file (mode %s) — a link here could copy content the name-based .local skip is meant to exclude",
				src, info.Mode().Type())
		}
		out = append(out, ExtFile{
			Namespace: strings.TrimSuffix(name, ".yaml"),
			File:      name,
			Src:       src,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].File < out[j].File })
	return out, nil
}

// guard classifies every ext key name in the closure and refuses the run
// when any is credential-named. Returns the WARN-class findings.
func (v *Vendorer) guard(p *Plan) ([]Finding, error) {
	var blocked, warned []Finding
	for _, handle := range p.Identities {
		for _, e := range p.Ext[handle] {
			keys, err := readExtKeys(e.Src)
			if err != nil {
				return nil, fmt.Errorf("reading %s: %w", e.Src, err)
			}
			for _, k := range keys {
				f := Finding{Handle: handle, Namespace: e.Namespace, Key: k, Verdict: Classify(k)}
				switch {
				case f.Verdict == Block && v.allow.allows(f):
					// An explicit per-key exemption. Still surfaced, so the
					// decision stays visible in the run's output.
					warned = append(warned, f)
				case f.Verdict == Block:
					blocked = append(blocked, f)
				case f.Verdict == Warn:
					warned = append(warned, f)
				}
			}
		}
	}
	if len(blocked) > 0 {
		return nil, blockError(blocked)
	}
	return sortFindings(warned), nil
}

// write copies the closure into the destination.
func (v *Vendorer) write(p *Plan) error {
	type item struct{ src, dst string }
	var items []item

	add := func(rel string) error {
		src, ok := v.find(rel)
		if !ok {
			return fmt.Errorf("vendor: %s not found in any source layer", rel)
		}
		items = append(items, item{src, filepath.Join(v.opts.Dest, rel)})
		return nil
	}

	for _, h := range p.Identities {
		if err := add(filepath.Join("identities", h+".yaml")); err != nil {
			return err
		}
		for _, e := range p.Ext[h] {
			items = append(items, item{e.Src, filepath.Join(v.opts.Dest, "identities", h+".ext", e.File)})
		}
	}
	for _, group := range []struct {
		dir   string
		ext   string
		slugs []string
	}{
		{attribute.Personalities.DirName, ".md", p.Personalities},
		{attribute.WritingStyles.DirName, ".md", p.WritingStyles},
		{attribute.Talents.DirName, ".md", p.Talents},
		{"roles", ".yaml", p.Roles},
		{"teams", ".yaml", p.Teams},
	} {
		for _, slug := range group.slugs {
			if err := add(filepath.Join(group.dir, slug+group.ext)); err != nil {
				return err
			}
		}
	}

	for _, it := range items {
		if err := copyRegular(it.src, it.dst); err != nil {
			return err
		}
	}
	p.FilesWritten = len(items)
	return nil
}

// find returns the first source root holding rel, honoring layer
// precedence. Only regular files and directories are accepted, and the
// stat is an lstat, so no link in a source layer can redirect a copy.
func (v *Vendorer) find(rel string) (string, bool) {
	for _, root := range v.src.Roots {
		if root == "" {
			continue
		}
		p := filepath.Join(root, rel)
		info, err := os.Lstat(p)
		if err != nil {
			continue
		}
		if info.IsDir() || info.Mode().IsRegular() {
			return p, true
		}
	}
	return "", false
}

// copyRegular copies one regular file, creating parent directories.
// The source is lstat'd: vendor never follows a link out of the store.
func copyRegular(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return fmt.Errorf("stat %s: %w", src, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("refusing to vendor %s: not a regular file (mode %s)", src, info.Mode().Type())
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("reading %s: %w", src, err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(dst), err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", dst, err)
	}
	return nil
}

// managedDirs are the only directories vendor writes or prunes. Anything
// else under the destination — agents/, missions/, CLAUDE.md — belongs
// to the repo, not to vendor.
var managedDirs = []string{
	"identities",
	attribute.Personalities.DirName,
	attribute.WritingStyles.DirName,
	attribute.Talents.DirName,
	"roles",
	"teams",
}

// prunable lists managed files the destination holds that the closure
// does not, so a plan shows what --prune would remove before it removes
// it. `*.local.yaml` is never listed: it is untracked machine-local
// state that vendor does not own and must not delete.
func (v *Vendorer) prunable(p *Plan) ([]string, error) {
	keep := map[string]bool{ManifestPath(v.opts.Dest): true}
	for _, h := range p.Identities {
		keep[filepath.Join(v.opts.Dest, "identities", h+".yaml")] = true
		for _, e := range p.Ext[h] {
			keep[filepath.Join(v.opts.Dest, "identities", h+".ext", e.File)] = true
		}
	}
	for _, g := range []struct {
		dir, ext string
		slugs    []string
	}{
		{attribute.Personalities.DirName, ".md", p.Personalities},
		{attribute.WritingStyles.DirName, ".md", p.WritingStyles},
		{attribute.Talents.DirName, ".md", p.Talents},
		{"roles", ".yaml", p.Roles},
		{"teams", ".yaml", p.Teams},
	} {
		for _, slug := range g.slugs {
			keep[filepath.Join(v.opts.Dest, g.dir, slug+g.ext)] = true
		}
	}

	var out []string
	for _, dir := range managedDirs {
		root := filepath.Join(v.opts.Dest, dir)
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if d.IsDir() || keep[path] {
				return nil
			}
			if strings.HasSuffix(d.Name(), identity.ExtLocalSuffix) {
				return nil // machine-local, not vendor's to delete
			}
			out = append(out, path)
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("scanning %s: %w", root, err)
		}
	}
	sort.Strings(out)
	return out, nil
}

// prune removes the files prunable listed.
func (v *Vendorer) prune(p *Plan) error {
	for _, path := range p.Pruned {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("pruning %s: %w", path, err)
		}
	}
	return nil
}
