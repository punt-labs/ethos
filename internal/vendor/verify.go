package vendor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/repomiss"
	"github.com/punt-labs/ethos/internal/role"
	"github.com/punt-labs/ethos/internal/team"
)

// Report is the completeness verdict for a vendored set.
type Report struct {
	// Root is the set that was checked.
	Root string
	// Missing is every reference the set does not satisfy, aggregated so
	// one `ethos vendor` round closes the whole gap.
	Missing []repomiss.MissingRef
	// ExtUnverifiable is true when the set has no .vendor.yaml. A
	// directory listing cannot tell "this identity has no extensions"
	// from "vendor omitted them", so the limit is reported rather than
	// silently assumed away.
	ExtUnverifiable bool
	// Handles is what was checked, for the "N identities" summary.
	Handles []string
}

// Complete reports whether the set satisfies every reference.
func (r *Report) Complete() bool {
	return len(r.Missing) == 0
}

// Summary is a one-line verdict for a health check.
func (r *Report) Summary() string {
	if r.Complete() {
		s := fmt.Sprintf("%d identities resolve with no global fallback", len(r.Handles))
		if r.ExtUnverifiable {
			s += "; no " + ManifestName + " — extension completeness unverifiable"
		}
		return s
	}
	refs := make([]string, 0, len(r.Missing))
	for _, m := range r.Missing {
		refs = append(refs, m.String())
	}
	return "missing " + strings.Join(refs, ", ")
}

// Err returns the aggregate error, or nil when the set is complete.
func (r *Report) Err() error {
	if r.Complete() {
		return nil
	}
	refs := make([]string, 0, len(r.Missing))
	for _, m := range r.Missing {
		refs = append(refs, fmt.Sprintf("%s (looked in %s)", m, m.Path))
	}
	return fmt.Errorf("%s does not resolve on its own — missing %s", r.Root, strings.Join(refs, ", "))
}

// Verify is Check reduced to an error, for callers that only gate.
func Verify(roots ...string) error {
	r, err := Check(roots...)
	if err != nil {
		return err
	}
	return r.Err()
}

// Check answers one question: do these layers resolve every identity
// they contain, using nothing but themselves?
//
// It is the predicate on BOTH sides of the feature. `ethos vendor` runs
// it on what it just wrote, before reporting success; `ethos doctor`
// runs it as the repo-only completeness gate. One implementation means
// the producing half and the consuming half cannot disagree about what
// "complete" means.
//
// roots is the read chain in precedence order — the repo layer, then an
// active bundle. Both are accepted because repo-only is legal with
// identities supplied entirely by a repo-local bundle and no
// .punt-labs/ethos/ directory at all; checking only the first root would
// fail a layout that resolves perfectly at runtime (Bugbot, PR #410).
// Vendor passes the one directory it wrote.
//
// It works by building a repo-only store over those layers with no
// global layer and resolving through it — the same code path a
// global-less checkout takes, not a reimplementation of it.
func Check(roots ...string) (*Report, error) {
	var repoRoot, bundleRoot string
	for _, r := range roots {
		switch {
		case r == "":
		case repoRoot == "":
			repoRoot = r
		case bundleRoot == "":
			bundleRoot = r
		}
	}
	if repoRoot == "" {
		return nil, fmt.Errorf("vendor: at least one root is required to check completeness")
	}
	rep := &Report{Root: repoRoot}

	// repo-only already makes the global layer unreachable, but the stores
	// still need a root for it. An EMPTY root would resolve relative to
	// the process's working directory — so a future change that read the
	// layer would silently pick up whatever happens to be under the cwd.
	// Point it at a path that cannot exist instead, so any such read fails
	// loudly rather than answering with someone else's identities.
	noGlobal := filepath.Join(repoRoot, ".vendor-check-has-no-global-layer")
	var bundleStore *identity.Store
	if bundleRoot != "" {
		bundleStore = identity.NewStore(bundleRoot)
	}
	store := identity.NewLayeredStoreWithBundle(
		identity.NewStore(repoRoot), bundleStore, identity.NewStore(noGlobal), true)
	roles := role.NewLayeredStoreWithBundle(repoRoot, bundleRoot, noGlobal, true)
	teams := team.NewLayeredStoreWithBundle(repoRoot, bundleRoot, noGlobal, true)

	list, err := store.List()
	if err != nil {
		return nil, fmt.Errorf("listing identities in %s: %w", repoRoot, err)
	}
	if len(list.Warnings) > 0 {
		return nil, fmt.Errorf("unreadable identities in %s: %s", repoRoot, strings.Join(list.Warnings, "; "))
	}
	for _, id := range list.Identities {
		rep.Handles = append(rep.Handles, id.Handle)
	}
	sort.Strings(rep.Handles)

	// Identity dimension: every identity must resolve its attributes.
	for _, handle := range rep.Handles {
		_, err := store.Load(handle)
		var incomplete *repomiss.ErrIncompleteRepoSet
		switch {
		case err == nil:
		case errors.As(err, &incomplete):
			rep.Missing = append(rep.Missing, incomplete.Missing...)
		default:
			return nil, fmt.Errorf("loading identity %q from %s: %w", handle, repoRoot, err)
		}
	}

	// Team dimension: every team must validate against this layer alone —
	// its members and their roles must all be present here.
	teamNames, err := teams.List()
	if err != nil {
		return nil, fmt.Errorf("listing teams in %s: %w", repoRoot, err)
	}
	sort.Strings(teamNames)
	for _, name := range teamNames {
		t, err := teams.Load(name)
		if err != nil {
			return nil, fmt.Errorf("loading team %q from %s: %w", name, repoRoot, err)
		}
		rep.Missing = append(rep.Missing, missingTeamRefs(repoRoot, t, store, roles)...)
	}

	// Ext dimension: manifest parity. Extra files and .local companions
	// are ignored; only a manifest-recorded base file that is absent
	// counts, since that is the only case that proves an omission.
	layers := []string{repoRoot}
	if bundleRoot != "" {
		layers = append(layers, bundleRoot)
	}
	m, err := firstManifest(layers)
	if err != nil {
		return nil, err
	}
	if m == nil {
		rep.ExtUnverifiable = true
	} else {
		rep.Missing = append(rep.Missing, missingExtRefs(layers, m.RequiredExt())...)
	}

	rep.Missing = repomiss.Sorted(rep.Missing)
	return rep, nil
}

// missingTeamRefs reports the members and roles a team names that the
// set does not hold.
func missingTeamRefs(root string, t *team.Team, store identity.IdentityStore, roles *role.LayeredStore) []repomiss.MissingRef {
	var out []repomiss.MissingRef
	seen := map[string]bool{}
	record := func(kind, slug, path string) {
		key := kind + "/" + slug
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, repomiss.MissingRef{Kind: kind, Slug: slug, Path: path})
	}
	for _, m := range t.Members {
		if !store.Exists(m.Identity) {
			record(repomiss.KindIdentity, m.Identity,
				filepath.Join(root, "identities", m.Identity+".yaml"))
		}
		if !roles.Exists(m.Role) {
			record(repomiss.KindRole, m.Role, filepath.Join(root, "roles", m.Role+".yaml"))
		}
	}
	return out
}

// firstManifest returns the manifest from the highest-precedence layer
// that has one, or (nil, nil) when none does. A vendored set carries its
// manifest at its own root, and that root may be the repo layer or a
// repo-local bundle.
func firstManifest(layers []string) (*Manifest, error) {
	for _, root := range layers {
		m, err := LoadManifest(root)
		if err != nil {
			return nil, err
		}
		if m != nil {
			return m, nil
		}
	}
	return nil, nil
}

// missingExtRefs reports manifest-recorded ext base files absent from
// every layer. This is a subset check by design: an extra namespace
// someone added by hand is not an incompleteness.
func missingExtRefs(layers []string, required map[string][]string) []repomiss.MissingRef {
	handles := make([]string, 0, len(required))
	for h := range required {
		handles = append(handles, h)
	}
	sort.Strings(handles)

	var out []repomiss.MissingRef
	for _, h := range handles {
		for _, file := range required[h] {
			rel := filepath.Join("identities", h+".ext", file)
			if anyLayerHasRegular(layers, rel) {
				continue
			}
			out = append(out, repomiss.MissingRef{
				Kind: repomiss.KindExt,
				Slug: h + "/" + strings.TrimSuffix(file, ".yaml"),
				// The highest-precedence layer is where vendor would put it.
				Path: filepath.Join(layers[0], rel),
			})
		}
	}
	return out
}

// anyLayerHasRegular reports whether rel names a regular file in any
// layer. lstat, not stat: a link is not a vendored file.
func anyLayerHasRegular(layers []string, rel string) bool {
	for _, root := range layers {
		info, err := os.Lstat(filepath.Join(root, rel))
		if err == nil && info.Mode().IsRegular() {
			return true
		}
	}
	return false
}
