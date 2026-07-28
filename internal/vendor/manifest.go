package vendor

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"gopkg.in/yaml.v3"
)

// ManifestName is the file `ethos vendor` writes at the root of a
// vendored set.
const ManifestName = ".vendor.yaml"

// ManifestVersion is the current schema version. A reader that meets a
// higher version refuses rather than guessing at a layout it does not
// know.
const ManifestVersion = 1

// VendoredIdentity records one identity in the closure and the ext base
// files vendor copied for it.
type VendoredIdentity struct {
	Handle string   `yaml:"handle"`
	Ext    []string `yaml:"ext,omitempty"` // base file names, e.g. "quarry.yaml"
}

// Manifest is ethos's own record of what `ethos vendor` copied.
//
// It exists because a directory listing cannot answer the question that
// matters: a repo with no `<handle>.ext/` directory might be an identity
// with no extensions, or an identity whose extensions vendor forgot.
// The manifest is the discriminator, so the ext half of a repo-only set
// can be checked for completeness at all (DES-057 Part A/B).
//
// It records only ethos's own output — handles and file names it wrote —
// never a consumer's extension VALUES, so it does not make ethos
// interpret extension data (DES-008).
type Manifest struct {
	Version       int                `yaml:"version"`
	GeneratedAt   time.Time          `yaml:"generated_at"`
	Seeds         []string           `yaml:"seeds"`
	Identities    []VendoredIdentity `yaml:"identities"`
	Personalities []string           `yaml:"personalities,omitempty"`
	WritingStyles []string           `yaml:"writing_styles,omitempty"`
	Talents       []string           `yaml:"talents,omitempty"`
	Roles         []string           `yaml:"roles,omitempty"`
	Teams         []string           `yaml:"teams,omitempty"`
}

// ManifestPath returns the manifest's location for a vendored set root.
func ManifestPath(root string) string {
	return filepath.Join(root, ManifestName)
}

// LoadManifest reads the manifest at the root of a vendored set.
// Returns (nil, nil) when there is none — a hand-authored set is legal,
// it just cannot have its ext completeness verified.
func LoadManifest(root string) (*Manifest, error) {
	path := ManifestPath(root)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if m.Version > ManifestVersion {
		return nil, fmt.Errorf(
			"%s: manifest version %d is newer than this ethos understands (%d) — upgrade ethos",
			path, m.Version, ManifestVersion)
	}
	return &m, nil
}

// RequiredExt returns the manifest's ext base files keyed by handle —
// the set Part A's repo-only ext-miss rule checks the source layer
// against. A nil manifest yields nil, which the store reads as "ext
// completeness is unverifiable here."
func (m *Manifest) RequiredExt() map[string][]string {
	if m == nil {
		return nil
	}
	out := make(map[string][]string, len(m.Identities))
	for _, id := range m.Identities {
		out[id.Handle] = id.Ext
	}
	return out
}

// Handles returns the vendored handles in manifest order.
func (m *Manifest) Handles() []string {
	if m == nil {
		return nil
	}
	out := make([]string, 0, len(m.Identities))
	for _, id := range m.Identities {
		out = append(out, id.Handle)
	}
	return out
}

// buildManifest turns a plan into the record written beside the snapshot.
func buildManifest(p *Plan, now time.Time) *Manifest {
	ids := make([]VendoredIdentity, 0, len(p.Identities))
	for _, h := range p.Identities {
		ext := make([]string, 0, len(p.Ext[h]))
		for _, e := range p.Ext[h] {
			ext = append(ext, e.File)
		}
		sort.Strings(ext)
		ids = append(ids, VendoredIdentity{Handle: h, Ext: ext})
	}
	return &Manifest{
		Version:       ManifestVersion,
		GeneratedAt:   now.UTC().Truncate(time.Second),
		Seeds:         p.Seeds,
		Identities:    ids,
		Personalities: p.Personalities,
		WritingStyles: p.WritingStyles,
		Talents:       p.Talents,
		Roles:         p.Roles,
		Teams:         p.Teams,
	}
}

// writeManifest serializes the manifest into the vendored set, with a
// header explaining what it is — the file lands in git, where the next
// reader is a human reviewing a diff.
func writeManifest(root string, m *Manifest) error {
	data, err := yaml.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshaling manifest: %w", err)
	}
	header := "# Written by `ethos vendor`. Records the vendored closure so ethos can\n" +
		"# verify this set is complete under `resolution: repo-only`.\n" +
		"# Do not edit by hand — re-run `ethos vendor --apply` instead.\n"
	path := ManifestPath(root)
	if err := os.WriteFile(path, append([]byte(header), data...), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
