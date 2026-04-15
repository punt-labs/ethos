package bundle

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/punt-labs/ethos/internal/resolve"
)

// ResolveActive returns the currently active bundle for a repo, or nil
// if no bundle is active.
//
// The active bundle is determined by:
//
//  1. active_bundle field in .punt-labs/ethos.yaml (explicit activation);
//     repo-local (<repoRoot>/.punt-labs/ethos-bundles/<name>/) wins over
//     global (<globalRoot>/bundles/<name>/).
//  2. Legacy compat: if .punt-labs/ethos/ exists as a directory and no
//     active_bundle is set, return a synthetic Bundle{Source: SourceLegacy}.
//  3. Otherwise nil (no bundle, use pure 2-layer resolution).
//
// When active_bundle names a bundle that cannot be found in either
// scope, an error is returned — the user asked for a specific bundle
// and we cannot silently fall back to legacy or nil.
func ResolveActive(repoRoot, globalRoot string) (*Bundle, error) {
	name, err := resolve.ResolveActiveBundle(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("resolving active bundle: %w", err)
	}

	if name != "" {
		if err := validateName(name); err != nil {
			return nil, fmt.Errorf("bundle: invalid active_bundle name %q: %w", name, err)
		}
		if repoRoot != "" {
			p := filepath.Join(repoRoot, ".punt-labs", "ethos-bundles", name)
			if isDir(p) {
				b, err := LoadBundle(p)
				if err != nil {
					return nil, fmt.Errorf("bundle %q: %w", name, err)
				}
				b.Source = SourceRepo
				return b, nil
			}
		}
		if globalRoot != "" {
			p := filepath.Join(globalRoot, "bundles", name)
			if isDir(p) {
				b, err := LoadBundle(p)
				if err != nil {
					return nil, fmt.Errorf("bundle %q: %w", name, err)
				}
				b.Source = SourceGlobal
				return b, nil
			}
		}
		return nil, fmt.Errorf("active bundle %q: not found in repo or global scope", name)
	}

	// No active_bundle set — check legacy dir.
	if repoRoot != "" {
		legacy := filepath.Join(repoRoot, ".punt-labs", "ethos")
		if isDir(legacy) {
			return &Bundle{
				Name:   "ethos",
				Path:   legacy,
				Source: SourceLegacy,
			}, nil
		}
	}

	return nil, nil
}

// List returns all discoverable bundles across repo-local, global, and
// legacy scopes. Repo-local and global bundles with the same name both
// appear — callers display Source to disambiguate.
//
// Results sorted by (Source: repo < global < legacy), then by Name.
func List(repoRoot, globalRoot string) ([]Bundle, error) {
	var out []Bundle

	if repoRoot != "" {
		dir := filepath.Join(repoRoot, ".punt-labs", "ethos-bundles")
		found, err := scanBundles(dir, SourceRepo)
		if err != nil {
			return nil, fmt.Errorf("listing repo bundles: %w", err)
		}
		out = append(out, found...)
	}

	if globalRoot != "" {
		dir := filepath.Join(globalRoot, "bundles")
		found, err := scanBundles(dir, SourceGlobal)
		if err != nil {
			return nil, fmt.Errorf("listing global bundles: %w", err)
		}
		out = append(out, found...)
	}

	if repoRoot != "" {
		legacy := filepath.Join(repoRoot, ".punt-labs", "ethos")
		if isDir(legacy) {
			out = append(out, Bundle{
				Name:   "ethos",
				Path:   legacy,
				Source: SourceLegacy,
			})
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Source != out[j].Source {
			return sourceRank(out[i].Source) < sourceRank(out[j].Source)
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// LoadBundle loads a bundle from a path and validates its structure.
// Returns an error if the path does not exist, is not a directory,
// has an invalid manifest name, or fails Validate. If bundle.yaml is
// present it is parsed; otherwise HasManifest is false and Name is
// derived from the directory basename.
func LoadBundle(path string) (*Bundle, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("bundle path %s: not a directory", path)
	}

	b := &Bundle{
		Name: filepath.Base(path),
		Path: path,
	}

	manifestPath := filepath.Join(path, "bundle.yaml")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			if err := b.Validate(); err != nil {
				return nil, err
			}
			return b, nil
		}
		return nil, fmt.Errorf("reading %s: %w", manifestPath, err)
	}
	if err := yaml.Unmarshal(data, &b.Manifest); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", manifestPath, err)
	}
	b.HasManifest = true
	if b.Manifest.Name != "" {
		if err := validateName(b.Manifest.Name); err != nil {
			return nil, fmt.Errorf("bundle %q: invalid manifest name %q: %w",
				filepath.Base(path), b.Manifest.Name, err)
		}
		b.Name = b.Manifest.Name
	}
	if err := b.Validate(); err != nil {
		return nil, err
	}
	return b, nil
}

// scanBundles returns every immediate subdirectory of dir as a Bundle,
// following symlinks so that symlinked bundle dirs are discovered.
// A missing dir is not an error — returns an empty slice. Invalid
// bundles are logged to stderr and skipped; a single broken entry
// must not poison the whole list.
func scanBundles(dir string, src Source) ([]Bundle, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}
	var out []Bundle
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if !isDir(p) {
			continue
		}
		b, err := LoadBundle(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ethos: skipping bundle %q: %v\n", e.Name(), err)
			continue
		}
		b.Source = src
		out = append(out, *b)
	}
	return out, nil
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func sourceRank(s Source) int {
	switch s {
	case SourceRepo:
		return 0
	case SourceGlobal:
		return 1
	case SourceLegacy:
		return 2
	}
	return 3
}
